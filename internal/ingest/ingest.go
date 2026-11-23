// Package ingest recibe los envelopes de los SDK, agrupa los eventos en issues
// y lleva su estado.
package ingest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	mrand "math/rand/v2"
	"sync/atomic"
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
	hashKey  atomic.Pointer[[]byte]
	batch    *batcher
}

// NewIngester acepta enricher y notifier nil: entonces no hacen nada.
func NewIngester(pool *pgxpool.Pool, enricher Enricher, notifier Notifier, log *slog.Logger) *Ingester {
	if enricher == nil {
		enricher = NopEnricher{}
	}
	if notifier == nil {
		notifier = NopNotifier{}
	}
	in := &Ingester{pool: pool, enricher: enricher, notifier: notifier, log: log, now: time.Now, random: mrand.Float64}
	in.batch = newBatcher(in)
	return in
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
	IssueID   int64
	ProjectID int64
	Outcome   Outcome
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

// IngestWith guarda un evento y actualiza su issue. Va en el lote de los que
// llegan a la vez, y vuelve cuando el lote está confirmado. Pasado el tope o
// fuera de la muestra, el issue cuenta el evento pero el evento no se guarda.
func (in *Ingester) IngestWith(ctx context.Context, projectID int64, ev *event.Event, lim Limits) (Result, error) {
	res, errs := in.ingestEvents(ctx, projectID, []*event.Event{ev}, lim, nil)
	return res[0], errs[0]
}

// ingestEvents guarda los eventos de un envío en el mismo lote, y da el
// resultado o el error de cada uno. arrived, si no es nil, se llama al
// entrar en la cola.
func (in *Ingester) ingestEvents(ctx context.Context, projectID int64, evs []*event.Event, lim Limits, arrived func()) ([]Result, []error) {
	res, errs := make([]Result, len(evs)), make([]error, len(evs))
	items := make([]*pending, 0, len(evs))
	for i, ev := range evs {
		it, err := in.prepare(ctx, projectID, ev, lim)
		if err != nil {
			errs[i] = err
			continue
		}
		items = append(items, it)
	}
	if len(items) > 0 {
		in.batch.submit(items, arrived)
	} else if arrived != nil {
		arrived()
	}
	j := 0
	for i := range evs {
		if errs[i] != nil {
			continue
		}
		res[i], errs[i] = items[j].res, items[j].err
		j++
	}
	return res, errs
}

// prepare hace lo de cada evento que no toca la base: el id, la fecha, los
// source maps, la huella y la codificación.
func (in *Ingester) prepare(ctx context.Context, projectID int64, ev *event.Event, lim Limits) (*pending, error) {
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
	data, err := store.EncodeEvent(ev)
	if err != nil {
		return nil, err
	}
	return &pending{ctx: ctx, projectID: projectID, ev: ev, g: Group(ev), data: data, now: now, lim: lim}, nil
}

// ingestOne guarda un evento solo en su transacción: es lo que se hace con
// los de un lote que ha fallado.
func (in *Ingester) ingestOne(ctx context.Context, it *pending) (Result, error) {
	var res Result
	err := pgx.BeginFunc(ctx, in.pool, func(tx pgx.Tx) error {
		var err error
		res, err = in.save(ctx, tx, it.projectID, it.ev, it.g, it.data, it.now, it.lim)
		if err != nil {
			return err
		}
		res.ProjectID = it.projectID
		if err := in.notifier.IssueChanged(ctx, tx, it.ev, res); err != nil {
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

func (in *Ingester) save(ctx context.Context, tx pgx.Tx, projectID int64, ev *event.Event, g Grouping, data store.EncodedEvent, now time.Time, lim Limits) (Result, error) {
	res := Result{}
	hour := now.Truncate(time.Hour)
	// newRelease: la release la crea este evento, así que no se sabe si es
	// nueva de verdad o la de un cliente viejo que no había mandado nada.
	newRelease := false
	if ev.Release != "" {
		var err error
		if _, newRelease, err = store.EnsureRelease(ctx, tx, projectID, ev.Release, now); err != nil {
			return res, err
		}
	}
	found, err := lockIssue(ctx, tx, projectID, g.Fingerprint)
	if err != nil {
		return res, err
	}
	if found == nil {
		// Si otro evento con la misma huella abre el issue a la vez, el insert
		// espera a que acabe y no inserta; entonces se trata como uno que ya existía.
		err := tx.QueryRow(ctx, `
			INSERT INTO issues (project_id, fingerprint, title, culprit, type, level, first_seen, last_seen, event_count,
				stored_hour, stored_in_hour, last_stored_at, first_release)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $7, 1, $8, 1, $9, $10)
			ON CONFLICT (project_id, fingerprint) DO NOTHING
			RETURNING id`,
			projectID, g.Fingerprint, g.Title, g.Culprit, g.Type, ev.Level, ev.Timestamp, hour, now, nullable(ev.Release)).Scan(&res.IssueID)
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
		status, regressed, err := transition(found.status, found.resolvedIn, found.fixedIn, ev.Release, newRelease,
			func(a, b string) (int, bool, bool, error) { return store.CompareReleases(ctx, tx, projectID, a, b) })
		if err != nil {
			return res, err
		}
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
		_, err = tx.Exec(ctx, `
			UPDATE issues SET
				event_count = event_count + 1,
				first_seen = LEAST(first_seen, $2),
				last_seen = GREATEST(last_seen, $2),
				status = $3,
				resolved_in_release = CASE WHEN $4 THEN NULL ELSE resolved_in_release END,
				fixed_in_release = CASE WHEN $4 THEN NULL ELSE fixed_in_release END,
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
		if regressed {
			if err := store.RecordRegression(ctx, tx, found.id, found.status, ev.Release); err != nil {
				return res, err
			}
		}
	}
	if err := store.TrackEvent(ctx, tx, projectID, res.IssueID, ev.Release, ev.Environment, ev.Timestamp, res.Outcome == Regressed); err != nil {
		return res, err
	}
	if res.Dropped != "" {
		return res, in.recordTags(ctx, tx, res.IssueID, ev)
	}

	eventRow, err := store.InsertEvent(ctx, tx, store.NewEvent{IssueID: res.IssueID, ProjectID: projectID, Event: ev, ReceivedAt: now, Encoded: data})
	if err != nil {
		return res, err
	}
	if eventRow == 0 {
		return res, errDuplicate
	}
	return res, in.recordTags(ctx, tx, res.IssueID, ev)
}

type lockedIssue struct {
	id           int64
	status       string
	resolvedIn   string
	fixedIn      string
	resolvedAt   *time.Time
	storedHour   *time.Time
	storedInHour int
	lastStoredAt *time.Time
}

func lockIssue(ctx context.Context, tx pgx.Tx, projectID int64, fp string) (*lockedIssue, error) {
	var li lockedIssue
	err := tx.QueryRow(ctx, `
		SELECT id, status, COALESCE(resolved_in_release, ''), COALESCE(fixed_in_release, ''), resolved_at,
			stored_hour, stored_in_hour, last_stored_at FROM issues
		WHERE project_id = $1 AND fingerprint = $2
		FOR UPDATE`, projectID, fp).Scan(&li.id, &li.status, &li.resolvedIn, &li.fixedIn, &li.resolvedAt,
		&li.storedHour, &li.storedInHour, &li.lastStoredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("buscar issue: %w", err)
	}
	return &li, nil
}

// transition da el estado del issue tras llegarle un evento y si es una
// regresión. Resuelto a mano, cualquier evento lo reabre. En resolved_next,
// uno de una release posterior a la que tenía al resolverse; resuelto con el
// despliegue de fixedIn, uno de esa release o de una posterior. Sin release
// no se sabe y no lo reabre. cmp compara dos releases como
// store.CompareReleases: por su número si se leen como versiones; si no, por
// cuándo se vieron, y entonces la release que crea el propio evento
// (newRelease) no cuenta como posterior, porque puede ser la de un cliente que
// no ha actualizado; una que no está en la tabla, sí.
func transition(status, resolvedIn, fixedIn, release string, newRelease bool,
	cmp func(a, b string) (c int, ok, byVersion bool, err error),
) (string, bool, error) {
	// after dice si release va después de than o, con orEqual, si es la misma
	// versión con otro nombre (1.1.0+build.2 y 1.1.0).
	after := func(than string, orEqual bool) (bool, error) {
		if release == "" {
			return false, nil
		}
		c, ok, byVersion, err := cmp(release, than)
		if err != nil || (newRelease && !byVersion) {
			return false, err
		}
		return !ok || c > 0 || (orEqual && c == 0), nil
	}
	var reopen bool
	var err error
	switch {
	case status == "resolved" && fixedIn == "":
		reopen = true
	case status == "resolved" && release == fixedIn:
		reopen = true
	case status == "resolved":
		reopen, err = after(fixedIn, true)
	case status == "resolved_next" && release != resolvedIn:
		reopen, err = after(resolvedIn, false)
	}
	if err != nil {
		return status, false, err
	}
	if reopen {
		return "open", true, nil
	}
	return status, false, nil
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
