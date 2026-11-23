package ingest

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

const fixturesDir = "../../testdata/envelopes"

// Los proyectos y claves de los DSN con que se grabaron los fixtures.
var fixtureProjects = []struct {
	id   int64
	name string
	key  string
}{
	{1, "api", "5b6d3a35fd6eb6e0f9d410fc5c0f6cbc"},
	{2, "web", "ce01335f125960aa6957a133d15b5f3a"},
	{3, "front", "7438a4393be53fabe513ce19bec380e9"},
}

// Fixtures con un event y el issue que deben abrir, según tools/fixtures/README.md.
var errorFixtures = map[string]struct{ title, culprit string }{
	"api-excepcion-1":     {"ZeroDivisionError: division by zero", "app in media"},
	"api-validacion-1":    {"ValidationError: 1 validation error for Pedido", "app in importar"},
	"api-log-exception-1": {"KeyError: 'oeste'", "app in sincronizar_stock"},
	"api-log-error-1":     {"pagos.reembolso_rechazado", ""},
	"api-tarea-a-1":       {"task failed id=88dfc065281046dfa0098d0fa91bbdf4", ""},
	"api-tarea-b-1":       {"task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35", ""},
	"web-typeerror-1":     {"TypeError: Cannot read properties of undefined (reading 'reduce')", "https://app.example.com/chunk-Z554BZQ6.js in a"},
	"web-cli-upload-1":    {"TypeError: Cannot read properties of undefined (reading 'reduce')", "https://app.example.com/chunk-Z554BZQ6.js in a"},
	"web-cli-inject-1":    {"TypeError: Cannot read properties of undefined (reading 'reduce')", "https://app.example.com/chunk-Z554BZQ6.js in a"},
	"api-sesion-crash-1":  {"KeyError: 'oeste'", "app in sincronizar_stock_estricto"},
}

var noEventFixtures = []string{
	"api-excepcion-2",
	"api-validacion-2",
	"api-descartado-1", "api-descartado-2",
	"web-sesion-1", "web-sesion-2",
	"api-sesiones-1", "api-sesion-ok-1", "api-sesion-crash-2",
}

// Fixtures con check-ins de crons, en el orden en que se mandaron.
var checkInFixtures = []string{
	"api-cron-ok-1", "api-cron-ok-2",
	"api-cron-error-1", "api-cron-error-2",
	"cli-monitors-run-1", "cli-monitors-run-2",
	"cli-monitors-run-error-1", "cli-monitors-run-error-2",
}

type fixture struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
	body    []byte
}

