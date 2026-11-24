package ingest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/envelope"
	"github.com/intake-es/mothlog/internal/store"
)

// MaxSessionReleases es cuántas releases puede tener un proyecto para que una
// sesión cree otra. Los eventos y la API las crean sin este tope.
const MaxSessionReleases = 10_000

// SessionBudget es cuántas veces el límite por minuto de eventos tiene el de
// sesiones, que va aparte: un navegador manda una o dos por página vista.
const SessionBudget = 10

// errSessionRelease es una sesión de una release nueva cuando el proyecto ya
// tiene MaxSessionReleases: se descarta y se cuenta.
var errSessionRelease = errors.New("the project has too many releases to create another one from a session")

// sessionRelease crea la release de una sesión si cabe.
func (in *Ingester) sessionRelease(ctx context.Context, tx db.Tx, projectID int64, release string, now time.Time) error {
	var id int64
	err := tx.QueryRow(ctx, `SELECT id FROM releases WHERE project_id = $1 AND version = $2`, projectID, release).Scan(&id)
	if err == nil {
		return nil
	}
	if !errors.Is(err, db.ErrNoRows) {
		return fmt.Errorf("find release: %w", err)
	}
	var n int64
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM releases WHERE project_id = $1`, projectID).Scan(&n); err != nil {
		return fmt.Errorf("count releases: %w", err)
	}
	if n >= in.maxReleases {
		return errSessionRelease
	}
	_, _, err = store.EnsureRelease(ctx, tx, projectID, release, now)
	return err
}

// SessionStateTTL es cuánto se recuerda una sesión sin noticias suyas: lo que
// llegue después de ella con init false ya no cuenta.
const SessionStateTTL = 24 * time.Hour

type sessionState struct {
	initSeen bool
	status   string
	errors   int64
	sentAt   time.Time
	userHash []byte
}

// counts es lo que suma la sesión en release_sessions; nada hasta ver su init.
func (s sessionState) counts() [4]int64 {
	if !s.initSeen {
		return [4]int64{}
	}
	f := envelope.Flags(s.status, s.errors)
	return [4]int64{1, b2i(f.Errored), b2i(f.Crashed), b2i(f.Abnormal)}
}

func b2i(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// clampTime deja t en now si no viene o va por delante del reloj.
func clampTime(t, now time.Time) time.Time {
	if t.IsZero() || t.After(now.Add(time.Minute)) {
		return now
	}
	return t.UTC()
}

func (in *Ingester) didHash(ctx context.Context, tx db.Tx, did string) ([]byte, error) {
	if did == "" {
		return nil, nil
	}
	key, err := in.userHashKey(ctx, tx)
	if err != nil {
		return nil, err
	}
	return store.UserHash(key, "id:"+did), nil
}

// IngestSession cuenta una sesión una sola vez, con su último estado: guarda
// lo que ya ha contado por su sid y suma solo la diferencia. Un reenvío o una
// actualización más vieja que la que ya se tiene no cambian nada.
func (in *Ingester) IngestSession(ctx context.Context, projectID int64, s *envelope.Session) error {
	now := in.now().UTC()
	started := clampTime(s.Started, now)
	sentAt := clampTime(s.Timestamp, now)
	sid := sha256.Sum256([]byte(s.SID))
	return db.BeginFunc(ctx, in.pool, func(tx db.Tx) error {
		user, err := in.didHash(ctx, tx, s.DID)
		if err != nil {
			return err
		}
		upd := sessionState{initSeen: s.Init, status: s.Status, errors: s.Errors, sentAt: sentAt, userHash: user}
		release, env, hour := s.Attrs.Release, s.Attrs.Environment, started.Truncate(time.Hour)

		var old sessionState
		err = tx.QueryRow(ctx, `
			INSERT INTO session_states (project_id, sid, release, environment, hour, init_seen, status, errors, sent_at, user_hash)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			ON CONFLICT (project_id, sid) DO NOTHING RETURNING true`,
			projectID, sid[:16], release, env, hour, upd.initSeen, upd.status, upd.errors, upd.sentAt, upd.userHash).Scan(new(bool))
		next := upd
		switch {
		case err == nil:
			if err := in.sessionRelease(ctx, tx, projectID, release, now); err != nil {
				return err
			}
		case errors.Is(err, db.ErrNoRows):
			// La release, el entorno y la hora son los de la primera vez que llegó.
			if err := tx.QueryRow(ctx, `
				SELECT release, environment, hour, init_seen, status, errors, sent_at, user_hash
				FROM session_states WHERE project_id = $1 AND sid = $2`+tx.Dialect().ForUpdate(), projectID, sid[:16]).
				Scan(&release, &env, &hour, &old.initSeen, &old.status, &old.errors, &old.sentAt, &old.userHash); err != nil {
				return fmt.Errorf("read session: %w", err)
			}
			next = merge(old, upd)
			if _, err := tx.Exec(ctx, `
				UPDATE session_states SET init_seen = $3, status = $4, errors = $5, sent_at = $6, user_hash = $7, updated_at = now()
				WHERE project_id = $1 AND sid = $2`,
				projectID, sid[:16], next.initSeen, next.status, next.errors, next.sentAt, next.userHash); err != nil {
				return fmt.Errorf("save session: %w", err)
			}
		default:
			return fmt.Errorf("save session: %w", err)
		}

		before, after := old.counts(), next.counts()
		var d [4]int64
		for i := range d {
			d[i] = after[i] - before[i]
		}
		if d != [4]int64{} {
			if err := addSessions(ctx, tx, projectID, release, env, hour, d); err != nil {
				return err
			}
		}
		if next.initSeen && next.userHash != nil {
			if err := addUser(ctx, tx, projectID, release, env, next.userHash, envelope.Flags(next.status, next.errors).Crashed, now); err != nil {
				return err
			}
		}
		return nil
	})
}

// merge aplica una actualización: el estado solo cambia con una más nueva y
// mientras la sesión no haya acabado.
func merge(old, upd sessionState) sessionState {
	next := old
	next.initSeen = old.initSeen || upd.initSeen
	if !envelope.Terminal(old.status) && !upd.sentAt.Before(old.sentAt) {
		next.status = upd.status
		next.sentAt = upd.sentAt
		next.errors = max(old.errors, upd.errors)
	}
	if next.userHash == nil {
		next.userHash = upd.userHash
	}
	return next
}

func addSessions(ctx context.Context, tx db.Tx, projectID int64, release, env string, hour time.Time, d [4]int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO release_sessions (project_id, release, environment, hour, total, errored, crashed, abnormal)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (project_id, release, environment, hour) DO UPDATE SET
			total = release_sessions.total + EXCLUDED.total,
			errored = release_sessions.errored + EXCLUDED.errored,
			crashed = release_sessions.crashed + EXCLUDED.crashed,
			abnormal = release_sessions.abnormal + EXCLUDED.abnormal`,
		projectID, release, env, hour, d[0], d[1], d[2], d[3])
	if err != nil {
		return fmt.Errorf("count sessions: %w", err)
	}
	return nil
}

