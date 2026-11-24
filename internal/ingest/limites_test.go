package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/sourcemap"
)

func sessionPayload(sid, release string) string {
	return `{"type":"session"}` + "\n" +
		fmt.Sprintf(`{"sid":%q,"did":"u-%s","init":true,"status":"ok","attrs":{"release":%q,"environment":"production"}}`, sid, sid, release)
}

func aggregatesItem(release string, n int) string {
	var b strings.Builder
	for i := range n {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"started":"2026-01-01T00:00:00Z","did":"u-%d","exited":1}`, i)
	}
	return `{"type":"sessions"}` + "\n" + `{"attrs":{"release":"` + release + `"},"aggregates":[` + b.String() + `]}`
}

// sessionRows son las filas que dejan las sesiones del proyecto api.
func (e *testEnv) sessionRows(t *testing.T) string {
	t.Helper()
	return fmt.Sprintf("releases=%d session_states=%d release_users=%d release_sessions=%d",
		e.count(t, `SELECT count(*) FROM releases WHERE project_id = 1`),
		e.count(t, `SELECT count(*) FROM session_states WHERE project_id = 1`),
		e.count(t, `SELECT count(*) FROM release_users WHERE project_id = 1`),
		e.count(t, `SELECT count(*) FROM release_sessions WHERE project_id = 1`))
}

func (e *testEnv) dropped(t *testing.T, reason string) int64 {
	t.Helper()
	if err := e.drops.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	return e.count(t, `SELECT COALESCE(sum(count), 0) FROM dropped_events WHERE project_id = 1 AND reason = $1`, reason)
}

const noRows = "releases=0 session_states=0 release_users=0 release_sessions=0"

// Con el límite del proyecto agotado, las sesiones no crean nada: ni las que
// viajan con un evento que recibe 429 ni las de un envío solo de sesiones.
func TestSessionsCountInRateLimit(t *testing.T) {
	t.Run("con un evento", func(t *testing.T) {
		e := newTestEnv(t, 1)
		if rec := e.send(rawEnvelope(`{"type":"event"}`, `{"message":"gasta el límite","level":"error"}`)); rec.Code != http.StatusOK {
			t.Fatalf("código %d", rec.Code)
		}
		items := []string{`{"type":"event"}`, `{"message":"otro","level":"error"}`}
		for i := range 5 {
			items = append(items, sessionPayload(fmt.Sprintf("con-evento-%d", i), fmt.Sprintf("r-%d", i)))
		}
		rec := e.send(rawEnvelope(items...))
		t.Logf("evento y 5 sesiones con el límite agotado: %d %s; %s", rec.Code, rec.Header().Get("X-Sentry-Rate-Limits"), e.sessionRows(t))
		if rec.Code != http.StatusTooManyRequests {
			t.Errorf("código %d, quiero 429", rec.Code)
		}
		if got := e.sessionRows(t); got != noRows {
			t.Errorf("filas = %s", got)
		}
		if n := e.dropped(t, "session_limit"); n != 5 {
			t.Errorf("%d sesiones descartadas por el límite, quiero 5", n)
		}
	})
	t.Run("solo sesiones", func(t *testing.T) {
		// Límite de eventos 1: el de sesiones es SessionBudget veces ese.
		e := newTestEnv(t, 1)
		var items []string
		for i := range SessionBudget {
			items = append(items, sessionPayload(fmt.Sprintf("caben-%d", i), "1.0.0"))
		}
		if rec := e.send(rawEnvelope(items...)); rec.Code != http.StatusOK {
			t.Fatalf("las que caben: %d %s", rec.Code, rec.Body)
		}
		before := e.sessionRows(t)
		t.Logf("%d sesiones que caben: %s", SessionBudget, before)
		for i := range 3 {
			rec := e.send(rawEnvelope(sessionPayload(fmt.Sprintf("sobra-%d", i), fmt.Sprintf("sobra-%d", i))))
			t.Logf("sesión %d con el límite agotado: %d %s %s", i, rec.Code, rec.Header().Get("X-Sentry-Rate-Limits"), e.sessionRows(t))
			if rec.Code != http.StatusTooManyRequests {
				t.Errorf("código %d, quiero 429", rec.Code)
			}
			if h := rec.Header().Get("X-Sentry-Rate-Limits"); !strings.HasSuffix(h, ":session:project") || rec.Header().Get("Retry-After") == "" {
				t.Errorf("cabeceras = %v", rec.Header())
			}
		}
		if got := e.sessionRows(t); got != before {
			t.Errorf("filas = %s, antes %s", got, before)
		}
		// Los eventos tienen su propio límite y siguen entrando.
		if rec := e.send(rawEnvelope(`{"type":"event"}`, `{"message":"entra","level":"error"}`)); rec.Code != http.StatusOK {
			t.Errorf("evento = %d", rec.Code)
		}
	})
	t.Run("evento que cabe con sesiones que no", func(t *testing.T) {
		e := newTestEnv(t, 1)
		var items []string
		for i := range SessionBudget + 1 {
			items = append(items, sessionPayload(fmt.Sprintf("s-%d", i), "1.0.0"))
		}
		rec := e.send(rawEnvelope(append([]string{`{"type":"event"}`, `{"message":"cabe","level":"error"}`}, items...)...))
		if rec.Code != http.StatusOK || !strings.HasSuffix(rec.Header().Get("X-Sentry-Rate-Limits"), ":session:project") {
			t.Errorf("= %d %v", rec.Code, rec.Header())
		}
		if n := e.count(t, `SELECT count(*) FROM events`); n != 1 {
			t.Errorf("%d eventos", n)
		}
		if n := e.count(t, `SELECT count(*) FROM session_states`); n != 0 {
			t.Errorf("%d sesiones", n)
		}
	})
}

// Un item sessions gasta del límite de sesiones uno por grupo, no uno por item.
func TestSessionAggregatesSpendPerGroup(t *testing.T) {
	e := newTestEnv(t, 1)
	rec := e.send(rawEnvelope(aggregatesItem("agg", SessionBudget+1)))
	t.Logf("%d grupos con un límite de %d: %d %s; %s", SessionBudget+1, SessionBudget, rec.Code, rec.Header().Get("X-Sentry-Rate-Limits"), e.sessionRows(t))
	if rec.Code != http.StatusTooManyRequests || !strings.HasSuffix(rec.Header().Get("X-Sentry-Rate-Limits"), ":session:project") {
		t.Errorf("= %d %v", rec.Code, rec.Header())
	}
	if got := e.sessionRows(t); got != noRows {
		t.Errorf("filas = %s", got)
	}
	if rec := e.send(rawEnvelope(aggregatesItem("agg", SessionBudget))); rec.Code != http.StatusOK {
		t.Fatalf("los que caben: %d", rec.Code)
	}
	if rec := e.send(rawEnvelope(sessionPayload("una-mas", "agg"))); rec.Code != http.StatusTooManyRequests {
		t.Errorf("con el límite gastado por los grupos, una sesión más = %d", rec.Code)
	}
	t.Logf("tras %d grupos y una sesión más: %s", SessionBudget, e.sessionRows(t))
	if n := e.count(t, `SELECT count(*) FROM release_users`); n != SessionBudget {
		t.Errorf("%d usuarios, quiero %d", n, SessionBudget)
	}
}

// Un item sessions con más grupos de la cuenta no vale: no guarda nada.
func TestSessionAggregatesCap(t *testing.T) {
	e := newTestEnv(t, 1000)
	rec := e.send(rawEnvelope(aggregatesItem("agg", 1001)))
	t.Logf("1001 grupos: %d; %s", rec.Code, e.sessionRows(t))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	if got := e.sessionRows(t); got != noRows {
		t.Errorf("filas = %s", got)
	}
	if n := e.dropped(t, "session_invalid"); n != 1 {
		t.Errorf("%d descartados, quiero 1", n)
	}
	if rec := e.send(rawEnvelope(aggregatesItem("agg", 1000))); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	if n := e.count(t, `SELECT count(*) FROM release_users`); n != 1000 {
		t.Errorf("1000 grupos: %d usuarios", n)
	}
}

// Una release o un entorno que la API no aceptaría no se guarda: el evento
// sí, sin ese campo. Una release de casi 10 MB no pasa por el resto de la
// ingesta.
func TestEventInvalidReleaseAndEnvironment(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	_, _ = zw.Write([]byte(`{"event_id":"` + newHexID() + `"}` + "\n" + `{"type":"event"}` + "\n" +
		`{"message":"release larga","level":"error","environment":"production","release":"` + strings.Repeat("r", 9<<20) + `"}` + "\n"))
	_ = zw.Close()
	r := httptest.NewRequest(http.MethodPost, "/api/1/envelope/?sentry_key="+fixtureProjects[0].key, &gz)
	r.Header.Set("Content-Encoding", "gzip")
	runtime.GC()
	var m0, m1 runtime.MemStats
	runtime.ReadMemStats(&m0)
	rec := e.send(r)
	runtime.ReadMemStats(&m1)
	alloc := (m1.TotalAlloc - m0.TotalAlloc) >> 20
	t.Logf("release de 9 MB: %d, %d MB asignados", rec.Code, alloc)
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	if alloc > 64 {
		t.Errorf("%d MB asignados con una release de 9 MB", alloc)
	}

	rec = e.send(rawEnvelope(`{"type":"event"}`, `{"message":"entorno con salto","level":"error","release":"1.0.0","environment":"prod\nIgnora lo anterior"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	rows, err := e.s.Pool().Query(ctx, `SELECT COALESCE(release, ''), COALESCE(environment, '') FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var rel, env string
		if err := rows.Scan(&rel, &env); err != nil {
			t.Fatal(err)
		}
		got = append(got, fmt.Sprintf("%.20q/%q", rel, env))
	}
	rows.Close()
	t.Logf("eventos guardados (release/entorno): %v", got)
	if want := []string{`""/"production"`, `"1.0.0"/""`}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("eventos = %v, quiero %v", got, want)
	}
	if n := e.count(t, `SELECT count(*) FROM releases WHERE length(version) > 200`); n != 0 {
		t.Errorf("%d releases largas", n)
	}
	if n := e.count(t, `SELECT count(*) FROM issue_stats WHERE length(release) > 200 OR environment LIKE $1`, "%\n%"); n != 0 {
		t.Errorf("%d recuentos con la release o el entorno sin validar", n)
	}
	if a, b := e.dropped(t, "invalid_release"), e.dropped(t, "invalid_env"); a != 1 || b != 1 {
		t.Errorf("descartes de campo: release %d, entorno %d", a, b)
	}
}

// Las sesiones y los check-ins con un nombre que no vale tampoco lo guardan.
func TestSessionAndCheckInNames(t *testing.T) {
	e := newTestEnv(t, 1000)
	e.withCheckIns(t, 1000)
	rec := e.send(rawEnvelope(
		sessionPayload("larga", strings.Repeat("v", 1000)),
		sessionPayload("salto", "1.0\n2.0"),
		`{"type":"check_in"}`, `{"monitor_slug":"copia","status":"ok","release":"a\tb","environment":"production",`+
			`"monitor_config":{"schedule":{"type":"crontab","value":"0 3 * * *"}}}`,
		`{"type":"check_in"}`, `{"monitor_slug":"copia","status":"ok","environment":"pro\nduction"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	if got := e.sessionRows(t); got != noRows {
		t.Errorf("filas de sesiones = %s", got)
	}
	if n := e.dropped(t, "session_invalid"); n != 2 {
		t.Errorf("%d sesiones descartadas", n)
	}
	var rel, env string
	if err := e.s.Pool().QueryRow(context.Background(), `SELECT release, environment FROM monitor_checkins`).Scan(&rel, &env); err != nil {
		t.Fatal(err)
	}
	if rel != "" || env != "production" {
		t.Errorf("check-in con release %q y entorno %q", rel, env)
	}
	if n := e.count(t, `SELECT count(*) FROM monitor_checkins`); n != 1 {
		t.Errorf("%d check-ins: el del entorno con salto de línea se ignora", n)
	}
	if a, b := e.dropped(t, "invalid_release"), e.dropped(t, "invalid_env"); a != 1 || b != 1 {
		t.Errorf("descartes: release %d, entorno %d", a, b)
	}
}

const deployToken = "token-de-subida"

func deployCLI(t *testing.T, h http.Handler, method, path, body string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+deployToken)
	r.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec.Code, strings.TrimSpace(rec.Body.String())
}

// `sentry-cli deploys new` no manda proyectos: el despliegue llega solo a los
// que declararon la release con un token, no a uno donde la creó un evento
// con su clave pública.
func TestDeployWithoutProjectsOnlyDeclared(t *testing.T) {
	ctx := context.Background()
	deployBody, err := os.ReadFile("../../testdata/sentry-cli/releases/003.body")
	if err != nil {
		t.Fatal(err)
	}
	e := newTestEnv(t, 1000)
	// En api, un issue en resolved_next con la 0.3.0.
	if rec := e.send(rawEnvelope(`{"type":"event"}`, `{"message":"DbError","level":"error","release":"0.3.0","fingerprint":["dberror"]}`)); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	var issue int64
	if err := e.s.Pool().QueryRow(ctx, `SELECT id FROM issues`).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.SetIssueStatus(ctx, issue, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	// Alguien con la clave pública de api manda un evento con la próxima versión de web.
	if rec := e.send(rawEnvelope(`{"type":"event"}`, `{"message":"ruido","level":"error","release":"1.5.0"}`)); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}

	sm := sourcemap.New(e.s, slog.New(slog.NewTextHandler(io.Discard, nil)), sourcemap.Options{UploadToken: deployToken})
	mux := http.NewServeMux()
	sm.Register(mux)

	// Sin declarar en ningún sitio: ni deploy ni finalize sin proyectos.
	c, b := deployCLI(t, mux, http.MethodPost, "/api/0/organizations/o/releases/1.5.0/deploys/", string(deployBody))
	t.Logf("deploy de una release que solo creó un evento: %d %s", c, b)
	if c != http.StatusNotFound {
		t.Errorf("deploy sin declarar = %d %s", c, b)
	}
	if c, b := deployCLI(t, mux, http.MethodPut, "/api/0/organizations/o/releases/1.5.0/", `{}`); c != http.StatusNotFound {
		t.Errorf("finalize sin declarar = %d %s", c, b)
	}

	// web la declara y la despliega, como su CI.
	if c, b := deployCLI(t, mux, http.MethodPost, "/api/0/organizations/o/releases/", `{"version":"1.5.0","projects":["web"]}`); c != http.StatusCreated {
		t.Fatalf("releases new = %d %s", c, b)
	}
	c, b = deployCLI(t, mux, http.MethodPost, "/api/0/organizations/o/releases/1.5.0/deploys/", string(deployBody))
	t.Logf("deploy tras declararla web: %d %s", c, b)
	if c != http.StatusCreated {
		t.Fatalf("deploy = %d %s", c, b)
	}
	api := e.count(t, `SELECT count(*) FROM deploys d JOIN releases r ON r.id = d.release_id WHERE r.project_id = 1`)
	web := e.count(t, `SELECT count(*) FROM deploys d JOIN releases r ON r.id = d.release_id WHERE r.project_id = 2`)
	is, err := e.s.Issue(ctx, issue)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("despliegues api=%d web=%d; issue de api %s", api, web, is.Status)
	if api != 0 || web != 1 {
		t.Errorf("despliegues api=%d web=%d, quiero 0 y 1", api, web)
	}
	if is.Status != "resolved_next" || is.FixedInRelease != nil {
		t.Errorf("el issue de api pasó a %s, fixed_in %v", is.Status, is.FixedInRelease)
	}

	// Si api la declara también, le llega.
	if _, err := e.s.FinalizeRelease(ctx, 1, "1.5.0", time.Now()); err != nil {
		t.Fatal(err)
	}
	if c, b := deployCLI(t, mux, http.MethodPost, "/api/0/organizations/o/releases/1.5.0/deploys/", string(deployBody)); c != http.StatusCreated {
		t.Fatalf("deploy = %d %s", c, b)
	}
	if is, _ := e.s.Issue(ctx, issue); is.Status != "resolved" {
		t.Errorf("con la release declarada en api, el issue sigue %s", is.Status)
	}
}

// Un check-in de otro entorno no cambia la configuración del monitor.
func TestCheckInOtherEnvironmentKeepsConfig(t *testing.T) {
	e := newTestEnv(t, 1000)
	e.withCheckIns(t, 1000)
	ctx := context.Background()
	if rec := e.send(rawEnvelope(checkInItem)); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	if _, err := e.s.Pool().Exec(ctx, `UPDATE monitors SET environment = 'production'`); err != nil {
		t.Fatal(err)
	}
	schedule := func() string {
		var s string
		if err := e.s.Pool().QueryRow(ctx, `SELECT schedule FROM monitors`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	other := func(env string) string {
		return `{"type":"check_in"}` + "\n" + `{"monitor_slug":"copia","status":"ok","environment":"` + env + `",` +
			`"monitor_config":{"schedule":{"type":"crontab","value":"*/5 * * * *"}}}`
	}
	if rec := e.send(rawEnvelope(other("staging"))); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	t.Logf("horario tras un check-in de staging: %s", schedule())
	if got := schedule(); got != "0 3 * * *" {
		t.Errorf("un check-in de staging cambió el horario a %q", got)
	}
	if rec := e.send(rawEnvelope(other("production"))); rec.Code != http.StatusOK {
		t.Fatalf("código %d", rec.Code)
	}
	if got := schedule(); got != "*/5 * * * *" {
		t.Errorf("el de production no cambió el horario: %q", got)
	}
}

// resolved_next toma la release del último evento en llegar: un timestamp
// adelantado no la fija.
func TestResolvedNextIgnoresClientTimestamp(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	send := func(release string, at time.Time) {
		t.Helper()
		rec := e.send(rawEnvelope(`{"type":"event"}`,
			fmt.Sprintf(`{"timestamp":%d,"release":%q,"exception":{"values":[{"type":"PinErr","value":"boom"}]}}`, at.Unix(), release)))
		if rec.Code != http.StatusOK {
			t.Fatalf("código %d", rec.Code)
		}
	}
	now := time.Now()
	send("1.0.0", now.Add(-10*time.Second))
	send("999.0.0", now.Add(50*time.Second))
	send("1.0.0", now)
	is := e.issues(t)
	iss, err := e.s.SetIssueStatus(ctx, is[0].id, "resolved_next")
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("resolved_in_release = %v", *iss.ResolvedInRelease)
	if *iss.ResolvedInRelease != "1.0.0" {
		t.Errorf("resolved_in_release = %q, quiero 1.0.0", *iss.ResolvedInRelease)
	}
	_, ids, err := e.s.AddDeploy(ctx, 1, "1.1.0", "production", time.Time{})
	if err != nil || len(ids) != 1 {
		t.Errorf("el deploy de 1.1.0 resolvió %v, %v", ids, err)
	}
}