func loadFixture(t *testing.T, name string) fixture {
	t.Helper()
	var f fixture
	b, err := os.ReadFile(filepath.Join(fixturesDir, name+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	if f.body, err = os.ReadFile(filepath.Join(fixturesDir, name+".envelope")); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f fixture) request(body []byte) *http.Request {
	target := f.Path
	if f.Query != "" {
		target += "?" + f.Query
	}
	r := httptest.NewRequest(f.Method, target, bytes.NewReader(body))
	for k, v := range f.Headers {
		if !strings.EqualFold(k, "Host") && !strings.EqualFold(k, "Content-Length") {
			r.Header.Set(k, v)
		}
	}
	return r
}

func (f fixture) envelope(t *testing.T) *envelope.Envelope {
	t.Helper()
	data, err := envelope.Decompress(bytes.NewReader(f.body), f.Headers["Content-Encoding"], MaxDecompressed)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

func (f fixture) eventID(t *testing.T) string {
	return f.envelope(t).Header.EventID
}

// withEventID devuelve el mismo envío con otro event_id, en la misma codificación.
func (f fixture) withEventID(t *testing.T, id string) []byte {
	t.Helper()
	data, err := envelope.Decompress(bytes.NewReader(f.body), f.Headers["Content-Encoding"], MaxDecompressed)
	if err != nil {
		t.Fatal(err)
	}
	old := f.eventID(t)
	if !bytes.Contains(data, []byte(old)) {
		t.Fatalf("el envío no contiene su event_id %s", old)
	}
	data = bytes.ReplaceAll(data, []byte(old), []byte(id))
	if f.Headers["Content-Encoding"] != "gzip" {
		return data
	}
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(data)
	_ = zw.Close()
	return buf.Bytes()
}

// withoutRelease devuelve el envío sin release. Solo vale para los del
// navegador, que van sin comprimir y sin length en los items.
func (f fixture) withoutRelease(t *testing.T) []byte {
	t.Helper()
	release := []byte(`"release":"fixture-release-1",`)
	if f.Headers["Content-Encoding"] != "" || bytes.Contains(f.body, []byte(`"length":`)) || !bytes.Contains(f.body, release) {
		t.Fatal("el envío no es del navegador o no lleva release")
	}
	return bytes.ReplaceAll(f.body, release, nil)
}

type call struct {
	issueID int64
	eventID string
	outcome Outcome
}

type recorder struct {
	mu    sync.Mutex
	calls []call
}

func (r *recorder) IssueChanged(ctx context.Context, tx db.Tx, ev *event.Event, res Result) error {
	issueID := res.IssueID
	// Comprueba que el aviso ve el issue de su propia transacción.
	var n int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM issues WHERE id = $1`, issueID).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return fmt.Errorf("el aviso no ve el issue %d", issueID)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, call{issueID, ev.ID, res.Outcome})
	return nil
}

func (r *recorder) all() []call {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]call(nil), r.calls...)
}

type testEnv struct {
	s     *store.Store
	h     http.Handler
	notes *recorder
	ing   *Ingester
	drops *Drops
}

func newTestEnv(t *testing.T, perMinute int) *testEnv {
	t.Helper()
	s := storetest.New(t)
	ctx := context.Background()
	for _, p := range fixtureProjects {
		if _, err := s.Pool().Exec(ctx, storetest.ProjectWithIDSQL(s.Pool().Dialect()),
			p.id, p.name, p.key); err != nil {
			t.Fatal(err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	notes := &recorder{}
	ing := NewIngester(s.Pool(), nil, notes, log)
	mux := http.NewServeMux()
	drops := NewDrops(s)
	mux.Handle(Pattern, NewHandler(s, ing, NewRateLimiter(perMinute), drops, log))
	return &testEnv{s: s, h: mux, notes: notes, ing: ing, drops: drops}
}

func (e *testEnv) send(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, r)
	return rec
}

func (e *testEnv) sendOK(t *testing.T, f fixture, body []byte) string {
	t.Helper()
	rec := e.send(f.request(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("código %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
		t.Error("falta Access-Control-Allow-Origin")
	}
	var resp struct{ ID string }
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("respuesta %q: %v", rec.Body, err)
	}
	return resp.ID
}

func (e *testEnv) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := e.s.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type issueRow struct {
	id                  int64
	title, culprit      string
	status              string
	count               int64
	regressed           bool
	resolvedInRelease   *string
	firstSeen, lastSeen time.Time
}

func (e *testEnv) issues(t *testing.T) []issueRow {
	t.Helper()
	rows, err := e.s.Pool().Query(context.Background(), `
		SELECT id, title, culprit, status, event_count, last_regressed_at IS NOT NULL, resolved_in_release, first_seen, last_seen
		FROM issues ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (issueRow, error) {
		var i issueRow
		err := r.Scan(&i.id, &i.title, &i.culprit, &i.status, &i.count, &i.regressed, &i.resolvedInRelease, &i.firstSeen, &i.lastSeen)
		return i, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func newHexID() string { return newID() }

func TestFixtureTablesCoverTestdata(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*.envelope"))
	if err != nil {
		t.Fatal(err)
	}
	if rows := len(errorFixtures) + len(noEventFixtures) + len(checkInFixtures); len(files) != rows {
		t.Fatalf("%d envelopes y %d filas en las tablas", len(files), rows)
	}
}

func TestErrorFixtureOpensIssue(t *testing.T) {
	for name, want := range errorFixtures {
		t.Run(name, func(t *testing.T) {
			e := newTestEnv(t, 1000)
			f := loadFixture(t, name)
			orig := f.eventID(t)

			if id := e.sendOK(t, f, f.body); id != orig {
				t.Errorf("id respondido %q, quiero %q", id, orig)
			}
			is := e.issues(t)
			if len(is) != 1 || is[0].count != 1 || is[0].status != "open" {
				t.Fatalf("issues = %+v", is)
			}
			if !strings.HasPrefix(is[0].title, want.title) || is[0].culprit != want.culprit {
				t.Errorf("título %q, culprit %q; quiero %q…, %q", is[0].title, is[0].culprit, want.title, want.culprit)
			}
			if n := e.count(t, `SELECT count(*) FROM events WHERE event_id = $1 AND issue_id = $2`, orig, is[0].id); n != 1 {
				t.Errorf("%d eventos con el id original", n)
			}

			other := newHexID()
			if id := e.sendOK(t, f, f.withEventID(t, other)); id != other {
				t.Errorf("id respondido %q, quiero %q", id, other)
			}
			is = e.issues(t)
			if len(is) != 1 || is[0].count != 2 {
				t.Fatalf("tras repetir con otro id: issues = %+v", is)
			}

			e.sendOK(t, f, f.body)
			is = e.issues(t)
			if len(is) != 1 || is[0].count != 2 || e.count(t, `SELECT count(*) FROM events`) != 2 {
				t.Fatalf("el mismo event_id no debe contar dos veces: issues = %+v", is)
			}

			calls := e.notes.all()
			wantCalls := []call{{is[0].id, orig, New}, {is[0].id, other, Unchanged}}
			if len(calls) != 2 || calls[0] != wantCalls[0] || calls[1] != wantCalls[1] {
				t.Errorf("avisos = %+v, quiero %+v", calls, wantCalls)
			}
		})
	}
}

func TestTaskFailedLogsSameIssue(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range []string{"api-tarea-a-1", "api-tarea-b-1"} {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	is := e.issues(t)
	if len(is) != 1 || is[0].count != 2 {
		t.Fatalf("issues = %+v", is)
	}
	if n := e.count(t, `SELECT count(*) FROM events WHERE issue_id = $1`, is[0].id); n != 2 {
		t.Errorf("%d eventos, quiero 2", n)
	}
	calls := e.notes.all()
	if len(calls) != 2 || calls[0].outcome != New || calls[1].outcome != Unchanged {
		t.Errorf("avisos = %+v", calls)
	}
}

func TestFixturesWithoutEventStoreNothing(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range noEventFixtures {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	if n := e.count(t, `SELECT (SELECT count(*) FROM issues) + (SELECT count(*) FROM events)`); n != 0 {
		t.Errorf("%d filas guardadas", n)
	}
	if calls := e.notes.all(); len(calls) != 0 {
		t.Errorf("avisos = %+v", calls)
	}
}

func TestStatusOnNewEvent(t *testing.T) {
	for _, tc := range []struct {
		name, fixture, status string
		noRelease             bool
		resolvedIn            *string
		wantStatus            string
		want                  Outcome
	}{
		{"resolved", "api-excepcion-1", "resolved", false, nil, "open", Regressed},
		{"resolved_next mismo release", "api-excepcion-1", "resolved_next", false, ptr("fixture-release-1"), "resolved_next", Unchanged},
		{"resolved_next otro release", "api-excepcion-1", "resolved_next", false, ptr("fixture-release-0"), "open", Regressed},
		{"resolved_next sin release", "web-typeerror-1", "resolved_next", true, ptr("fixture-release-0"), "resolved_next", Unchanged},
		{"silenced", "api-excepcion-1", "silenced", false, nil, "silenced", Unchanged},
		{"open", "api-excepcion-1", "open", false, nil, "open", Unchanged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newTestEnv(t, 1000)
			f := loadFixture(t, tc.fixture)
			if tc.noRelease {
				f.body = f.withoutRelease(t)
			}
			e.sendOK(t, f, f.body)
			if _, err := e.s.Pool().Exec(context.Background(),
				`UPDATE issues SET status = $1, resolved_in_release = $2`, tc.status, tc.resolvedIn); err != nil {
				t.Fatal(err)
			}

			e.sendOK(t, f, f.withEventID(t, newHexID()))
			is := e.issues(t)
			if len(is) != 1 {
				t.Fatalf("issues = %+v", is)
			}
			got := is[0]
			if got.status != tc.wantStatus || got.count != 2 {
				t.Errorf("estado %q y %d eventos; quiero %q y 2", got.status, got.count, tc.wantStatus)
			}
			if got.regressed != (tc.want == Regressed) {
				t.Errorf("last_regressed_at puesta = %v", got.regressed)
			}
			if tc.want == Regressed && got.resolvedInRelease != nil {
				t.Errorf("resolved_in_release = %q tras reabrir", *got.resolvedInRelease)
			}
			calls := e.notes.all()
			if len(calls) != 2 || calls[1].outcome != tc.want {
				t.Errorf("avisos = %+v, quiero el último %v", calls, tc.want)
			}
		})
	}
}

func TestNotifierErrorRollsBack(t *testing.T) {
	s := storetest.New(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "p", "k")
	if err != nil {
		t.Fatal(err)
	}
	ing := NewIngester(s.Pool(), nil, failingNotifier{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev := &event.Event{ID: newHexID(), Message: "hola", Level: "error"}
	if _, err := ing.Ingest(ctx, p.ID, ev); err == nil {
		t.Fatal("quiero el error del aviso")
	}
	var n int
	if err := s.Pool().QueryRow(ctx, `SELECT (SELECT count(*) FROM issues) + (SELECT count(*) FROM events)`).Scan(&n); err != nil || n != 0 {
		t.Errorf("filas = %d, %v", n, err)
	}
}

type failingNotifier struct{}

func (failingNotifier) IssueChanged(context.Context, db.Tx, *event.Event, Result) error {
	return io.ErrUnexpectedEOF
}

func TestConcurrentEventsOpenOneIssue(t *testing.T) {
	e := newTestEnv(t, 1000)
	f := loadFixture(t, "api-excepcion-1")
	const n = 16
	bodies := make([][]byte, n)
	for i := range bodies {
		bodies[i] = f.withEventID(t, newHexID())
	}
	var wg sync.WaitGroup
	codes := make([]int, n)
	for i := range n {
		wg.Go(func() { codes[i] = e.send(f.request(bodies[i])).Code })
	}
	wg.Wait()
	for i, c := range codes {
		if c != http.StatusOK {
			t.Errorf("envío %d: código %d", i, c)
		}
	}
	is := e.issues(t)
	if len(is) != 1 || is[0].count != n {
		t.Fatalf("issues = %+v", is)
	}
	news := 0
	for _, c := range e.notes.all() {
		if c.outcome == New {
			news++
		}
	}
	if news != 1 {
		t.Errorf("%d avisos New, quiero 1", news)
	}
}

func TestRejections(t *testing.T) {
	e := newTestEnv(t, 1000)
	f := loadFixture(t, "api-excepcion-1")
	front := loadFixture(t, "web-typeerror-1")

	gz := func(data []byte) []byte {
		var buf bytes.Buffer
		zw := gzip.NewWriter(&buf)
		_, _ = zw.Write(data)
		_ = zw.Close()
		return buf.Bytes()
	}
	bomb := gz(append([]byte("{}\n{\"type\":\"event\"}\n{\"message\":\""), bytes.Repeat([]byte("a"), MaxDecompressed+1)...))
	if len(bomb) > MaxCompressed {
		t.Fatalf("la bomba ocupa %d bytes comprimida", len(bomb))
	}
	big := append([]byte("{}\n{\"type\":\"event\"}\n{\"message\":\""), bytes.Repeat([]byte("a"), MaxCompressed)...)

	for _, tc := range []struct {
		name string
		req  func() *http.Request
		want int
	}{
		{"sin clave", func() *http.Request {
			r := f.request(f.body)
			r.Header.Del("X-Sentry-Auth")
			return r
		}, http.StatusUnauthorized},
		{"clave de otro proyecto", func() *http.Request {
			r := f.request(f.body)
			r.Header.Set("X-Sentry-Auth", "Sentry sentry_key=ce01335f125960aa6957a133d15b5f3a, sentry_version=7")
			return r
		}, http.StatusUnauthorized},
		{"clave mala en la query", func() *http.Request {
			g := front
			g.Query = strings.Replace(g.Query, "ce01335f125960aa6957a133d15b5f3a", "00000000000000000000000000000000", 1)
			return g.request(front.body)
		}, http.StatusUnauthorized},
		{"proyecto que no existe", func() *http.Request {
			g := f
			g.Path = "/api/999/envelope/"
			return g.request(f.body)
		}, http.StatusNotFound},
		{"proyecto no numérico", func() *http.Request {
			g := f
			g.Path = "/api/abc/envelope/"
			return g.request(f.body)
		}, http.StatusNotFound},
		{"más de 1 MiB con Content-Length", func() *http.Request {
			r := f.request(big)
			r.Header.Del("Content-Encoding")
			return r
		}, http.StatusRequestEntityTooLarge},
		{"más de 1 MiB sin Content-Length", func() *http.Request {
			r := f.request(big)
			r.Header.Del("Content-Encoding")
			r.ContentLength = -1
			return r
		}, http.StatusRequestEntityTooLarge},
		{"bomba gzip", func() *http.Request { return f.request(bomb) }, http.StatusRequestEntityTooLarge},
		{"envelope roto", func() *http.Request {
			return f.request(gz([]byte("{}\n{\"type\":\"event\",\"length\":100}\n{\"message\":\"x\"}\n")))
		}, http.StatusBadRequest},
		{"gzip roto", func() *http.Request { return f.request(f.body[:len(f.body)/2]) }, http.StatusBadRequest},
		{"event que no es json", func() *http.Request {
			return f.request(gz([]byte("{}\n{\"type\":\"event\"}\n{no es json}\n")))
		}, http.StatusBadRequest},
		{"GET", func() *http.Request {
			return httptest.NewRequest(http.MethodGet, "/api/1/envelope/", nil)
		}, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := e.send(tc.req())
			if rec.Code != tc.want {
				t.Fatalf("código %d, quiero %d: %s", rec.Code, tc.want, rec.Body)
			}
			if rec.Header().Get("Access-Control-Allow-Origin") != "*" {
				t.Error("falta Access-Control-Allow-Origin")
			}
		})
	}
	if n := e.count(t, `SELECT (SELECT count(*) FROM issues) + (SELECT count(*) FROM events)`); n != 0 {
		t.Errorf("%d filas guardadas tras los rechazos", n)
	}
}

func TestPreflight(t *testing.T) {
	e := newTestEnv(t, 1000)
	r := httptest.NewRequest(http.MethodOptions, "/api/2/envelope/?sentry_version=7", nil)
	r.Header.Set("Origin", "https://app.example.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	r.Header.Set("Access-Control-Request-Headers", "content-type")
	rec := e.send(r)
	h := rec.Header()
	if rec.Code != http.StatusNoContent || h.Get("Access-Control-Allow-Origin") != "*" ||
		!strings.Contains(h.Get("Access-Control-Allow-Methods"), "POST") ||
		!strings.Contains(strings.ToLower(h.Get("Access-Control-Allow-Headers")), "content-type") ||
		!strings.Contains(h.Get("Access-Control-Expose-Headers"), "X-Sentry-Rate-Limits") {
		t.Fatalf("código %d, cabeceras %v", rec.Code, h)
	}
}

func TestRateLimit(t *testing.T) {
	e := newTestEnv(t, 2)
	f := loadFixture(t, "api-excepcion-1")
	for range 2 {
		e.sendOK(t, f, f.withEventID(t, newHexID()))
	}
	rec := e.send(f.request(f.withEventID(t, newHexID())))
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("código %d, quiero 429", rec.Code)
	}
	retry := rec.Header().Get("Retry-After")
	if !regexp.MustCompile(`^[1-9][0-9]*$`).MatchString(retry) {
		t.Errorf("Retry-After = %q", retry)
	}
	if got := rec.Header().Get("X-Sentry-Rate-Limits"); got != retry+":error:project" {
		t.Errorf("X-Sentry-Rate-Limits = %q", got)
	}
	if n := e.count(t, `SELECT count(*) FROM events`); n != 2 {
		t.Errorf("%d eventos guardados, quiero 2", n)
	}

	// Lo que no trae eventos y los demás proyectos siguen pasando.
	s := loadFixture(t, "api-excepcion-2")
	e.sendOK(t, s, s.body)
	g := loadFixture(t, "web-typeerror-1")
	e.sendOK(t, g, g.body)
}

func TestPurge(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range []string{"api-excepcion-1", "api-log-error-1"} {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	ctx := context.Background()
	old := time.Now().AddDate(0, 0, -100)
	for _, q := range []string{
		`UPDATE events SET received_at = $1, occurred_at = $1 WHERE issue_id IN (SELECT id FROM issues WHERE title LIKE 'ZeroDivisionError%')`,
		`UPDATE issues SET first_seen = $1, last_seen = $1 WHERE title LIKE 'ZeroDivisionError%'`,
	} {
		if _, err := e.s.Pool().Exec(ctx, q, old); err != nil {
			t.Fatal(err)
		}
	}
	evs, iss, err := Purge(ctx, e.s.Pool(), time.Now().AddDate(0, 0, -90))
	if err != nil || evs != 1 || iss != 1 {
		t.Fatalf("borrados %d eventos y %d issues, %v", evs, iss, err)
	}
	is := e.issues(t)
	if len(is) != 1 || is[0].title != "pagos.reembolso_rechazado" {
		t.Fatalf("issues = %+v", is)
	}
}

// Un evento con fecha antigua que llega a un issue mientras se purga no debe
// perderse ni bloquear la purga.
func TestPurgeSkipsIssueBeingIngested(t *testing.T) {
	storetest.OnlyPostgres(t, "con SQLite la transacción abierta tiene el cerrojo de escritura y la purga espera a que acabe; "+
		"no hay filas bloqueadas de las que saltarse")
	e := newTestEnv(t, 1000)
	f := loadFixture(t, "api-excepcion-1")
	e.sendOK(t, f, f.body)
	ctx := context.Background()
	old := time.Now().AddDate(0, 0, -100)
	if _, err := e.s.Pool().Exec(ctx, `UPDATE events SET received_at = $1, occurred_at = $1`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool().Exec(ctx, `UPDATE issues SET first_seen = $1, last_seen = $1`, old); err != nil {
		t.Fatal(err)
	}

	tx, err := e.s.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ing := NewIngester(e.s.Pool(), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	env := f.envelope(t)
	ev, err := event.Parse(env.Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	ev.ID = newHexID()
	ev.Timestamp = old
	enc, err := store.EncodeEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing.save(ctx, tx, 1, ev, Group(ev), enc, time.Now(), DefaultLimits()); err != nil {
		t.Fatal(err)
	}

	pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	evs, iss, err := Purge(pctx, e.s.Pool(), time.Now().AddDate(0, 0, -90))
	if err != nil || evs != 1 || iss != 0 {
		t.Fatalf("con el issue bloqueado: borrados %d eventos y %d issues, %v", evs, iss, err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("guardar el evento tras la purga: %v", err)
	}
	if _, iss, err := Purge(ctx, e.s.Pool(), time.Now().AddDate(0, 0, -90)); err != nil || iss != 0 {
		t.Fatalf("el issue tiene un evento nuevo: borrados %d issues, %v", iss, err)
	}
	if is := e.issues(t); len(is) != 1 || e.count(t, `SELECT count(*) FROM events`) != 1 {
		t.Fatalf("issues = %+v", is)
	}
}

// Un evento con fecha antigua que se está guardando mientras empieza la purga
// no se pierde, con los dos motores: con Postgres la purga se salta su issue;
// con SQLite espera a que acabe la transacción del evento.
func TestPurgeKeepsEventBeingIngested(t *testing.T) {
	e := newTestEnv(t, 1000)
	f := loadFixture(t, "api-excepcion-1")
	e.sendOK(t, f, f.body)
	ctx := context.Background()
	old := time.Now().AddDate(0, 0, -100)
	for _, q := range []string{`UPDATE events SET received_at = $1, occurred_at = $1`, `UPDATE issues SET first_seen = $1, last_seen = $1`} {
		if _, err := e.s.Pool().Exec(ctx, q, old); err != nil {
			t.Fatal(err)
		}
	}

	tx, err := e.s.Pool().Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	ing := NewIngester(e.s.Pool(), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ev, err := event.Parse(f.envelope(t).Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	ev.ID = newHexID()
	ev.Timestamp = old
	enc, err := store.EncodeEvent(ev)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ing.save(ctx, tx, 1, ev, Group(ev), enc, time.Now(), DefaultLimits()); err != nil {
		t.Fatal(err)
	}

	type purged struct {
		evs, iss int64
		err      error
	}
	done := make(chan purged, 1)
	go func() {
		pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		evs, iss, err := Purge(pctx, e.s.Pool(), time.Now().AddDate(0, 0, -90))
		done <- purged{evs, iss, err}
	}()
	time.Sleep(200 * time.Millisecond)
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("guardar el evento con la purga en marcha: %v", err)
	}
	if p := <-done; p.err != nil || p.evs != 1 || p.iss != 0 {
		t.Fatalf("purga: borrados %d eventos y %d issues, %v", p.evs, p.iss, p.err)
	}
	var n int64
	if err := e.s.Pool().QueryRow(ctx, `SELECT count(*) FROM events WHERE event_id = $1`, ev.ID).Scan(&n); err != nil || n != 1 {
		t.Fatalf("el evento guardado durante la purga: %d, %v", n, err)
	}
	if is := e.issues(t); len(is) != 1 {
		t.Fatalf("issues = %+v", is)
	}
}

func TestValidID(t *testing.T) {
	if !validID(hex.EncodeToString(make([]byte, 16))) || validID("xyz") || validID("") {
		t.Fatal("validID")
	}
}

func TestStoredEventKeepsDebugMeta(t *testing.T) {
	e := newTestEnv(t, 1000)
	for _, name := range []string{"web-cli-inject-1", "web-cli-upload-1"} {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}
	rows, err := e.s.Pool().Query(context.Background(), `SELECT CAST(id AS text) FROM events ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	ids, err := db.CollectRows(rows, db.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	withMeta, injected := 0, 0
	for _, id := range ids {
		ev, err := e.s.Event(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if len(ev.Event.DebugMeta) == 0 {
			continue
		}
		withMeta++
		var meta struct{ Images []json.RawMessage }
		if err := json.Unmarshal(ev.Event.DebugMeta, &meta); err != nil {
			t.Fatal(err)
		}
		if len(meta.Images) == 2 && ev.Event.Release == "prueba-inject" {
			injected++
		}
	}
	if injected != 1 {
		t.Errorf("%d eventos con debug_meta", injected)
	}
	if withMeta != 1 {
		t.Errorf("%d eventos con debug_meta; el de upload no lo trae", withMeta)
	}
}
