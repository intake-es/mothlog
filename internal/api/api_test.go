package api_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type env struct {
	db    *storetest.DB
	srv   *httptest.Server
	token string
}

func setup(t *testing.T) *env {
	t.Helper()
	db := storetest.NewDB(t)
	mux := http.NewServeMux()
	// Las rutas de ingesta (INT-246) y de source maps (INT-247) conviven con las de la API.
	noop := func(http.ResponseWriter, *http.Request) {}
	mux.HandleFunc("POST /api/{project_id}/envelope/", noop)
	mux.HandleFunc("POST /api/{project_id}/releases/{release}/sourcemaps/", noop)
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com"}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(context.Background(), "tests")
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, srv: srv, token: token}
}

func (e *env) do(t *testing.T, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	return res.StatusCode, b
}

func (e *env) get(t *testing.T, path string, v any) {
	t.Helper()
	code, b := e.do(t, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, code, b)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, b)
	}
}

func ids(issues []store.Issue) []int64 {
	out := make([]int64, len(issues))
	for i, is := range issues {
		out[i] = is.ID
	}
	return out
}

func TestProjects(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	var got struct {
		Data []struct {
			ID   int64  `json:"id"`
			Name string `json:"name"`
			DSN  string `json:"dsn"`
		} `json:"data"`
	}
	e.get(t, "/api/0/projects/", &got)
	want := "https://" + p.PublicKey + "@errors.example.com/" + itoa(p.ID)
	if len(got.Data) != 1 || got.Data[0].Name != "backend" || got.Data[0].DSN != want {
		t.Fatalf("= %+v; quiero dsn %s", got, want)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

func TestListIssues(t *testing.T) {
	e := setup(t)
	back := e.db.AddProject(t, "backend")
	front := e.db.AddProject(t, "front")
	base := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	at := func(h int) time.Time { return base.Add(time.Duration(h) * time.Hour) }

	zero := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "ZeroDivisionError: division by zero", Culprit: "app.core.http.middlewares in __call__", FirstSeen: at(0), LastSeen: at(10), Count: 5})
	valid := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "ValidationError: 1 validation error", Culprit: "app.pedidos in validar", FirstSeen: at(1), LastSeen: at(8), Count: 50})
	typeErr := e.db.AddIssue(t, storetest.Issue{ProjectID: front.ID, Title: "TypeError: Cannot read properties of undefined", Culprit: "t.failForFixture", FirstSeen: at(2), LastSeen: at(9), Count: 1})
	resolved := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "KeyError: 'numero'", Status: "resolved", FirstSeen: at(3), LastSeen: at(3), Count: 2})
	silenced := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "task failed id=<id>", Status: "silenced", FirstSeen: at(4), LastSeen: at(12), Count: 7})
	next := e.db.AddIssue(t, storetest.Issue{ProjectID: front.ID, Title: "Error 50%_off", Status: "resolved_next", FirstSeen: at(5), LastSeen: at(5), Count: 3})
	// Mismo last_seen que zero: el desempate es por id.
	tie := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "RuntimeError: empate", FirstSeen: at(6), LastSeen: at(10), Count: 5})

	for _, tc := range []struct {
		name  string
		query string
		want  []int64
	}{
		{"abiertos por defecto, por last_seen", "", []int64{tie, zero, typeErr, valid}},
		{"all", "status=all", []int64{silenced, tie, zero, typeErr, valid, next, resolved}},
		{"resolved", "status=resolved", []int64{resolved}},
		{"resolved_next", "status=resolved_next", []int64{next}},
		{"silenced", "status=silenced", []int64{silenced}},
		{"proyecto por id", "project=" + itoa(front.ID), []int64{typeErr}},
		{"proyecto por nombre", "project=backend&status=all", []int64{silenced, tie, zero, valid, resolved}},
		{"first_seen", "sort=first_seen", []int64{tie, typeErr, valid, zero}},
		{"count", "sort=count&status=all", []int64{valid, silenced, tie, zero, next, resolved, typeErr}},
		{"búsqueda en el título sin mayúsculas", "query=zerodivision", []int64{zero}},
		{"búsqueda en el culprit", "query=failForFixture", []int64{typeErr}},
		{"los comodines de LIKE son texto", "query=50%25_&status=all", []int64{next}},
		{"sin resultados", "query=nada", []int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var page store.IssuePage
			e.get(t, "/api/0/issues/?"+tc.query, &page)
			if got := ids(page.Issues); !slices.Equal(got, tc.want) {
				t.Errorf("= %v; quiero %v", got, tc.want)
			}
			if page.NextCursor != "" {
				t.Errorf("next_cursor sin más páginas: %q", page.NextCursor)
			}
		})
	}

	t.Run("campos", func(t *testing.T) {
		rel := "r7"
		reg := at(11)
		id := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "Con regresión", Type: "error", FirstSeen: at(0), LastSeen: at(11), Count: 1, ResolvedInRelease: &rel, LastRegression: &reg})
		ev := storetest.Fixture(t, "backend-fastapi-zerodivision-1")
		e.db.AddEvent(t, back.ID, id, ev, at(11))
		var page store.IssuePage
		e.get(t, "/api/0/issues/?limit=1", &page)
		got := page.Issues[0]
		if got.ID != id || got.Project != "backend" || got.ProjectID != back.ID || got.Level != "error" || got.Status != "open" ||
			got.LastRelease == nil || *got.LastRelease != "fixture-release-1" || got.LastRegression == nil || !got.LastRegression.Equal(reg) ||
			!got.FirstSeen.Equal(at(0)) || !got.LastSeen.Equal(at(11)) || got.Count != 1 {
			t.Errorf("= %+v", got)
		}
		if _, err := e.db.Pool().Exec(context.Background(), `DELETE FROM issues WHERE id = $1`, id); err != nil {
			t.Fatal(err)
		}
	})

	for _, sort := range []string{"last_seen", "first_seen", "count"} {
		t.Run("paginación por "+sort, func(t *testing.T) {
			var all store.IssuePage
			e.get(t, "/api/0/issues/?status=all&sort="+sort, &all)
			var walked []int64
			cursor := ""
			for range 10 {
				var page store.IssuePage
				e.get(t, "/api/0/issues/?status=all&limit=2&sort="+sort+"&cursor="+url.QueryEscape(cursor), &page)
				walked = append(walked, ids(page.Issues)...)
				if page.NextCursor == "" {
					break
				}
				if len(page.Issues) != 2 {
					t.Fatalf("página con next_cursor y %d issues", len(page.Issues))
				}
				cursor = page.NextCursor
			}
			if !slices.Equal(walked, ids(all.Issues)) || len(walked) != 7 {
				t.Errorf("paginando = %v; de una vez = %v", walked, ids(all.Issues))
			}
		})
	}

	t.Run("cursor de otro orden", func(t *testing.T) {
		var page store.IssuePage
		e.get(t, "/api/0/issues/?status=all&limit=1", &page)
		code, b := e.do(t, http.MethodGet, "/api/0/issues/?status=all&sort=count&cursor="+page.NextCursor, "")
		if code != http.StatusBadRequest || !strings.Contains(string(b), `"error"`) {
			t.Errorf("= %d %s", code, b)
		}
	})

	for _, q := range []string{"status=cerrado", "sort=title", "limit=0x", "limit=101", "cursor=basura"} {
		t.Run("400 con "+q, func(t *testing.T) {
			code, b := e.do(t, http.MethodGet, "/api/0/issues/?"+q, "")
			var body map[string]string
			if code != http.StatusBadRequest || json.Unmarshal(b, &body) != nil || body["error"] == "" {
				t.Errorf("= %d %s", code, b)
			}
		})
	}
}

