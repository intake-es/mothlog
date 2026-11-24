package sentryimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/sentryimport/fake"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

const testToken = "sntrys_eyJpYXQiOjE3MzAwMDAwMDB9_secretoDePrueba123"

type env struct {
	t     *testing.T
	db    *store.Store
	fake  *fake.Server
	srv   *httptest.Server
	ing   *ingest.Ingester
	mu    sync.Mutex
	waits []time.Duration
	out   bytes.Buffer
}

func newEnv(t *testing.T) *env {
	t.Helper()
	db := storetest.New(t)
	f, err := fake.New("../../testdata/sentry", testToken)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	return &env{t: t, db: db, fake: f, srv: srv, ing: ingest.NewIngester(db.Pool(), nil, nil, log)}
}

func (e *env) run(opts Options) *Report {
	e.t.Helper()
	c, err := NewClient(e.srv.URL, testToken, e.srv.Client())
	if err != nil {
		e.t.Fatal(err)
	}
	c.Sleep = func(_ context.Context, d time.Duration) error {
		e.mu.Lock()
		e.waits = append(e.waits, d)
		e.mu.Unlock()
		return nil
	}
	if opts.Org == "" {
		opts.Org = "ejemplo"
	}
	opts.Progress = &e.out
	rep, err := New(c, e.db, e.ing, opts).Run(context.Background())
	if err != nil {
		e.t.Fatal(err)
	}
	if err := rep.Write(&e.out); err != nil {
		e.t.Fatal(err)
	}
	return rep
}

