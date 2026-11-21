// Package ingest recibe los envelopes de los SDK, agrupa los eventos en issues
// y lleva su estado.
package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

type Enricher interface {
	Enrich(ctx context.Context, projectID int64, ev *event.Event) error
}

type Outcome int

const (
	Unchanged Outcome = iota
	New
	Regressed
)

func (o Outcome) String() string {
	switch o {
	case New:
		return "new"
	case Regressed:
		return "regressed"
	default:
		return "unchanged"
	}
}

type Notifier interface {
	IssueChanged(ctx context.Context, tx pgx.Tx, ev *event.Event, res Result) error
}

type NopEnricher struct{}

func (NopEnricher) Enrich(context.Context, int64, *event.Event) error { return nil }

type NopNotifier struct{}

func (NopNotifier) IssueChanged(context.Context, pgx.Tx, *event.Event, Result) error {
	return nil
}

type Ingester struct {
	pool     *pgxpool.Pool
	enricher Enricher
	notifier Notifier
	log      *slog.Logger
	now      func() time.Time
	random   func() float64
}

// NewIngester acepta enricher y notifier nil: entonces no hacen nada.
func NewIngester(pool *pgxpool.Pool, enricher Enricher, notifier Notifier, log *slog.Logger) *Ingester {
	if enricher == nil {
		enricher = NopEnricher{}
	}
	if notifier == nil {
		notifier = NopNotifier{}
	}
	return &Ingester{pool: pool, enricher: enricher, notifier: notifier, log: log, now: time.Now, random: mrand.Float64}
}

// Limits son el tope y el muestreo de los eventos de issues que ya existen.
// El primer evento de un issue y el de una regresión se guardan siempre.
type Limits struct {
	// PerIssuePerHour es cuántos eventos se guardan como mucho por issue y hora.
	PerIssuePerHour int
	// SampleRate es la parte, de 0 a 1, de los eventos que se guardan.
	SampleRate float64
}

func DefaultLimits() Limits {
	return Limits{PerIssuePerHour: store.DefaultEventsPerIssuePerHour, SampleRate: 1}
}

func LimitsOf(f store.Filters) Limits {
	return Limits{PerIssuePerHour: f.PerIssuePerHour(), SampleRate: f.SampleRate}
}

type Result struct {
	IssueID int64
	Outcome Outcome
	// Duplicate indica que el event_id ya estaba guardado y no se ha contado.
	Duplicate bool
	// ResolvedAt es, en una regresión, desde cuándo estaba resuelto el issue.
	ResolvedAt *time.Time
	// Dropped es, si el evento cuenta en el issue pero no se guarda, por qué:
	// store.DropIssueLimit o store.DropSampled.
	Dropped string
}

var errDuplicate = errors.New("evento repetido")

// Ingest es IngestWith con los límites por defecto.
func (in *Ingester) Ingest(ctx context.Context, projectID int64, ev *event.Event) (Result, error) {
	return in.IngestWith(ctx, projectID, ev, DefaultLimits())
}

// IngestWith guarda un evento y actualiza su issue en una sola transacción.
// Pasado el tope o fuera de la muestra, el issue cuenta el evento pero el
// evento no se guarda.
func (in *Ingester) IngestWith(ctx context.Context, projectID int64, ev *event.Event, lim Limits) (Result, error) {
	if lim.PerIssuePerHour <= 0 {
		lim.PerIssuePerHour = store.DefaultEventsPerIssuePerHour
	}
	now := in.now().UTC()
	if !validID(ev.ID) {
		ev.ID = newID()
	}
	// Un reloj adelantado no puede dejar el issue visto en el futuro.
	if ev.Timestamp.IsZero() || ev.Timestamp.After(now.Add(time.Minute)) {
		ev.Timestamp = now
	}
	if err := in.enricher.Enrich(ctx, projectID, ev); err != nil {
		in.log.WarnContext(ctx, "enriquecer evento", "proyecto", projectID, "event_id", ev.ID, "err", err)
	}
	g := Group(ev)
	data, err := json.Marshal(ev)
	if err != nil {
		return Result{}, fmt.Errorf("serializar evento: %w", err)
	}

	var res Result
	err = pgx.BeginFunc(ctx, in.pool, func(tx pgx.Tx) error {
		var err error
		res, err = in.save(ctx, tx, projectID, ev, g, data, now, lim)
		if err != nil {
			return err
		}
		if err := in.notifier.IssueChanged(ctx, tx, ev, res); err != nil {
			return fmt.Errorf("avisar: %w", err)
		}
		return nil
	})
	if errors.Is(err, errDuplicate) {
		return Result{Duplicate: true}, nil
	}
	if err != nil {
		return Result{}, err
	}
	return res, nil
}