func TestIssueDetail(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	now := time.Now().UTC()
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "ZeroDivisionError: division by zero", FirstSeen: now.AddDate(0, 0, -20), LastSeen: now, Count: 7})
	base := storetest.Fixture(t, "backend-fastapi-zerodivision-1")
	ev := func(days int, env, release, user, org string) {
		x := base
		x.Environment, x.Release = env, release
		x.User = &event.User{Email: user}
		x.Tags = map[string]string{"organization_id": org}
		e.db.AddEvent(t, p.ID, id, x, now.AddDate(0, 0, -days))
	}
	ev(20, "production", "r1", "ana@x.es", "7") // fuera de la gráfica
	ev(13, "production", "r1", "ana@x.es", "7")
	ev(1, "production", "r2", "ana@x.es", "7")
	ev(1, "staging", "r2", "luis@x.es", "9")
	ev(0, "production", "r2", "", "7")
	ev(0, "production", "r3", "", "")
	latest := e.db.AddEvent(t, p.ID, id, base, now.Add(time.Second))

	var d store.IssueDetail
	e.get(t, "/api/0/issues/"+itoa(id)+"/", &d)
	if d.ID != id || d.Title != "ZeroDivisionError: division by zero" || d.Count != 7 {
		t.Errorf("issue = %+v", d.Issue)
	}
	if d.LatestEvent == nil || d.LatestEvent.ID != latest || d.LatestEvent.Event.Exceptions[0].Type != "ZeroDivisionError" {
		t.Fatalf("último evento = %+v", d.LatestEvent)
	}

	if len(d.Daily) != store.DailyDays {
		t.Fatalf("%d días", len(d.Daily))
	}
	counts := map[string]int64{}
	var total int64
	for _, c := range d.Daily {
		counts[c.Date] = c.Count
		total += c.Count
	}
	day := func(n int) string { return now.AddDate(0, 0, -n).Format("2006-01-02") }
	if d.Daily[0].Date != day(13) || d.Daily[13].Date != day(0) {
		t.Errorf("de %s a %s", d.Daily[0].Date, d.Daily[13].Date)
	}
	if counts[day(13)] != 1 || counts[day(1)] != 2 || counts[day(0)] != 3 || total != 6 {
		t.Errorf("gráfica = %v", d.Daily)
	}

	// Por frecuencia y, a igualdad, por valor.
	want := map[string]string{
		"environment":     "production=5 development=1 staging=1",
		"release":         "r2=3 r1=2 fixture-release-1=1 r3=1",
		"user":            "ana@x.es=3 luis@x.es=1",
		"organization_id": "7=4 9=1",
	}
	for k, w := range want {
		var got []string
		for _, v := range d.TopValues[k] {
			got = append(got, v.Value+"="+strconv.FormatInt(v.Count, 10))
		}
		if strings.Join(got, " ") != w {
			t.Errorf("%s = %v; quiero %s", k, got, w)
		}
	}

	code, b := e.do(t, http.MethodGet, "/api/0/issues/999999/", "")
	if code != http.StatusNotFound || !strings.Contains(string(b), `"error"`) {
		t.Errorf("issue que no existe = %d %s", code, b)
	}
}

