package store

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

// HealthSeriesHours es cuántas horas trae la serie del detalle de una release.
const HealthSeriesHours = 72

// ReleaseHealth son las sesiones de una release en un entorno, o en todos si
// Environment está vacío. Los porcentajes, de 0 a 1, son nil sin sesiones o
// sin usuarios: no hay nada que medir.
type ReleaseHealth struct {
	Environment       string   `json:"environment"`
	Sessions          int64    `json:"sessions"`
	SessionsErrored   int64    `json:"sessions_errored"`
	SessionsCrashed   int64    `json:"sessions_crashed"`
	SessionsAbnormal  int64    `json:"sessions_abnormal"`
	Users             int64    `json:"users"`
	UsersCrashed      int64    `json:"users_crashed"`
	CrashFreeSessions *float64 `json:"crash_free_sessions"`
	CrashFreeUsers    *float64 `json:"crash_free_users"`
	// Sessions24h son las sesiones de las últimas 24 horas y Adoption, la
	// parte que son de todas las del proyecto (en ese entorno) en esas horas.
	Sessions24h int64    `json:"sessions_24h"`
	Adoption    *float64 `json:"adoption"`
}

// HealthHour es una hora de la serie, por la hora de inicio de las sesiones.
type HealthHour struct {
	Hour              time.Time `json:"hour"`
	Sessions          int64     `json:"sessions"`
	SessionsErrored   int64     `json:"sessions_errored"`
	SessionsCrashed   int64     `json:"sessions_crashed"`
	SessionsAbnormal  int64     `json:"sessions_abnormal"`
	CrashFreeSessions *float64  `json:"crash_free_sessions"`
}

// crashFree es 1 − bad/total, o nil si no hay total.
func crashFree(bad, total int64) *float64 {
	if total <= 0 {
		return nil
	}
	v := max(0, 1-float64(bad)/float64(total))
	return &v
}

func share(part, total int64) *float64 {
	if total <= 0 {
		return nil
	}
	v := min(1, float64(part)/float64(total))
	return &v
}

func (h *ReleaseHealth) finish(projectSessions24h int64) {
	h.CrashFreeSessions = crashFree(h.SessionsCrashed, h.Sessions)
	h.CrashFreeUsers = crashFree(h.UsersCrashed, h.Users)
	h.Adoption = share(h.Sessions24h, projectSessions24h)
}

// since24h es el inicio de las 24 horas de la adopción: la hora en curso y las
// 23 anteriores.
func since24h(now time.Time) time.Time {
	return now.UTC().Truncate(time.Hour).Add(-23 * time.Hour)
}

