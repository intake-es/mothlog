package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// DigestIssue es un issue en un resumen, con sus eventos en el periodo y en el anterior.
type DigestIssue struct {
	ID       int64  `json:"id"`
	Title    string `json:"title"`
	Culprit  string `json:"culprit"`
	Level    string `json:"level"`
	Status   string `json:"status"`
	Events   int64  `json:"events"`
	Previous int64  `json:"previous_events"`
	// Users son los usuarios afectados del issue, en total.
	Users     int64     `json:"users"`
	FirstSeen time.Time `json:"first_seen"`
	// Release es, en una regresión, la release con la que volvió.
	Release string `json:"release,omitempty"`
}

const digestIssueCols = `i.id, i.title, i.culprit, i.level, i.status, i.user_count, i.first_seen`

func scanDigestIssues(rows db.Rows, extra ...any) ([]DigestIssue, error) {
	out := []DigestIssue{}
	var d DigestIssue
	dest := append([]any{&d.ID, &d.Title, &d.Culprit, &d.Level, &d.Status, &d.Users, &d.FirstSeen, &d.Events, &d.Previous}, extra...)
	err := db.ForEachRow(rows, dest, func() error {
		out = append(out, d)
		return nil
	})
	return out, err
}

// eventsIn cuenta los eventos guardados del issue i entre dos fechas.
const eventsIn = `(SELECT count(*) FROM events e WHERE e.issue_id = i.id AND e.occurred_at >= %s AND e.occurred_at < %s)`

// DigestNewIssues son los issues que aparecieron en [start, end), los que más
// eventos tienen primero, hasta limit, y cuántos son en total.
func (s *Store) DigestNewIssues(ctx context.Context, projectID int64, start, end time.Time, limit int) ([]DigestIssue, int, error) {
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`
		SELECT `+digestIssueCols+`, `+eventsIn+` AS n, CAST(0 AS bigint), count(*) OVER ()
		FROM issues i
		WHERE i.project_id = $1 AND i.first_seen >= $2 AND i.first_seen < $3
		ORDER BY n DESC, i.first_seen, i.id LIMIT $4`, "$2", "$3"), projectID, start, end, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("issues nuevos del resumen: %w", err)
	}
	var total int
	out, err := scanDigestIssues(rows, &total)
	if err != nil {
		return nil, 0, fmt.Errorf("issues nuevos del resumen: %w", err)
	}
	return out, total, nil
}

// DigestRegressions son los issues que volvieron en [start, end) tras
// resolverse, con la release con la que volvieron la última vez.
func (s *Store) DigestRegressions(ctx context.Context, projectID int64, start, end time.Time, limit int) ([]DigestIssue, int, error) {
	changes := `FROM issue_status_changes c
			WHERE c.issue_id = i.id AND c.source = 'ingest' AND c.created_at >= $2 AND c.created_at < $3`
	rows, err := s.pool.Query(ctx, fmt.Sprintf(db.ByEngine(s.pool.Dialect(), `
		SELECT `+digestIssueCols+`, `+eventsIn+` AS n, CAST(0 AS bigint), count(*) OVER (), COALESCE(r.release, '')
		FROM issues i
		CROSS JOIN LATERAL (
			SELECT c.release `+changes+`
			ORDER BY c.created_at DESC, c.id DESC LIMIT 1) r
		WHERE i.project_id = $1 AND i.first_seen < $2
		ORDER BY n DESC, i.id LIMIT $4`,
		// Sin LATERAL: la release de la última vuelta, con una subconsulta.
		`
		SELECT `+digestIssueCols+`, `+eventsIn+` AS n, CAST(0 AS bigint), count(*) OVER (),
			COALESCE((SELECT c.release `+changes+` ORDER BY c.created_at DESC, c.id DESC LIMIT 1), '')
		FROM issues i
		WHERE i.project_id = $1 AND i.first_seen < $2 AND EXISTS (SELECT 1 `+changes+`)
		ORDER BY n DESC, i.id LIMIT $4`), "$2", "$3"), projectID, start, end, limit)
	if err != nil {
		return nil, 0, fmt.Errorf("regresiones del resumen: %w", err)
	}
	var total int
	var release string
	var out []DigestIssue
	var d DigestIssue
	err = db.ForEachRow(rows, []any{&d.ID, &d.Title, &d.Culprit, &d.Level, &d.Status, &d.Users, &d.FirstSeen,
		&d.Events, &d.Previous, &total, &release}, func() error {
		d.Release = release
		out = append(out, d)
		return nil
	})
	if err != nil {
		return nil, 0, fmt.Errorf("regresiones del resumen: %w", err)
	}
	if out == nil {
		out = []DigestIssue{}
	}
	return out, total, nil
}