func TestEvents(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "x"})
	ev := storetest.Fixture(t, "backend-fastapi-zerodivision-1")
	now := time.Now().UTC().Truncate(time.Second)
	var want []int64
	for i := range 5 {
		want = append([]int64{e.db.AddEvent(t, p.ID, id, ev, now.Add(time.Duration(i)*time.Minute))}, want...)
	}
	// Dos a la misma hora: el desempate es por id.
	want = append([]int64{e.db.AddEvent(t, p.ID, id, ev, now.Add(4*time.Minute))}, want...)

	var walked []int64
	cursor := ""
	for range 10 {
		var page store.EventPage
		e.get(t, "/api/0/issues/"+itoa(id)+"/events/?limit=4&cursor="+url.QueryEscape(cursor), &page)
		for _, x := range page.Events {
			walked = append(walked, x.ID)
			if x.Message != "ZeroDivisionError: division by zero" || x.Release != "fixture-release-1" || len(x.EventID) != 32 {
				t.Errorf("evento = %+v", x)
			}
		}
		if cursor = page.NextCursor; cursor == "" {
			break
		}
	}
	if !slices.Equal(walked, want) {
		t.Errorf("= %v; quiero %v", walked, want)
	}

	var full store.StoredEvent
	e.get(t, "/api/0/events/"+itoa(want[0])+"/", &full)
	if full.ID != want[0] || full.IssueID != id || full.Event.Request == nil || full.Event.Tags["component"] != "api" ||
		full.Event.Exceptions[0].Frames[0].ContextLine == "" {
		t.Errorf("evento = %+v", full)
	}
	var byEventID store.StoredEvent
	e.get(t, "/api/0/events/"+full.EventID+"/", &byEventID)
	if byEventID.ID != full.ID {
		t.Errorf("por event_id = %d; quiero %d", byEventID.ID, full.ID)
	}
	for _, path := range []string{"/api/0/events/999999/", "/api/0/events/no-es-un-id/", "/api/0/issues/999999/events/"} {
		if code, b := e.do(t, http.MethodGet, path, ""); code != http.StatusNotFound {
			t.Errorf("%s = %d %s", path, code, b)
		}
	}
}