// snapshot vuelca todo lo que escribe la importación, para comparar dos momentos.
func (e *env) snapshot() string {
	e.t.Helper()
	var b strings.Builder
	for _, q := range []string{
		`SELECT id, name, public_key FROM projects ORDER BY id`,
		`SELECT id, project_id, fingerprint, title, culprit, type, level, status, fixed_in_release, resolved_in_release,
			resolved_at, first_seen, last_seen, event_count, user_count FROM issues ORDER BY id`,
		`SELECT id, issue_id, project_id, event_id, occurred_at, received_at, release, environment, data FROM events ORDER BY id`,
		`SELECT id, project_id, version, first_seen, finalized_at FROM releases ORDER BY id`,
		`SELECT id, release_id, environment, deployed_at FROM deploys ORDER BY id`,
		`SELECT * FROM issue_stats ORDER BY issue_id, release, environment`,
		`SELECT * FROM issue_tag_values ORDER BY issue_id, key, value`,
		`SELECT issue_id, user_hash FROM issue_users ORDER BY issue_id, user_hash`,
		`SELECT id, issue_id, source, from_status, to_status, release FROM issue_status_changes ORDER BY id`,
		`SELECT * FROM import_origins ORDER BY id`,
	} {
		rows, err := e.db.Pool().Query(context.Background(), q)
		if err != nil {
			e.t.Fatalf("%s: %v", q, err)
		}
		for rows.Next() {
			vals, err := rows.Values()
			if err != nil {
				e.t.Fatalf("%s: %v", q, err)
			}
			fmt.Fprintln(&b, vals...)
		}
		if err := rows.Err(); err != nil {
			e.t.Fatalf("%s: %v", q, err)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func (e *env) count(q string, args ...any) int64 {
	e.t.Helper()
	var n int64
	if err := e.db.Pool().QueryRow(context.Background(), q, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", q, err)
	}
	return n
}

type issueRow struct {
	id                    int64
	project, title, level string
	status                string
	fixedIn, resolvedIn   *string
	count, users          int64
	firstSeen, lastSeen   time.Time
}

// issue busca el issue importado de Sentry con ese id.
func (e *env) issue(sentryID string) issueRow {
	e.t.Helper()
	var r issueRow
	err := e.db.Pool().QueryRow(context.Background(), `
		SELECT i.id, p.name, i.title, i.level, i.status, i.fixed_in_release, i.resolved_in_release,
			i.event_count, i.user_count, i.first_seen, i.last_seen
		FROM import_origins o JOIN issues i ON i.id = o.issue_id JOIN projects p ON p.id = i.project_id
		WHERE o.kind = 'issue' AND o.external_id = $1`, sentryID).Scan(&r.id, &r.project, &r.title, &r.level,
		&r.status, &r.fixedIn, &r.resolvedIn, &r.count, &r.users, &r.firstSeen, &r.lastSeen)
	if err != nil {
		e.t.Fatalf("issue %s: %v", sentryID, err)
	}
	return r
}

func wantCounts(t *testing.T, what string, got, want Counts) {
	t.Helper()
	if got != want {
		t.Errorf("%s = %+v, quiero %+v", what, got, want)
	}
}

func TestImportAllAndAgain(t *testing.T) {
	e := newEnv(t)
	// firstRelease viene en el detalle del issue; si la lista lo trae, se usa.
	if err := e.fake.Edit("ejemplo/issues-api", func(list []map[string]any) []map[string]any {
		for _, is := range list {
			if is["id"] == "7002" {
				is["firstRelease"] = map[string]any{"version": "api@0.9.0", "shortVersion": "0.9.0"}
			}
		}
		return list
	}); err != nil {
		t.Fatal(err)
	}
	rep := e.run(Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errores: %v", rep.Errors)
	}
	wantCounts(t, "proyectos", rep.Projects, Counts{Created: 3})
	wantCounts(t, "releases", rep.Releases, Counts{Created: 3})
	wantCounts(t, "despliegues", rep.Deploys, Counts{Created: 4})
	// 7006 no tiene eventos en Sentry: se salta.
	wantCounts(t, "issues", rep.Issues, Counts{Created: 6, Skipped: 1})
	// 7005 tiene 7 eventos y se traen los 5 últimos.
	wantCounts(t, "eventos", rep.Events, Counts{Created: 3 + 2 + 1 + 1 + 5 + 2})
	if len(rep.Skipped) != 1 || !strings.Contains(rep.Skipped[0], "API-6") {
		t.Errorf("saltados = %v", rep.Skipped)
	}
	if len(rep.NotGrouped) != 0 {
		t.Errorf("sin agrupar = %v", rep.NotGrouped)
	}

	zero := e.issue("7001")
	if zero.project != "api" || zero.title != "ZeroDivisionError: division by zero" || zero.status != "open" ||
		zero.count != 42 || zero.level != "error" {
		t.Errorf("7001 = %+v", zero)
	}
	if want := time.Date(2026, 10, 28, 17, 5, 53, 945e6, time.UTC); !zero.firstSeen.Equal(want) {
		t.Errorf("7001 first_seen = %v, quiero %v", zero.firstSeen, want)
	}
	if r := e.issue("7002"); r.status != "resolved" || r.fixedIn == nil || *r.fixedIn != "fixture-release-1" {
		t.Errorf("7002 resuelto en una release = %+v", r)
	}
	for id, want := range map[string]string{"7001": "fixture-release-1", "7002": "api@0.9.0", "7101": "fixture-release-1"} {
		var got *string
		if err := e.db.Pool().QueryRow(context.Background(), `SELECT first_release FROM issues WHERE id = $1`,
			e.issue(id).id).Scan(&got); err != nil || got == nil || *got != want {
			t.Errorf("%s first_release = %v, %v; quiero %s", id, got, err, want)
		}
	}
	if r := e.issue("7003"); r.status != "silenced" {
		t.Errorf("7003 ignorado = %q, quiero silenced", r.status)
	}
	if r := e.issue("7004"); r.status != "resolved_next" || r.resolvedIn == nil || *r.resolvedIn != "fixture-release-1" {
		t.Errorf("7004 resuelto en la siguiente = %+v", r)
	}
	if r := e.issue("7005"); r.users != 1 || r.count != 7 {
		t.Errorf("7005 = %+v", r)
	}
	if n := e.count(`SELECT count(*) FROM events WHERE issue_id = $1`, e.issue("7005").id); n != 5 {
		t.Errorf("7005 tiene %d eventos, quiero 5", n)
	}
	if n := e.count(`SELECT count(*) FROM issue_status_changes WHERE source = 'import'`); n != 3 {
		t.Errorf("%d cambios de estado de la importación, quiero 3", n)
	}
	if n := e.count(`SELECT count(*) FROM releases r JOIN projects p ON p.id = r.project_id
		WHERE p.name = 'api' AND r.version = 'fixture-release-1' AND r.finalized_at IS NOT NULL`); n != 1 {
		t.Error("falta fixture-release-1 publicada en api")
	}

	// El evento guardado es el de la ingesta: la traza con el código y las etiquetas.
	latest, err := e.db.LatestEvent(context.Background(), zero.id)
	if err != nil || latest == nil {
		t.Fatalf("último evento: %v, %v", latest, err)
	}
	ev := latest.Event
	if ev.ID != "7ce73bbc4d29462b9aafeebea37d4014" || ev.Release != "fixture-release-1" || ev.Environment != "production" ||
		len(ev.Exceptions) != 1 || ev.Request == nil || ev.SDK.Name == "" {
		t.Fatalf("evento = %+v", ev)
	}
	frames := ev.Exceptions[0].Frames
	last := frames[len(frames)-1]
	if last.InApp == nil || !*last.InApp || last.ContextLine == "" || len(last.PreContext) == 0 || last.Line == 0 {
		t.Errorf("último fotograma = %+v", last)
	}
	if ev.Tags["component"] != "api" || ev.Tags["level"] != "" {
		t.Errorf("tags = %v", ev.Tags)
	}
	if n := e.count(`SELECT count(*) FROM issue_tag_values WHERE issue_id = $1 AND key = 'component'`, zero.id); n != 1 {
		t.Error("falta la etiqueta component")
	}

	// Repetir la importación no cambia nada.
	before := e.snapshot()
	again := e.run(Options{})
	wantCounts(t, "proyectos otra vez", again.Projects, Counts{Unchanged: 3})
	wantCounts(t, "releases otra vez", again.Releases, Counts{Unchanged: 3})
	wantCounts(t, "despliegues otra vez", again.Deploys, Counts{Unchanged: 4})
	wantCounts(t, "issues otra vez", again.Issues, Counts{Unchanged: 6, Skipped: 1})
	wantCounts(t, "eventos otra vez", again.Events, Counts{Unchanged: 14})
	if after := e.snapshot(); after != before {
		t.Errorf("la segunda importación ha cambiado la base:\nantes: %s\ndespués: %s", before, after)
	}
}

func TestImportUpdatesWhatChanged(t *testing.T) {
	e := newEnv(t)
	e.run(Options{})
	ctx := context.Background()
	// Aquí alguien silencia 7005; allí 7001 se resuelve y cuenta 8 eventos más.
	if _, err := e.db.SetIssueStatus(ctx, e.issue("7005").id, "silenced"); err != nil {
		t.Fatal(err)
	}
	if err := e.fake.Edit("ejemplo/issues-api", func(list []map[string]any) []map[string]any {
		for _, is := range list {
			if is["id"] == "7001" {
				is["status"], is["count"] = "resolved", "50"
			}
		}
		return list
	}); err != nil {
		t.Fatal(err)
	}
	before := e.snapshot()
	dry := e.run(Options{DryRun: true})
	if len(dry.Errors) > 0 {
		t.Fatalf("errores en la prueba: %v", dry.Errors)
	}
	wantCounts(t, "issues en la prueba", dry.Issues, Counts{Updated: 1, Unchanged: 5, Skipped: 1})
	if e.snapshot() != before {
		t.Fatal("--dry-run ha escrito")
	}
	rep := e.run(Options{})
	if len(rep.Errors) > 0 {
		t.Fatalf("errores: %v", rep.Errors)
	}
	wantCounts(t, "issues", rep.Issues, Counts{Updated: 1, Unchanged: 5, Skipped: 1})
	if r := e.issue("7001"); r.status != "resolved" || r.count != 50 {
		t.Errorf("7001 = %+v", r)
	}
	if r := e.issue("7005"); r.status != "silenced" {
		t.Errorf("7005 = %q: la importación ha pisado el estado cambiado aquí", r.status)
	}
	if n := e.count(`SELECT count(*) FROM issue_status_changes WHERE issue_id = $1 AND source = 'import'
		AND from_status = 'open' AND to_status = 'resolved'`, e.issue("7001").id); n != 1 {
		t.Error("falta el cambio de estado en el historial")
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	e := newEnv(t)
	empty := e.snapshot()
	dry := e.run(Options{DryRun: true})
	if e.snapshot() != empty {
		t.Fatal("--dry-run ha escrito en la base")
	}
	if !dry.DryRun || len(dry.Errors) > 0 {
		t.Fatalf("informe = %+v", dry)
	}
	wantCounts(t, "proyectos", dry.Projects, Counts{Created: 3})
	wantCounts(t, "issues", dry.Issues, Counts{Created: 6, Skipped: 1})
	wantCounts(t, "eventos", dry.Events, Counts{Created: 14})
	if !strings.Contains(e.out.String(), "nothing was written") {
		t.Errorf("el informe no dice que es una prueba:\n%s", e.out.String())
	}
	real := e.run(Options{})
	if real.Projects != dry.Projects || real.Issues != dry.Issues || real.Events != dry.Events ||
		real.Releases != dry.Releases || real.Deploys != dry.Deploys {
		t.Errorf("la prueba dijo %+v y la importación hizo %+v", dry, real)
	}
}

func TestProjectsSinceAndEvents(t *testing.T) {
	e := newEnv(t)
	rep := e.run(Options{Projects: []string{"web", "no-existe"}, Events: 1,
		Since: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)})
	wantCounts(t, "proyectos", rep.Projects, Counts{Created: 1})
	wantCounts(t, "issues", rep.Issues, Counts{Created: 1})
	wantCounts(t, "eventos", rep.Events, Counts{Created: 1})
	if len(rep.Errors) != 1 || !strings.Contains(rep.Errors[0], "no-existe") {
		t.Errorf("errores = %v", rep.Errors)
	}
	if n := e.count(`SELECT count(*) FROM projects`); n != 1 {
		t.Errorf("%d proyectos, quiero 1", n)
	}
}

func TestExistingProjectIsUsed(t *testing.T) {
	e := newEnv(t)
	p, err := e.db.CreateProject(context.Background(), "api", storetest.RandomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	rep := e.run(Options{Projects: []string{"api"}})
	wantCounts(t, "proyectos", rep.Projects, Counts{Unchanged: 1})
	if r := e.issue("7001"); r.project != "api" || e.count(`SELECT count(*) FROM projects`) != 1 {
		t.Errorf("no ha usado el proyecto que había (%d)", p.ID)
	}
}

func TestRateLimitRetryAfter(t *testing.T) {
	e := newEnv(t)
	e.fake.Throttle(2, "7")
	rep := e.run(Options{Projects: []string{"web"}})
	if len(rep.Errors) > 0 {
		t.Fatalf("errores: %v", rep.Errors)
	}
	wantCounts(t, "issues", rep.Issues, Counts{Created: 1})
	if len(e.waits) < 2 || e.waits[0] != 7*time.Second || e.waits[1] != 7*time.Second {
		t.Errorf("esperas = %v, quiero dos de 7s por Retry-After", e.waits)
	}
	var throttled int
	for _, r := range e.fake.Requests() {
		if r.Status == http.StatusTooManyRequests {
			throttled++
		}
	}
	if throttled != 2 {
		t.Errorf("%d respuestas 429, quiero 2", throttled)
	}
	if !strings.Contains(e.out.String(), "429 from the API: waiting 7s") {
		t.Errorf("el progreso no dice que espera:\n%s", e.out.String())
	}
}

func TestRateLimitHeaders(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	h := http.Header{}
	h.Set("Retry-After", "3")
	if d := retryAfter(h, now); d != 3*time.Second {
		t.Errorf("Retry-After 3 = %v", d)
	}
	h.Set("Retry-After", now.Add(5*time.Second).UTC().Format(http.TimeFormat))
	if d := retryAfter(h, now); d != 5*time.Second {
		t.Errorf("Retry-After con fecha = %v", d)
	}
	h = http.Header{}
	h.Set("X-Sentry-Rate-Limit-Reset", "1800000004")
	if d := retryAfter(h, now); d != 4*time.Second {
		t.Errorf("sin Retry-After, el reinicio = %v", d)
	}
}

func TestIngestGroupsWithImported(t *testing.T) {
	e := newEnv(t)
	e.run(Options{})
	ctx := context.Background()
	var project int64
	if err := e.db.Pool().QueryRow(ctx, `SELECT id FROM projects WHERE name = 'api'`).Scan(&project); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ fixture, sentryID string }{
		{"api-excepcion-1", "7001"},
		{"api-tarea-b-1", "7003"},
		{"api-log-error-1", "7004"},
		{"api-sesion-crash-1", "7005"},
	} {
		before := e.issue(tc.sentryID)
		ev := storetest.Fixture(t, tc.fixture)
		ev.ID = storetest.RandomHex(t, 16)
		res, err := e.ing.Ingest(ctx, project, &ev)
		if err != nil {
			t.Fatal(err)
		}
		if res.IssueID != before.id || res.Outcome == ingest.New {
			t.Errorf("%s ha ido al issue %d (%s), quiero el importado %d", tc.fixture, res.IssueID, res.Outcome, before.id)
			continue
		}
		if after := e.issue(tc.sentryID); after.count != before.count+1 {
			t.Errorf("%s: contador %d, quiero %d", tc.fixture, after.count, before.count+1)
		}
	}
	if n := e.count(`SELECT count(*) FROM issues WHERE project_id = $1`, project); n != 5 {
		t.Errorf("%d issues en api, quiero los 5 importados", n)
	}
	// Repetir la importación tras la ingesta no deshace el contador.
	count := e.issue("7001").count
	e.run(Options{})
	if got := e.issue("7001").count; got != count {
		t.Errorf("tras reimportar, 7001 cuenta %d; quiero %d", got, count)
	}
}

func TestNotGroupedIsReported(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	// Un issue de la ingesta con la misma huella que 7001 ya existe.
	p, err := e.db.CreateProject(ctx, "api", storetest.RandomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	ev := storetest.Fixture(t, "api-excepcion-1")
	ev.ID = storetest.RandomHex(t, 16)
	res, err := e.ing.Ingest(ctx, p.ID, &ev)
	if err != nil {
		t.Fatal(err)
	}
	rep := e.run(Options{Projects: []string{"api"}})
	if len(rep.NotGrouped) != 1 || !strings.Contains(rep.NotGrouped[0], "API-1") {
		t.Fatalf("sin agrupar = %v", rep.NotGrouped)
	}
	if r := e.issue("7001"); r.id == res.IssueID {
		t.Error("ha mezclado el issue importado con el de la ingesta")
	}
	if !strings.Contains(e.out.String(), "don't group") {
		t.Errorf("el informe no lo dice:\n%s", e.out.String())
	}
}

// hostile responde como la API falsa, pero un error que devuelve el token.
type hostile struct {
	next http.Handler
}

func (h hostile) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.Contains(r.URL.Path, "/issues/7101/") {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"detail": "token ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + ` no vale aquí"}`))
		return
	}
	h.next.ServeHTTP(w, r)
}

func TestTokenNeverLeaks(t *testing.T) {
	e := newEnv(t)
	e.srv.Config.Handler = hostile{e.fake}
	var logs bytes.Buffer
	e.ing = ingest.NewIngester(e.db.Pool(), nil, nil, slog.New(slog.NewTextHandler(&logs, nil)))
	e.fake.Throttle(1, "1")
	rep := e.run(Options{})
	if len(rep.Errors) == 0 {
		t.Fatal("quiero el error de 7101 en el informe")
	}
	js, err := json.Marshal(rep)
	if err != nil {
		t.Fatal(err)
	}
	for name, s := range map[string]string{"informe": string(js), "salida": e.out.String(), "log": logs.String()} {
		if strings.Contains(s, testToken) {
			t.Errorf("el token sale en %s:\n%s", name, s)
		}
	}
	if !slices.ContainsFunc(rep.Errors, func(s string) bool { return strings.Contains(s, "[token]") }) {
		t.Errorf("errores = %v", rep.Errors)
	}
	for _, r := range e.fake.Requests() {
		if strings.Contains(r.Query, testToken) || strings.Contains(r.Path, testToken) {
			t.Errorf("el token va en la URL: %s?%s", r.Path, r.Query)
		}
		if r.Authorization != "Bearer "+testToken {
			t.Errorf("petición sin el token en Authorization: %s", r.Path)
		}
	}
}

func TestNextPageStaysOnServer(t *testing.T) {
	c, err := NewClient("https://sentry.example.com", "x", nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := c.nextPage(`<https://sentry.example.com/api/0/x/?cursor=0:0:1>; rel="previous"; results="false"; cursor="0:0:1", ` +
		`<https://sentry.example.com/api/0/x/?cursor=0:100:0>; rel="next"; results="true"; cursor="0:100:0"`)
	if err != nil || u == nil || u.Query().Get("cursor") != "0:100:0" {
		t.Fatalf("siguiente = %v, %v", u, err)
	}
	if u, err := c.nextPage(`<https://sentry.example.com/api/0/x/?cursor=0:100:0>; rel="next"; results="false"; cursor="0:100:0"`); u != nil || err != nil {
		t.Errorf("sin resultados = %v, %v", u, err)
	}
	if _, err := c.nextPage(`<https://otro.example.com/api/0/x/?cursor=0:100:0>; rel="next"; results="true"; cursor="0:100:0"`); err == nil {
		t.Error("ha seguido un enlace a otro servidor")
	}
}