// Lo que tiene que crecer un issue para salir entre los que crecen: al menos
// GrowthMinEvents eventos más que en el periodo anterior y el doble.
const (
	GrowthMinEvents = 5
	GrowthFactor    = 2
)

// DigestGrowing son los issues que ya existían y que más crecen en [start,
// end) respecto al periodo anterior de la misma duración, sin las regresiones.
func (s *Store) DigestGrowing(ctx context.Context, projectID int64, start, end time.Time, limit int) ([]DigestIssue, error) {
	prev := start.Add(-end.Sub(start))
	rows, err := s.pool.Query(ctx, `
		WITH counts AS (
			SELECT e.issue_id, count(*) FILTER (WHERE e.occurred_at >= $2) AS cur,
				count(*) FILTER (WHERE e.occurred_at < $2) AS prev
			FROM events e
			WHERE e.project_id = $1 AND e.occurred_at >= $4 AND e.occurred_at < $3
			GROUP BY e.issue_id)
		SELECT `+digestIssueCols+`, c.cur, c.prev
		FROM counts c JOIN issues i ON i.id = c.issue_id
		WHERE i.first_seen < $2 AND c.cur - c.prev >= $5 AND c.cur >= c.prev * $6
			AND NOT EXISTS (SELECT 1 FROM issue_status_changes x
				WHERE x.issue_id = i.id AND x.source = 'ingest' AND x.created_at >= $2 AND x.created_at < $3)
		ORDER BY c.cur - c.prev DESC, i.id LIMIT $7`,
		projectID, start, end, prev, GrowthMinEvents, GrowthFactor, limit)
	if err != nil {
		return nil, fmt.Errorf("issues que crecen: %w", err)
	}
	out, err := scanDigestIssues(rows)
	if err != nil {
		return nil, fmt.Errorf("issues que crecen: %w", err)
	}
	return out, nil
}

// DigestTotals son los números del periodo y los del anterior.
type DigestTotals struct {
	Events         int64 `json:"events"`
	PreviousEvents int64 `json:"previous_events"`
	// Issues son los issues con algún evento en el periodo.
	Issues int64 `json:"issues"`
	// Users son los usuarios distintos de los eventos del periodo, por su id,
	// correo, nombre o IP.
	Users int64 `json:"users"`
	// Resolved son los issues que se marcaron resueltos en el periodo.
	Resolved int64 `json:"resolved"`
}

// userIdent es el usuario de un evento guardado, como lo cuenta la ingesta.
func userIdent(d db.Dialect) string {
	return `COALESCE(e.user_ident, ` + legacy(d, legacyUserIdent) + `)`
}

// legacyUserIdent es userIdent leído de events.data, para los eventos aún sin
// convertir. Se va cuando se quite la columna data.
const legacyUserIdent = `CASE
	WHEN COALESCE(e.data->'user'->>'id', '') <> '' THEN 'id:' || (e.data->'user'->>'id')
	WHEN btrim(COALESCE(e.data->'user'->>'email', ''), E' \t\n\r') <> '' THEN 'email:' || lower(btrim(e.data->'user'->>'email', E' \t\n\r'))
	WHEN COALESCE(e.data->'user'->>'username', '') <> '' THEN 'username:' || (e.data->'user'->>'username')
	WHEN COALESCE(e.data->'user'->>'ip_address', '') NOT IN ('', '{{auto}}') THEN 'ip:' || (e.data->'user'->>'ip_address')
END`

// DigestTotals cuenta los eventos guardados, los usuarios y los resueltos de [start, end).
func (s *Store) DigestTotals(ctx context.Context, projectID int64, start, end time.Time) (DigestTotals, error) {
	var t DigestTotals
	prev := start.Add(-end.Sub(start))
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE e.occurred_at >= $2),
			count(*) FILTER (WHERE e.occurred_at < $2),
			count(DISTINCT e.issue_id) FILTER (WHERE e.occurred_at >= $2),
			count(DISTINCT `+userIdent(s.pool.Dialect())+`) FILTER (WHERE e.occurred_at >= $2)
		FROM events e
		WHERE e.project_id = $1 AND e.occurred_at >= $4 AND e.occurred_at < $3`,
		projectID, start, end, prev).Scan(&t.Events, &t.PreviousEvents, &t.Issues, &t.Users)
	if err != nil {
		return t, fmt.Errorf("totales del resumen: %w", err)
	}
	err = s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT c.issue_id) FROM issue_status_changes c JOIN issues i ON i.id = c.issue_id
		WHERE i.project_id = $1 AND c.to_status IN ('resolved', 'resolved_next') AND c.from_status NOT IN ('resolved', 'resolved_next')
			AND c.created_at >= $2 AND c.created_at < $3`, projectID, start, end).Scan(&t.Resolved)
	if err != nil {
		return t, fmt.Errorf("resueltos del resumen: %w", err)
	}
	return t, nil
}