func TestStacktraceBackend(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "ZeroDivisionError: division by zero", Culprit: "app.core.http.middlewares in __call__", Count: 1})
	e.db.AddEvent(t, p.ID, id, storetest.Fixture(t, "backend-fastapi-zerodivision-1"), time.Now())

	md := e.markdown(t, id)
	for _, want := range []string{
		"# ZeroDivisionError: division by zero\n",
		"- Proyecto: backend\n",
		"- Release: fixture-release-1\n",
		"- Entorno: ",
		"- Usuario: (ninguno)\n",
		"- Request: GET http://testserver/__prueba/division\n",
		"- Transaction: /__prueba/division\n",
		"## Etiquetas\n\n- component: api\n",
		"### 1. ZeroDivisionError: division by zero\n\nMecanismo: generic, manejada\n",
		// Fotograma in_app con su contexto y la línea que falló marcada.
		"#### `app/core/http/middlewares.py:41` en `__call__` (módulo `app.core.http.middlewares`) (app)\n\n```\n" +
			"  36 |             if message[\"type\"] == \"http.response.start\":\n",
		"> 41 |             await self.app(scope, receive, _send)\n  42 |         except Exception:\n",
		// El de fuera de la app, en una línea.
		"- `starlette/routing.py:676` en `__call__` (módulo `starlette.routing`)\n",
		"- `fastapi/routing.py:328` en `run_endpoint_function` (módulo `fastapi.routing`)\n",
		// Sin in_app, el fotograma que falló también sale con su código.
		"#### `backend.py:25` en `division` (módulo `__main__`) (app)\n",
		"> 25 |     return {\"media\": 10 / total[\"mensajes\"]}\n",
		"## Breadcrumbs\n\n(ninguno)\n",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("falta %q en\n%s", want, md)
		}
	}
	if strings.Contains(md, "starlette/routing.py:676` en `__call__` (módulo `starlette.routing`) (app)") {
		t.Error("un fotograma de starlette sale como de la app")
	}
}