func addUser(ctx context.Context, tx db.Tx, projectID int64, release, env string, user []byte, crashed bool, now time.Time) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO release_users (project_id, release, environment, user_hash, crashed, last_seen)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (project_id, release, environment, user_hash) DO UPDATE SET
			crashed = release_users.crashed OR EXCLUDED.crashed,
			last_seen = `+tx.Dialect().Greatest("release_users.last_seen", "EXCLUDED.last_seen"),
		projectID, release, env, user, crashed, now)
	if err != nil {
		return fmt.Errorf("count release users: %w", err)
	}
	return nil
}

// IngestSessionAggregates suma las sesiones que los SDK de servidor mandan ya
// agregadas. No llevan id, así que un reenvío del mismo item cuenta otra vez.
func (in *Ingester) IngestSessionAggregates(ctx context.Context, projectID int64, s *envelope.SessionAggregates) error {
	now := in.now().UTC()
	release, env := s.Attrs.Release, s.Attrs.Environment
	return db.BeginFunc(ctx, in.pool, func(tx db.Tx) error {
		if err := in.sessionRelease(ctx, tx, projectID, release, now); err != nil {
			return err
		}
		hours := map[time.Time][4]int64{}
		for _, a := range s.Aggregates {
			total := a.Total()
			if total == 0 {
				continue
			}
			crashed := a.Crashed + a.Unhandled
			h := clampTime(a.Started, now).Truncate(time.Hour)
			c := hours[h]
			c[0] += total
			c[1] += a.Errored + a.Abnormal + crashed
			c[2] += crashed
			c[3] += a.Abnormal
			hours[h] = c
			user, err := in.didHash(ctx, tx, a.DID)
			if err != nil {
				return err
			}
			if user != nil {
				if err := addUser(ctx, tx, projectID, release, env, user, crashed > 0, now); err != nil {
					return err
				}
			}
		}
		for h, c := range hours {
			if err := addSessions(ctx, tx, projectID, release, env, h, c); err != nil {
				return err
			}
		}
		return nil
	})
}

// PurgeSessions borra las horas y los usuarios de antes de cutoff, y lo que se
// recordaba de las sesiones sin noticias desde hace SessionStateTTL.
func PurgeSessions(ctx context.Context, pool db.DB, cutoff, now time.Time) (int64, error) {
	var n int64
	for _, q := range []struct {
		sql string
		at  time.Time
	}{
		{`DELETE FROM release_sessions WHERE hour < $1`, cutoff},
		{`DELETE FROM release_users WHERE last_seen < $1`, cutoff},
		{`DELETE FROM session_states WHERE updated_at < $1`, now.Add(-SessionStateTTL)},
	} {
		tag, err := pool.Exec(ctx, q.sql, q.at)
		if err != nil {
			return n, fmt.Errorf("delete sessions: %w", err)
		}
		n += tag.RowsAffected()
	}
	return n, nil
}

// sessionItem es un item session o sessions ya leído.
type sessionItem struct {
	one *envelope.Session
	agg *envelope.SessionAggregates
}

// cost es lo que gasta del límite de sesiones: un item session, uno; un item
// sessions, uno por grupo, porque cada grupo puede crear un usuario.
func (s sessionItem) cost() int {
	if s.agg != nil {
		return max(1, len(s.agg.Aggregates))
	}
	return 1
}

func sessionsCost(items []sessionItem) int {
	n := 0
	for _, s := range items {
		n += s.cost()
	}
	return n
}

func (s sessionItem) attrs() envelope.SessionAttrs {
	if s.one != nil {
		return s.one.Attrs
	}
	return s.agg.Attrs
}

// parseSessions lee los items session y sessions; los que no se pueden leer o
// no valen se cuentan como descartados.
func (h *Handler) parseSessions(ctx context.Context, projectID int64, items []envelope.Item) []sessionItem {
	var out []sessionItem
	for _, it := range items {
		var s sessionItem
		var err error
		if it.Header.Type == envelope.TypeSession {
			s.one, err = envelope.ParseSession(it.Payload)
		} else {
			s.agg, err = envelope.ParseSessionAggregates(it.Payload)
		}
		if err != nil {
			h.dropOther(projectID, store.DropSessionInvalid, 1)
			h.log.DebugContext(ctx, "session ignored", "project", projectID, "err", err)
			continue
		}
		out = append(out, s)
	}
	return out
}

func (h *Handler) sessions(ctx context.Context, projectID int64, items []sessionItem) {
	for _, s := range items {
		var err error
		if s.one != nil {
			err = h.ingester.IngestSession(ctx, projectID, s.one)
		} else {
			err = h.ingester.IngestSessionAggregates(ctx, projectID, s.agg)
		}
		// Una sesión que no se puede guardar no tumba los eventos del envío.
		switch {
		case errors.Is(err, errSessionRelease):
			h.dropOther(projectID, store.DropSessionReleaseLimit, 1)
			h.log.DebugContext(ctx, "session dropped", "project", projectID, "err", err)
		case err != nil:
			h.log.ErrorContext(ctx, "save session", "project", projectID, "err", err)
		}
	}
}