// loadHealth rellena Health y HealthByEnvironment de cada release, solo con
// el entorno env si no está vacío.
func (s *Store) loadHealth(ctx context.Context, rels []Release, env string, now time.Time) error {
	ids := make([]int64, len(rels))
	at := make(map[int64]int, len(rels))
	byEnv := make([]map[string]*ReleaseHealth, len(rels))
	var projects []int64
	seenProject := map[int64]bool{}
	for i := range rels {
		ids[i] = rels[i].ID
		at[rels[i].ID] = i
		byEnv[i] = map[string]*ReleaseHealth{}
		rels[i].Health = ReleaseHealth{Environment: env}
		rels[i].HealthByEnvironment = []ReleaseHealth{}
		if !seenProject[rels[i].ProjectID] {
			seenProject[rels[i].ProjectID] = true
			projects = append(projects, rels[i].ProjectID)
		}
	}
	if len(ids) == 0 {
		return nil
	}
	since := since24h(now)
	get := func(i int, e string) *ReleaseHealth {
		h := byEnv[i][e]
		if h == nil {
			h = &ReleaseHealth{Environment: e}
			byEnv[i][e] = h
		}
		return h
	}

	rows, err := s.pool.Query(ctx, `
		SELECT r.id, s.environment, CAST(sum(s.total) AS bigint), CAST(sum(s.errored) AS bigint), CAST(sum(s.crashed) AS bigint),
			CAST(sum(s.abnormal) AS bigint), CAST(COALESCE(sum(s.total) FILTER (WHERE s.hour >= $3), 0) AS bigint)
		FROM releases r JOIN release_sessions s ON s.project_id = r.project_id AND s.release = r.version
		WHERE `+s.pool.Dialect().InList("r.id", "$1")+` AND ($2 = '' OR s.environment = $2)
		GROUP BY r.id, s.environment`, ids, env, since)
	if err != nil {
		return fmt.Errorf("read sessions: %w", err)
	}
	var rid int64
	var e string
	var c [5]int64
	err = db.ForEachRow(rows, []any{&rid, &e, &c[0], &c[1], &c[2], &c[3], &c[4]}, func() error {
		i := at[rid]
		for _, h := range []*ReleaseHealth{get(i, e), &rels[i].Health} {
			h.Sessions += c[0]
			h.SessionsErrored += c[1]
			h.SessionsCrashed += c[2]
			h.SessionsAbnormal += c[3]
			h.Sessions24h += c[4]
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("read sessions: %w", err)
	}

	// Por entorno con su propia fila y, en total, cada usuario una vez.
	d := s.pool.Dialect()
	users := `
		FROM releases r JOIN release_users u ON u.project_id = r.project_id AND u.release = r.version
		WHERE ` + d.InList("r.id", "$1") + ` AND ($2 = '' OR u.environment = $2)`
	counts := `count(DISTINCT u.user_hash), count(DISTINCT u.user_hash) FILTER (WHERE u.crashed)`
	rows, err = s.pool.Query(ctx, db.ByEngine(d, `
		SELECT r.id, COALESCE(u.environment, ''), u.environment IS NULL, `+counts+users+`
		GROUP BY GROUPING SETS ((r.id, u.environment), (r.id))`, `
		SELECT r.id, u.environment, false, `+counts+users+` GROUP BY r.id, u.environment
		UNION ALL
		SELECT r.id, '', true, `+counts+users+` GROUP BY r.id`), ids, env)
	if err != nil {
		return fmt.Errorf("read release users: %w", err)
	}
	var all bool
	err = db.ForEachRow(rows, []any{&rid, &e, &all, &c[0], &c[1]}, func() error {
		h := &rels[at[rid]].Health
		if !all {
			h = get(at[rid], e)
		}
		h.Users, h.UsersCrashed = c[0], c[1]
		return nil
	})
	if err != nil {
		return fmt.Errorf("read release users: %w", err)
	}

	// Las sesiones de cada proyecto y entorno en las últimas 24 horas.
	rows, err = s.pool.Query(ctx, `
		SELECT project_id, environment, CAST(sum(total) AS bigint) FROM release_sessions
		WHERE `+s.pool.Dialect().InList("project_id", "$1")+` AND hour >= $2 AND ($3 = '' OR environment = $3)
		GROUP BY project_id, environment`, projects, since, env)
	if err != nil {
		return fmt.Errorf("read adoption: %w", err)
	}
	type key struct {
		project int64
		env     string
	}
	recent := map[key]int64{}
	var pid, n int64
	err = db.ForEachRow(rows, []any{&pid, &e, &n}, func() error {
		recent[key{pid, e}] += n
		recent[key{pid, ""}] += n
		return nil
	})
	if err != nil {
		return fmt.Errorf("read adoption: %w", err)
	}
	for i := range rels {
		r := &rels[i]
		// Con un entorno elegido, recent solo tiene ese entorno.
		r.Health.finish(recent[key{r.ProjectID, ""}])
		envs := make([]string, 0, len(byEnv[i]))
		for e := range byEnv[i] {
			envs = append(envs, e)
		}
		sort.Strings(envs)
		for _, e := range envs {
			h := byEnv[i][e]
			h.finish(recent[key{r.ProjectID, e}])
			r.HealthByEnvironment = append(r.HealthByEnvironment, *h)
		}
	}
	return nil
}

// healthSeries son las sesiones de la release por hora de las últimas
// HealthSeriesHours horas, con las horas sin sesiones a cero.
func (s *Store) healthSeries(ctx context.Context, projectID int64, version, env string, now time.Time) ([]HealthHour, error) {
	end := now.UTC().Truncate(time.Hour)
	start := end.Add(-(HealthSeriesHours - 1) * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT hour, CAST(sum(total) AS bigint), CAST(sum(errored) AS bigint), CAST(sum(crashed) AS bigint), CAST(sum(abnormal) AS bigint)
		FROM release_sessions
		WHERE project_id = $1 AND release = $2 AND ($3 = '' OR environment = $3) AND hour >= $4
		GROUP BY hour`, projectID, version, env, start)
	if err != nil {
		return nil, fmt.Errorf("read session series: %w", err)
	}
	got := map[time.Time]HealthHour{}
	var h HealthHour
	err = db.ForEachRow(rows, []any{&h.Hour, &h.Sessions, &h.SessionsErrored, &h.SessionsCrashed, &h.SessionsAbnormal}, func() error {
		got[h.Hour.UTC()] = h
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read session series: %w", err)
	}
	out := make([]HealthHour, 0, HealthSeriesHours)
	for t := start; !t.After(end); t = t.Add(time.Hour) {
		p, ok := got[t]
		if !ok {
			p = HealthHour{}
		}
		p.Hour = t
		p.CrashFreeSessions = crashFree(p.SessionsCrashed, p.Sessions)
		out = append(out, p)
	}
	return out, nil
}