func (in *Ingester) save(ctx context.Context, tx pgx.Tx, projectID int64, ev *event.Event, g Grouping, data []byte, now time.Time, lim Limits) (Result, error) {
	res := Result{}
	hour := now.Truncate(time.Hour)
	found, err := lockIssue(ctx, tx, projectID, g.Fingerprint)
	if err != nil {
		return res, err
	}
	if found == nil {
		// Si otro evento con la misma huella abre el issue a la vez, el insert
		// espera a que acabe y no inserta; entonces se trata como uno que ya existía.
		err := tx.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, culprit, type, level, first_seen, last_seen, event_count,
				stored_hour, stored_in_hour, last_stored_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7, 1, $8, 1, $9)
			ON CONFLICT (project_id, fingerprint) DO NOTHING
			RETURNING id`,
			projectID, g.Fingerprint, g.Title, g.Culprit, g.Type, ev.Level, ev.Timestamp, hour, now).Scan(&res.IssueID)
		switch {
		case err == nil:
			res.Outcome = New
		case errors.Is(err, pgx.ErrNoRows):
			if found, err = lockIssue(ctx, tx, projectID, g.Fingerprint); err != nil {
				return res, err
			}
			if found == nil {
				return res, errors.New("el issue desapareció tras un conflicto")
			}
		default:
			return res, fmt.Errorf("crear issue: %w", err)
		}
	}

	if found != nil {
		res.IssueID = found.id
		status, regressed := transition(found.status, found.resolvedIn, ev.Release)
		if regressed {
			res.Outcome = Regressed
			res.ResolvedAt = found.resolvedAt
		}
		stored := 0
		if found.storedHour != nil && found.storedHour.Equal(hour) {
			stored = found.storedInHour
		}
		// El primer evento de cada día (UTC) salta el muestreo, para que el issue
		// siempre tenga un evento dentro de la retención y la purga no lo borre.
		firstToday := found.lastStoredAt == nil || found.lastStoredAt.Before(now.Truncate(24*time.Hour))
		if !regressed {
			switch {
			case stored >= lim.PerIssuePerHour:
				res.Dropped = store.DropIssueLimit
			case !firstToday && lim.SampleRate < 1 && in.random() >= lim.SampleRate:
				res.Dropped = store.DropSampled
			}
		}
		if res.Dropped == "" {
			stored++
		} else {
			// Un reenvío del mismo evento no debe contar dos veces, igual que si se guardara.
			var dup bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE project_id = $1 AND event_id = $2)`,
				projectID, ev.ID).Scan(&dup); err != nil {
				return res, fmt.Errorf("buscar evento: %w", err)
			}
			if dup {
				return res, errDuplicate
			}
		}
		_, err := tx.Exec(ctx, `
			UPDATE issues SET
				event_count = event_count + 1,
				first_seen = LEAST(first_seen, $2),
				last_seen = GREATEST(last_seen, $2),
				status = $3,
				resolved_in_release = CASE WHEN $4 THEN NULL ELSE resolved_in_release END,
				resolved_at = CASE WHEN $4 THEN NULL ELSE resolved_at END,
				last_regressed_at = CASE WHEN $4 THEN $5 ELSE last_regressed_at END,
				stored_hour = $6,
				stored_in_hour = $7,
				last_stored_at = CASE WHEN $8 THEN $5 ELSE last_stored_at END
			WHERE id = $1`,
			found.id, ev.Timestamp, status, regressed, now, hour, stored, res.Dropped == "")
		if err != nil {
			return res, fmt.Errorf("actualizar issue: %w", err)
		}
		if res.Dropped != "" {
			return res, nil
		}
	}

	var eventRow int64
	err = tx.QueryRow(ctx, `
		INSERT INTO events (issue_id, project_id, event_id, occurred_at, received_at, release, environment, data)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (project_id, event_id) DO NOTHING
		RETURNING id`,
		res.IssueID, projectID, ev.ID, ev.Timestamp, now, nullable(ev.Release), nullable(ev.Environment), data).Scan(&eventRow)
	if errors.Is(err, pgx.ErrNoRows) {
		return res, errDuplicate
	}
	if err != nil {
		return res, fmt.Errorf("guardar evento: %w", err)
	}
	return res, nil
}

type lockedIssue struct {
	id           int64
	status       string
	resolvedIn   string
	resolvedAt   *time.Time
	storedHour   *time.Time
	storedInHour int
	lastStoredAt *time.Time
}

func lockIssue(ctx context.Context, tx pgx.Tx, projectID int64, fp string) (*lockedIssue, error) {
	var li lockedIssue
	err := tx.QueryRow(ctx, `
		SELECT id, status, COALESCE(resolved_in_release, ''), resolved_at, stored_hour, stored_in_hour, last_stored_at FROM issues
		WHERE project_id = $1 AND fingerprint = $2
		FOR UPDATE`, projectID, fp).Scan(&li.id, &li.status, &li.resolvedIn, &li.resolvedAt, &li.storedHour, &li.storedInHour, &li.lastStoredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("buscar issue: %w", err)
	}
	return &li, nil
}

// transition da el estado del issue tras llegarle un evento y si es una regresión.
func transition(status, resolvedIn, release string) (string, bool) {
	switch status {
	case "resolved":
		return "open", true
	case "resolved_next":
		if release != "" && release != resolvedIn {
			return "open", true
		}
	}
	return status, false
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
