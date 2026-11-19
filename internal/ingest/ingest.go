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
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/intake-es/intake-errors/internal/event"
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
	IssueChanged(ctx context.Context, tx pgx.Tx, issueID int64, ev *event.Event, outcome Outcome) error
}

type NopEnricher struct{}

func (NopEnricher) Enrich(context.Context, int64, *event.Event) error { return nil }

type NopNotifier struct{}

func (NopNotifier) IssueChanged(context.Context, pgx.Tx, int64, *event.Event, Outcome) error {
	return nil
}

type Ingester struct {
	pool     *pgxpool.Pool
	enricher Enricher
	notifier Notifier
	log      *slog.Logger
	now      func() time.Time
}

// NewIngester acepta enricher y notifier nil: entonces no hacen nada.
func NewIngester(pool *pgxpool.Pool, enricher Enricher, notifier Notifier, log *slog.Logger) *Ingester {
	if enricher == nil {
		enricher = NopEnricher{}
	}
	if notifier == nil {
		notifier = NopNotifier{}
	}
	return &Ingester{pool: pool, enricher: enricher, notifier: notifier, log: log, now: time.Now}
}

type Result struct {
	IssueID int64
	Outcome Outcome
	// Duplicate indica que el event_id ya estaba guardado y no se ha contado.
	Duplicate bool
}

var errDuplicate = errors.New("evento repetido")

// Ingest guarda un evento y actualiza su issue en una sola transacción.
func (in *Ingester) Ingest(ctx context.Context, projectID int64, ev *event.Event) (Result, error) {
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
		res, err = in.save(ctx, tx, projectID, ev, g, data, now)
		if err != nil {
			return err
		}
		if err := in.notifier.IssueChanged(ctx, tx, res.IssueID, ev, res.Outcome); err != nil {
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

func (in *Ingester) save(ctx context.Context, tx pgx.Tx, projectID int64, ev *event.Event, g Grouping, data []byte, now time.Time) (Result, error) {
	res := Result{}
	found, err := lockIssue(ctx, tx, projectID, g.Fingerprint)
	if err != nil {
		return res, err
	}
	if found == nil {
		// Si otro evento con la misma huella abre el issue a la vez, el insert
		// espera a que acabe y no inserta; entonces se trata como uno que ya existía.
		err := tx.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, culprit, type, level, first_seen, last_seen, event_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7, 1)
			ON CONFLICT (project_id, fingerprint) DO NOTHING
			RETURNING id`,
			projectID, g.Fingerprint, g.Title, g.Culprit, g.Type, ev.Level, ev.Timestamp).Scan(&res.IssueID)
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
		}
		_, err := tx.Exec(ctx, `
			UPDATE issues SET
				event_count = event_count + 1,
				first_seen = LEAST(first_seen, $2),
				last_seen = GREATEST(last_seen, $2),
				status = $3,
				resolved_in_release = CASE WHEN $4 THEN NULL ELSE resolved_in_release END,
				last_regressed_at = CASE WHEN $4 THEN $5 ELSE last_regressed_at END
			WHERE id = $1`,
			found.id, ev.Timestamp, status, regressed, now)
		if err != nil {
			return res, fmt.Errorf("actualizar issue: %w", err)
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
	id         int64
	status     string
	resolvedIn string
}

func lockIssue(ctx context.Context, tx pgx.Tx, projectID int64, fp string) (*lockedIssue, error) {
	var li lockedIssue
	err := tx.QueryRow(ctx, `
		SELECT id, status, COALESCE(resolved_in_release, '') FROM issues
		WHERE project_id = $1 AND fingerprint = $2
		FOR UPDATE`, projectID, fp).Scan(&li.id, &li.status, &li.resolvedIn)
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