func TestStacktraceFrontend(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "front")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError: Cannot read properties of undefined (reading 'id')", Culprit: "t.failForFixture"})
	ev := storetest.Fixture(t, "frontend-login-typeerror-1")
	e.db.AddEvent(t, p.ID, id, ev, time.Now())

	md := e.markdown(t, id)
	for _, want := range []string{
		"# TypeError: Cannot read properties of undefined (reading 'id')\n",
		"- Proyecto: front\n",
		"- Release: fixture-release-1\n",
		"- Entorno: production\n",
		"- Request: https://app.example.com/auth/login\n",
		"Mecanismo: auto.function.angular.error_handler, no manejada\n",
		// Sin source maps los fotogramas no traen código: una línea cada uno, en el orden del SDK.
		"- `https://app.example.com/chunk-A1B2C3D4.js:1:8546` en `t.handleSubmit` (app)\n" +
			"- `https://app.example.com/chunk-A1B2C3D4.js:1:11129` en `t.failForFixture` (app)\n",
		"- `https://app.example.com/polyfills-J9K8L7M6.js:1:2410` en `e.runTask` (app)\n",
		`[navigation] {"to":"/auth/login","from":"/auth/login"}`,
	} {
		if !strings.Contains(md, want) {
			t.Errorf("falta %q en\n%s", want, md)
		}
	}

	// Con el contexto que deja INT-247 al traducir el fotograma, sale el código.
	last := &ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1]
	last.Filename, last.Line, last.Column = "src/app/pages/pagina-de-ejemplo/pagina-de-ejemplo.ts", 268, 12
	last.PreContext = []string{"  private failForFixture(data: Datos) {"}
	last.ContextLine = "    return data.cuenta!.id;"
	last.PostContext = []string{"  }"}
	e.db.AddEvent(t, p.ID, id, ev, time.Now().Add(time.Second))
	md = e.markdown(t, id)
	want := "#### `src/app/pages/pagina-de-ejemplo/pagina-de-ejemplo.ts:268:12` en `t.failForFixture` (app)\n\n```\n" +
		"  267 |   private failForFixture(data: Datos) {\n" +
		"> 268 |     return data.cuenta!.id;\n" +
		"  269 |   }\n```\n"
	if !strings.Contains(md, want) {
		t.Errorf("falta %q en\n%s", want, md)
	}
}

func TestStacktraceBreadcrumbs(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "front")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "x"})
	ev := storetest.Fixture(t, "frontend-login-typeerror-1")
	var crumbs []map[string]any
	for i := range 25 {
		crumbs = append(crumbs, map[string]any{"timestamp": 1795090879 + i, "category": "console", "level": "warning", "message": "aviso " + itoa(int64(i))})
	}
	ev.Breadcrumbs, _ = json.Marshal(map[string]any{"values": crumbs})
	e.db.AddEvent(t, p.ID, id, ev, time.Now())
	md := e.markdown(t, id)
	if strings.Contains(md, "aviso 4\n") || !strings.Contains(md, "los 20 últimos") ||
		!strings.Contains(md, "- 2026-11-19T12:21:24.000Z [console] warning aviso 5\n") || !strings.Contains(md, "aviso 24\n") {
		t.Errorf("breadcrumbs:\n%s", md)
	}
}

func (e *env) markdown(t *testing.T, id int64) string {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, e.srv.URL+"/api/0/issues/"+itoa(id)+"/stacktrace.md", nil)
	req.Header.Set("Authorization", "Bearer "+e.token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/markdown") {
		t.Fatalf("= %d %s: %s", res.StatusCode, res.Header.Get("Content-Type"), b)
	}
	return string(b)
}

