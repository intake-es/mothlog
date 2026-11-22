package ingest

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/monitor"
	"github.com/intake-es/intake-errors/internal/store"
)

func TestCheckInFixturesWithoutMonitorsAreIgnored(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range checkInFixtures {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	if n := e.count(t, `SELECT (SELECT count(*) FROM monitors) + (SELECT count(*) FROM issues)`); n != 0 {
		t.Errorf("%d filas guardadas", n)
	}
}

func TestCheckInFixtures(t *testing.T) {
	e := newTestEnv(t, 1000)
	svc := monitor.New(e.s, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 11, 22, 15, 41, 3, 0, time.UTC)
	svc.Now = func() time.Time { return now }
	mux := http.NewServeMux()
	mux.Handle(Pattern, NewHandler(e.s, e.ing, NewRateLimiter(1000), e.drops, slog.New(slog.NewTextHandler(io.Discard, nil))).
		WithCheckIns(svc))
	e.h = mux
	for _, name := range checkInFixtures {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
		now = now.Add(time.Second)
	}
	ctx := context.Background()
	want := []struct {
		slug, schedule, tz string
		margin, runtime    int
		status             string
		history            []string
	}{
		{"limpieza-nocturna", `{"type":"crontab","value":"0 3 * * *"}`, "Europe/Madrid", 5, 20, "ok", []string{"ok"}},
		{"enviar-facturas", `{"type":"interval","value":2,"unit":"hour"}`, "UTC", 1, 30, "error", []string{"error"}},
		// Creado por un check-in, vigila cualquier entorno: también cuenta el error de staging.
		{"copia-seguridad", `{"type":"crontab","value":"*/5 * * * *"}`, "Europe/Madrid", 2, 3, "error", []string{"error", "ok"}},
	}
	for _, w := range want {
		m, err := e.s.Monitor(ctx, 1, w.slug)
		if err != nil {
			t.Fatalf("%s: %v", w.slug, err)
		}
		sched, _ := m.Schedule.MarshalJSON()
		if string(sched) != w.schedule || m.Timezone != w.tz || m.CheckinMargin != w.margin || m.MaxRuntime != w.runtime ||
			m.Status != w.status || m.Environment != "" {
			t.Errorf("%s: %s %s margen %d máximo %d estado %s entorno %q", w.slug, sched, m.Timezone, m.CheckinMargin,
				m.MaxRuntime, m.Status, m.Environment)
		}
		page, err := e.s.CheckIns(ctx, m.ID, "", 10)
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, c := range page.CheckIns {
			got = append(got, c.Status)
			if c.DurationMs == nil || c.FinishedAt == nil {
				t.Errorf("%s: check-in sin duración o sin fin: %+v", w.slug, c)
			}
		}
		if !slices.Equal(got, w.history) {
			t.Errorf("%s: historial %v; quería %v", w.slug, got, w.history)
		}
	}
	m, _ := e.s.Monitor(ctx, 1, "limpieza-nocturna")
	// Las 3:00 de Madrid del día siguiente.
	if want := time.Date(2026, 11, 23, 2, 0, 0, 0, time.UTC); m.NextCheckinAt == nil || !m.NextCheckinAt.Equal(want) {
		t.Errorf("siguiente = %v; quería %v", m.NextCheckinAt, want)
	}
	page, _ := e.s.CheckIns(ctx, m.ID, "", 1)
	if c := page.CheckIns[0]; c.Release != "fixture-release-1" || *c.DurationMs != 201 {
		t.Errorf("check-in = %+v", c)
	}
	if n := e.count(t, `SELECT count(*) FROM monitor_checkins WHERE status = $1`, store.CheckInInProgress); n != 0 {
		t.Errorf("%d check-ins siguen in_progress", n)
	}
}

func (e *testEnv) withCheckIns(t *testing.T, perMinute int) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle(Pattern, NewHandler(e.s, e.ing, NewRateLimiter(perMinute), e.drops, log).WithCheckIns(monitor.New(e.s, nil, log)))
	e.h = mux
}

func rawEnvelope(items ...string) *http.Request {
	body := `{"event_id":"` + newHexID() + `"}` + "\n" + strings.Join(items, "\n") + "\n"
	r := httptest.NewRequest(http.MethodPost, "/api/1/envelope/", strings.NewReader(body))
	r.Header.Set("X-Sentry-Auth", "Sentry sentry_key="+fixtureProjects[0].key+", sentry_version=7")
	return r
}

const checkInItem = `{"type":"check_in"}` + "\n" +
	`{"check_in_id":"0f8e1a2b3c4d4e5f8a9b0c1d2e3f4a5b","monitor_slug":"copia","status":"ok",` +
	`"monitor_config":{"schedule":{"type":"crontab","value":"0 3 * * *"}}}`

// Un check-in mal formado se salta: el evento que viaja con él se guarda.
func TestMalformedCheckInKeepsEvents(t *testing.T) {
	e := newTestEnv(t, 1000)
	e.withCheckIns(t, 1000)
	rec := e.send(rawEnvelope(
		`{"type":"event"}`, `{"message":"fallo de prueba","level":"error","platform":"python"}`,
		`{"type":"check_in"}`, `{"status":"ok"}`,
		checkInItem))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	if n := e.count(t, `SELECT count(*) FROM events`); n != 1 {
		t.Errorf("%d eventos", n)
	}
	if n := e.count(t, `SELECT count(*) FROM monitor_checkins`); n != 1 {
		t.Errorf("%d check-ins", n)
	}
}

// Los check-ins cuentan en el límite por minuto del proyecto.
func TestCheckInsCountInRateLimit(t *testing.T) {
	e := newTestEnv(t, 2)
	e.withCheckIns(t, 2)
	for i := range 2 {
		if rec := e.send(rawEnvelope(checkInItem)); rec.Code != http.StatusOK {
			t.Fatalf("%d: código %d: %s", i, rec.Code, rec.Body)
		}
	}
	rec := e.send(rawEnvelope(checkInItem))
	if rec.Code != http.StatusTooManyRequests || !strings.HasSuffix(rec.Header().Get("X-Sentry-Rate-Limits"), ":monitor:project") {
		t.Errorf("tercero = %d %v", rec.Code, rec.Header())
	}
	if n := e.count(t, `SELECT count(*) FROM monitor_checkins`); n != 1 {
		t.Errorf("%d check-ins guardados; el mismo check_in_id se cierra una vez", n)
	}
}
