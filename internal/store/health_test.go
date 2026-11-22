package store

import (
	"context"
	"math"
	"testing"
	"time"
)

func near(got *float64, want float64) bool {
	return got != nil && math.Abs(*got-want) < 1e-9
}

func TestReleaseHealth(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "web", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	hour := now.Truncate(time.Hour)
	for _, v := range []string{"1.0", "2.0", "3.0"} {
		if _, _, err := s.CreateRelease(ctx, p.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	sessions := func(release, env string, at time.Time, total, errored, crashed int64) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO release_sessions (project_id, release, environment, hour, total, errored, crashed)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, p.ID, release, env, at, total, errored, crashed); err != nil {
			t.Fatal(err)
		}
	}
	user := func(release, env, id string, crashed bool) {
		if _, err := s.pool.Exec(ctx, `INSERT INTO release_users (project_id, release, environment, user_hash, crashed, last_seen)
			VALUES ($1, $2, $3, $4, $5, now())`, p.ID, release, env, []byte(id), crashed); err != nil {
			t.Fatal(err)
		}
	}
	// 1.0: en producción 8 sesiones (2 con crash) hace dos horas y en staging
	// 2, fuera de las últimas 24 horas. u1 tuvo un crash en producción y no en staging.
	sessions("1.0", "production", hour.Add(-2*time.Hour), 5, 2, 1)
	sessions("1.0", "production", hour.Add(-3*time.Hour), 3, 1, 1)
	sessions("1.0", "staging", hour.Add(-48*time.Hour), 2, 0, 0)
	user("1.0", "production", "u1", true)
	user("1.0", "production", "u2", false)
	user("1.0", "production", "u3", false)
	user("1.0", "staging", "u1", false)
	user("1.0", "staging", "u4", false)
	// 2.0: 10 sesiones sin crash y sin did, en la hora en curso.
	sessions("2.0", "production", hour, 10, 0, 0)

	page, err := s.ListReleases(ctx, ReleaseFilter{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Release{}
	for _, r := range page.Releases {
		got[r.Version] = r
	}

	h := got["1.0"].Health
	if h.Environment != "" || h.Sessions != 10 || h.SessionsCrashed != 2 || h.SessionsErrored != 3 || h.Users != 4 || h.UsersCrashed != 1 || h.Sessions24h != 8 {
		t.Errorf("1.0 en total = %+v", h)
	}
	if !near(h.CrashFreeSessions, 0.8) || !near(h.CrashFreeUsers, 0.75) || !near(h.Adoption, 8.0/18) {
		t.Errorf("1.0 en total: %v %v %v", *h.CrashFreeSessions, *h.CrashFreeUsers, *h.Adoption)
	}
	envs := got["1.0"].HealthByEnvironment
	if len(envs) != 2 || envs[0].Environment != "production" || envs[1].Environment != "staging" {
		t.Fatalf("1.0 por entorno = %+v", envs)
	}
	prod, stg := envs[0], envs[1]
	if prod.Sessions != 8 || prod.Users != 3 || !near(prod.CrashFreeSessions, 0.75) || !near(prod.CrashFreeUsers, 2.0/3) || !near(prod.Adoption, 8.0/18) {
		t.Errorf("1.0 en producción = %+v", prod)
	}
	// En staging no hubo sesiones en 24 horas: no hay adopción que medir.
	if stg.Sessions != 2 || !near(stg.CrashFreeSessions, 1) || !near(stg.CrashFreeUsers, 1) || stg.Adoption != nil {
		t.Errorf("1.0 en staging = %+v", stg)
	}

	h = got["2.0"].Health
	if !near(h.CrashFreeSessions, 1) || h.CrashFreeUsers != nil || !near(h.Adoption, 10.0/18) {
		t.Errorf("2.0 = %+v", h)
	}

	// Sin sesiones no hay porcentaje, y la adopción es cero.
	h = got["3.0"].Health
	if h.Sessions != 0 || h.CrashFreeSessions != nil || h.CrashFreeUsers != nil || !near(h.Adoption, 0) || len(got["3.0"].HealthByEnvironment) != 0 {
		t.Errorf("3.0 = %+v", got["3.0"])
	}

	// Con un entorno, solo lo suyo; 3.0 no sale porque no tiene nada en él.
	page, err = s.ListReleases(ctx, ReleaseFilter{Environment: "staging"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Releases) != 1 || page.Releases[0].Version != "1.0" {
		t.Fatalf("releases en staging = %+v", page.Releases)
	}
	h = page.Releases[0].Health
	if h.Environment != "staging" || h.Sessions != 2 || h.Users != 2 || !near(h.CrashFreeSessions, 1) || h.Adoption != nil {
		t.Errorf("1.0 en staging = %+v", h)
	}

	d, err := s.ReleaseDetail(ctx, p.ID, "1.0", "production")
	if err != nil {
		t.Fatal(err)
	}
	if !near(d.Health.CrashFreeSessions, 0.75) || len(d.HealthSeries) != HealthSeriesHours {
		t.Fatalf("detalle = %+v, %d horas", d.Health, len(d.HealthSeries))
	}
	last := d.HealthSeries[len(d.HealthSeries)-1]
	if !last.Hour.Equal(hour) || last.Sessions != 0 || last.CrashFreeSessions != nil {
		t.Errorf("última hora = %+v", last)
	}
	p2 := d.HealthSeries[len(d.HealthSeries)-3]
	if !p2.Hour.Equal(hour.Add(-2*time.Hour)) || p2.Sessions != 5 || p2.SessionsCrashed != 1 || !near(p2.CrashFreeSessions, 0.8) {
		t.Errorf("hace dos horas = %+v", p2)
	}

	// La lista global de entornos sale de los eventos: los que solo tienen
	// sesiones no aparecen en el selector ni filtran la lista de errores.
	envList, err := s.Environments(ctx, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(envList) != 0 {
		t.Errorf("entornos = %+v; sin eventos no debería haber ninguno", envList)
	}
}
