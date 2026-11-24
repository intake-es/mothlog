package ingest

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

type sessionRow struct {
	total, errored, crashed, abnormal int64
}

func (e *testEnv) sessionCounts(t *testing.T, projectID int64, release, env string) sessionRow {
	t.Helper()
	var r sessionRow
	if err := e.s.Pool().QueryRow(context.Background(), `
		SELECT COALESCE(sum(total), 0), COALESCE(sum(errored), 0), COALESCE(sum(crashed), 0), COALESCE(sum(abnormal), 0)
		FROM release_sessions WHERE project_id = $1 AND release = $2 AND environment = $3`,
		projectID, release, env).Scan(&r.total, &r.errored, &r.crashed, &r.abnormal); err != nil {
		t.Fatal(err)
	}
	return r
}

// sendSession manda un item session al proyecto web, como el SDK del navegador.
func (e *testEnv) sendSession(t *testing.T, payload string) {
	t.Helper()
	r := httptest.NewRequest("POST", "/api/2/envelope/?sentry_key=ce01335f125960aa6957a133d15b5f3a",
		strings.NewReader("{}\n{\"type\":\"session\"}\n"+payload+"\n"))
	if rec := e.send(r); rec.Code != 200 {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
}

func session(sid string, init bool, status string, errors int, at time.Time, extra string) string {
	return fmt.Sprintf(`{"sid":%q,"init":%v,"started":%q,"timestamp":%q,"status":%q,"errors":%d%s,"attrs":{"release":"2.0.0","environment":"staging"}}`,
		sid, init, at.Add(-time.Minute).Format(time.RFC3339Nano), at.Format(time.RFC3339Nano), status, errors, extra)
}

func TestBrowserSessionFixtures(t *testing.T) {
	for _, order := range [][]string{
		{"web-sesion-1", "web-sesion-2"},
		{"web-sesion-1", "web-sesion-2", "web-sesion-1", "web-sesion-2"},
		{"web-sesion-2", "web-sesion-1"},
	} {
		t.Run(strings.Join(order, ","), func(t *testing.T) {
			e := newTestEnv(t, 1000)
			for _, name := range order {
				f := loadFixture(t, name)
				e.sendOK(t, f, f.body)
			}
			got := e.sessionCounts(t, 2, "fixture-release-1", "production")
			if got != (sessionRow{total: 1, errored: 1, crashed: 1}) {
				t.Errorf("sesiones = %+v", got)
			}
			if n := e.count(t, `SELECT count(*) FROM releases WHERE project_id = 2 AND version = 'fixture-release-1'`); n != 1 {
				t.Errorf("%d releases", n)
			}
			// El navegador no manda did: no hay usuarios.
			if n := e.count(t, `SELECT count(*) FROM release_users`); n != 0 {
				t.Errorf("%d usuarios", n)
			}
		})
	}
}

func TestPythonSessionFixtures(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range []string{"api-sesiones-1", "api-sesion-ok-1", "api-sesion-crash-1", "api-sesion-crash-2"} {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	want := sessionRow{total: 6, errored: 2, crashed: 2}
	if got := e.sessionCounts(t, 1, "fixture-release-1", "production"); got != want {
		t.Errorf("sesiones = %+v, quiero %+v", got, want)
	}
	// La sesión individual reenviada no cuenta otra vez.
	f := loadFixture(t, "api-sesion-crash-2")
	e.sendOK(t, f, f.body)
	if got := e.sessionCounts(t, 1, "fixture-release-1", "production"); got != want {
		t.Errorf("tras el reenvío, sesiones = %+v, quiero %+v", got, want)
	}

	ctx := context.Background()
	key, err := store.LoadUserHashKey(ctx, e.s.Pool())
	if err != nil {
		t.Fatal(err)
	}
	for user, crashed := range map[string]bool{"u_ana": false, "u_luis": true} {
		var got bool
		if err := e.s.Pool().QueryRow(ctx, `SELECT crashed FROM release_users WHERE project_id = 1 AND release = 'fixture-release-1'
			AND environment = 'production' AND user_hash = $1`, store.UserHash(key, "id:"+user)).Scan(&got); err != nil {
			t.Fatalf("%s: %v", user, err)
		}
		if got != crashed {
			t.Errorf("%s: crashed = %v", user, got)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM release_users`); n != 2 {
		t.Errorf("%d usuarios", n)
	}
}

func TestSessionUpdatesCountOnceWithFinalStatus(t *testing.T) {
	e := newTestEnv(t, 1000)
	t0 := time.Now().UTC().Add(-time.Hour)
	sid := "0f5e9d1c2b3a49788f6e5d4c3b2a1908"
	steps := []struct {
		payload string
		want    sessionRow
	}{
		{session(sid, true, "ok", 0, t0, `,"did":"u_1"`), sessionRow{total: 1}},
		{session(sid, false, "ok", 1, t0.Add(time.Minute), ""), sessionRow{total: 1, errored: 1}},
		// Reenvío de la misma actualización.
		{session(sid, false, "ok", 1, t0.Add(time.Minute), ""), sessionRow{total: 1, errored: 1}},
		{session(sid, false, "crashed", 2, t0.Add(2*time.Minute), ""), sessionRow{total: 1, errored: 1, crashed: 1}},
		// Una actualización vieja o una tras el final no cambian nada.
		{session(sid, false, "ok", 0, t0, ""), sessionRow{total: 1, errored: 1, crashed: 1}},
		{session(sid, false, "exited", 2, t0.Add(3*time.Minute), ""), sessionRow{total: 1, errored: 1, crashed: 1}},
		// Ni el init repetido.
		{session(sid, true, "ok", 0, t0, `,"did":"u_1"`), sessionRow{total: 1, errored: 1, crashed: 1}},
	}
	for i, s := range steps {
		e.sendSession(t, s.payload)
		if got := e.sessionCounts(t, 2, "2.0.0", "staging"); got != s.want {
			t.Fatalf("paso %d: sesiones = %+v, quiero %+v", i, got, s.want)
		}
	}
	if n := e.count(t, `SELECT count(*) FROM release_users WHERE crashed`); n != 1 {
		t.Errorf("%d usuarios con crash", n)
	}

	// Sin el init no cuenta; cuando llega, cuenta con el último estado.
	other := "11111111222233334444555566667777"
	e.sendSession(t, session(other, false, "abnormal", 0, t0.Add(5*time.Minute), ""))
	if got := e.sessionCounts(t, 2, "2.0.0", "staging"); got.total != 1 {
		t.Fatalf("sin init, sesiones = %+v", got)
	}
	e.sendSession(t, session(other, true, "ok", 0, t0.Add(4*time.Minute), ""))
	want := sessionRow{total: 2, errored: 2, crashed: 1, abnormal: 1}
	if got := e.sessionCounts(t, 2, "2.0.0", "staging"); got != want {
		t.Fatalf("con el init tarde, sesiones = %+v, quiero %+v", got, want)
	}
}

func TestSessionsFollowProjectFilters(t *testing.T) {
	e := newTestEnv(t, 1000)
	if _, err := e.s.Pool().Exec(context.Background(),
		`INSERT INTO project_filters (project_id, environments) VALUES (2, $1)`, []string{"staging"}); err != nil {
		t.Fatal(err)
	}
	e.sendSession(t, session("aaaa", true, "exited", 0, time.Now(), ""))
	if n := e.count(t, `SELECT count(*) FROM release_sessions`); n != 0 {
		t.Errorf("%d filas de sesiones de un entorno filtrado", n)
	}
}

func TestMalformedSessionDoesNotRejectEnvelope(t *testing.T) {
	e := newTestEnv(t, 1000)
	e.sendSession(t, `{"init":true,"attrs":{}}`)
	if n := e.count(t, `SELECT count(*) FROM release_sessions`); n != 0 {
		t.Errorf("%d filas", n)
	}
}

func TestPurgeSessions(t *testing.T) {
	e := newTestEnv(t, 1000)
	now := time.Now().UTC()
	e.sendSession(t, session("bbbb", true, "ok", 0, now.AddDate(0, 0, -100), ""))
	e.sendSession(t, session("cccc", true, "ok", 0, now, `,"did":"u_2"`))
	ctx := context.Background()
	if _, err := e.s.Pool().Exec(ctx, `UPDATE session_states SET updated_at = $1 WHERE release = '2.0.0'
		AND hour < $2`, now.Add(-25*time.Hour), now.AddDate(0, 0, -1)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool().Exec(ctx, `INSERT INTO release_users (project_id, release, environment, user_hash, last_seen)
		VALUES (2, '2.0.0', 'staging', $2, $1)`, now.AddDate(0, 0, -100), []byte{0}); err != nil {
		t.Fatal(err)
	}
	n, err := PurgeSessions(ctx, e.s.Pool(), now.AddDate(0, 0, -90), now)
	if err != nil || n != 3 {
		t.Fatalf("borradas %d filas, %v", n, err)
	}
	if got := e.sessionCounts(t, 2, "2.0.0", "staging"); got.total != 1 {
		t.Errorf("quedan %+v", got)
	}
	if n := e.count(t, `SELECT (SELECT count(*) FROM session_states) + (SELECT count(*) FROM release_users)`); n != 2 {
		t.Errorf("quedan %d filas de estado y usuarios", n)
	}
}