func TestSetStatus(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "backend")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "x"})
	ev := storetest.Fixture(t, "backend-fastapi-zerodivision-1")
	now := time.Now()
	ev.Release = "r-viejo"
	e.db.AddEvent(t, p.ID, id, ev, now.Add(-time.Hour))
	ev.Release = "r-nuevo"
	e.db.AddEvent(t, p.ID, id, ev, now)
	ev.Release = "r-recibido-tarde"
	e.db.AddEvent(t, p.ID, id, ev, now.Add(-2*time.Hour))

	set := func(status string) (int, store.Issue, string) {
		t.Helper()
		code, b := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(id)+"/status", `{"status": "`+status+`"}`)
		var is store.Issue
		_ = json.Unmarshal(b, &is)
		return code, is, string(b)
	}
	dbState := func() (string, *string) {
		var st string
		var rel *string
		if err := e.db.Pool().QueryRow(context.Background(), `SELECT status, resolved_in_release FROM issues WHERE id = $1`, id).Scan(&st, &rel); err != nil {
			t.Fatal(err)
		}
		return st, rel
	}

	for _, tc := range []struct {
		status  string
		release string
	}{
		{"resolved", ""},
		{"resolved_next", "r-nuevo"},
		{"silenced", ""},
		{"resolved_next", "r-nuevo"},
		{"open", ""},
	} {
		code, is, b := set(tc.status)
		st, rel := dbState()
		got := ""
		if rel != nil {
			got = *rel
		}
		if code != http.StatusOK || is.Status != tc.status || st != tc.status || got != tc.release {
			t.Errorf("%s: %d %s; en la base %s, %q", tc.status, code, b, st, got)
		}
	}

	if code, _, b := set("ignored"); code != http.StatusBadRequest || !strings.Contains(b, `"error"`) {
		t.Errorf("estado no válido = %d %s", code, b)
	}
	if code, b := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(id)+"/status", `no es json`); code != http.StatusBadRequest {
		t.Errorf("cuerpo roto = %d %s", code, b)
	}
	if code, b := e.do(t, http.MethodPost, "/api/0/issues/999999/status", `{"status":"resolved"}`); code != http.StatusNotFound {
		t.Errorf("issue que no existe = %d %s", code, b)
	}

	sinRelease := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "front"})
	ev.Release = ""
	e.db.AddEvent(t, p.ID, sinRelease, ev, now)
	code, b := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(sinRelease)+"/status", `{"status":"resolved_next"}`)
	if code != http.StatusConflict || !strings.Contains(string(b), "release") {
		t.Errorf("resolved_next sin release = %d %s", code, b)
	}
}

func TestAuth(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	_, revoked, err := e.db.CreateToken(ctx, "revocado")
	if err != nil {
		t.Fatal(err)
	}
	if err := e.db.RevokeToken(ctx, "revocado"); err != nil {
		t.Fatal(err)
	}

	paths := []struct{ method, path string }{
		{http.MethodGet, "/api/0/projects/"},
		{http.MethodGet, "/api/0/issues/"},
		{http.MethodGet, "/api/0/issues/1/"},
		{http.MethodGet, "/api/0/issues/1/events/"},
		{http.MethodGet, "/api/0/issues/1/stacktrace.md"},
		{http.MethodPost, "/api/0/issues/1/status"},
		{http.MethodGet, "/api/0/events/1/"},
		{http.MethodPost, "/mcp"},
	}
	for _, tc := range []struct{ name, header string }{
		{"sin token", ""},
		{"desconocido", "Bearer ie_desconocido"},
		{"sin prefijo", "Bearer " + strings.TrimPrefix(e.token, "ie_")},
		{"otro esquema", "Basic " + e.token},
		{"revocado", "Bearer " + revoked},
	} {
		for _, p := range paths {
			req, _ := http.NewRequest(p.method, e.srv.URL+p.path, strings.NewReader(`{"status":"resolved"}`))
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			var body map[string]string
			_ = json.NewDecoder(res.Body).Decode(&body)
			_ = res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized || body["error"] == "" || res.Header.Get("WWW-Authenticate") == "" {
				t.Errorf("%s %s %s = %d %v", tc.name, p.method, p.path, res.StatusCode, body)
			}
		}
	}

	if code, b := e.do(t, http.MethodGet, "/api/0/projects/", ""); code != http.StatusOK {
		t.Fatalf("con token = %d %s", code, b)
	}
	tokens, err := e.db.ListTokens(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tk := range tokens {
		switch tk.Name {
		case "tests":
			if tk.LastUsedAt == nil || tk.RevokedAt != nil {
				t.Errorf("token usado = %+v", tk)
			}
		case "revocado":
			if tk.LastUsedAt != nil || tk.RevokedAt == nil {
				t.Errorf("token revocado = %+v", tk)
			}
		}
	}
}