// DigestCron es un monitor que falló o no llegó en el periodo.
type DigestCron struct {
	Slug     string `json:"slug"`
	Name     string `json:"name"`
	Failed   int64  `json:"failed"`
	Missed   int64  `json:"missed"`
	Timeouts int64  `json:"timeouts"`
	// Status es el de ahora.
	Status string `json:"status"`
}

// DigestCrons son los monitores con check-ins fallidos, sin llegar o pasados
// de tiempo en [start, end), los que más fallan primero.
func (s *Store) DigestCrons(ctx context.Context, projectID int64, start, end time.Time) ([]DigestCron, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.slug, m.name, count(*) FILTER (WHERE c.status = 'error'), count(*) FILTER (WHERE c.status = 'missed'),
			count(*) FILTER (WHERE c.status = 'timeout'), m.status
		FROM monitor_checkins c JOIN monitors m ON m.id = c.monitor_id
		WHERE m.project_id = $1 AND c.status IN ('error', 'missed', 'timeout')
			AND COALESCE(c.expected_at, c.started_at) >= $2 AND COALESCE(c.expected_at, c.started_at) < $3
		GROUP BY m.id
		ORDER BY count(*) DESC, `+s.pool.Dialect().Collate("m.slug"), projectID, start, end)
	if err != nil {
		return nil, fmt.Errorf("crons del resumen: %w", err)
	}
	out := []DigestCron{}
	var c DigestCron
	err = db.ForEachRow(rows, []any{&c.Slug, &c.Name, &c.Failed, &c.Missed, &c.Timeouts, &c.Status}, func() error {
		out = append(out, c)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("crons del resumen: %w", err)
	}
	return out, nil
}

// DigestUptime es un monitor de disponibilidad que estuvo caído en el periodo.
type DigestUptime struct {
	Slug      string `json:"slug"`
	Name      string `json:"name"`
	Incidents int64  `json:"incidents"`
	// DownMs es cuánto estuvo caído dentro del periodo.
	DownMs int64 `json:"down_ms"`
	// Status es el de ahora: up, down o pending.
	Status string `json:"status"`
}

// DigestUptime son los monitores de disponibilidad con incidentes que tocan
// [start, end), los que más tiempo estuvieron caídos primero.
func (s *Store) DigestUptime(ctx context.Context, projectID int64, start, end time.Time) ([]DigestUptime, error) {
	d := s.pool.Dialect()
	rows, err := s.pool.Query(ctx, `
		SELECT m.slug, m.name, count(*),
			CAST(sum(`+d.SecondsBetween(d.Least("COALESCE(i.ended_at, $3)", "$3"), d.Greatest("i.started_at", "$2"))+` * 1000) AS bigint),
			m.status
		FROM uptime_incidents i JOIN uptime_monitors m ON m.id = i.monitor_id
		WHERE m.project_id = $1 AND i.started_at < $3 AND (i.ended_at IS NULL OR i.ended_at > $2)
		GROUP BY m.id
		ORDER BY 4 DESC, `+d.Collate("m.slug"), projectID, start, end)
	if err != nil {
		return nil, fmt.Errorf("disponibilidad del resumen: %w", err)
	}
	out := []DigestUptime{}
	var u DigestUptime
	err = db.ForEachRow(rows, []any{&u.Slug, &u.Name, &u.Incidents, &u.DownMs, &u.Status}, func() error {
		out = append(out, u)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("disponibilidad del resumen: %w", err)
	}
	return out, nil
}

// LatestRelease es la última release del proyecto que se vio antes de end, con su salud.
func (s *Store) LatestRelease(ctx context.Context, projectID int64, end time.Time) (*Release, error) {
	var version string
	err := s.pool.QueryRow(ctx, `SELECT version FROM releases WHERE project_id = $1 AND first_seen < $2
		ORDER BY first_seen DESC, id DESC LIMIT 1`, projectID, end).Scan(&version)
	if errors.Is(err, db.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("última release: %w", err)
	}
	r, err := s.Release(ctx, projectID, version, "")
	if err != nil {
		return nil, err
	}
	return &r, nil
}

// DigestSent apunta cuándo salió el último resumen del proyecto.
func (s *Store) DigestSent(ctx context.Context, q Querier, projectID int64, at time.Time) error {
	_, err := q.Exec(ctx, `UPDATE project_digests SET last_sent_at = $2 WHERE project_id = $1`, projectID, at)
	return err
}
