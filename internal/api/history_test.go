package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/sourcemap"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// sessionAuth hace de la sesión de la interfaz: X-Test-User es el login.
func sessionAuth(r *http.Request) (api.Principal, bool, error) {
	if u := r.Header.Get("X-Test-User"); u != "" {
		return api.Principal{Kind: "session", Name: u}, true, nil
	}
	return api.Principal{}, false, nil
}

// setupWithSession es setup con la sesión de la interfaz además del token.
func setupWithSession(t *testing.T) *env {
	t.Helper()
	db := storetest.NewDB(t)
	mux := http.NewServeMux()
	auth := api.RequireAuth(api.BearerToken(db.Store), sessionAuth)
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com", Auth: auth}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(context.Background(), "agente")
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, srv: srv, token: token}
}

func (e *env) asUser(t *testing.T, user, method, path, body string) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, e.srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test-User", user)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, b
}

func TestCommentsAndHistoryAPI(t *testing.T) {
	e := setupWithSession(t)
	p := e.db.AddProject(t, "backend")
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "ZeroDivisionError: division by zero"})
	e.db.AddEvent(t, p.ID, id, storetest.Fixture(t, "api-excepcion-1"), time.Now())
	base := "/api/0/issues/" + strconv.FormatInt(id, 10)

	code, b := e.do(t, http.MethodPost, base+"/comments/", `{"body": "Pasa con **pedidos sin envíos**."}`)
	if code != http.StatusCreated {
		t.Fatalf("comentar con token = %d %s", code, b)
	}
	var mine store.Comment
	decode(t, b, &mine)
	if mine.Author != (store.Actor{Name: "agente", Kind: "token"}) || !mine.Mine {
		t.Errorf("comentario = %+v", mine)
	}
	code, b = e.asUser(t, "ana", http.MethodPost, base+"/comments/", `{"body": "Lo miro yo"}`)
	if code != http.StatusCreated {
		t.Fatalf("comentar con sesión = %d %s", code, b)
	}
	var anas store.Comment
	decode(t, b, &anas)
	if anas.Author != (store.Actor{Name: "ana", Kind: "user"}) {
		t.Errorf("autor con sesión = %+v", anas.Author)
	}
	for _, body := range []string{`{"body": "  "}`, `no es json`} {
		if code, b := e.do(t, http.MethodPost, base+"/comments/", body); code != http.StatusBadRequest {
			t.Errorf("comentar %s = %d %s", body, code, b)
		}
	}
	if code, _ := e.do(t, http.MethodPost, "/api/0/issues/999999/comments/", `{"body": "x"}`); code != http.StatusNotFound {
		t.Errorf("comentar un issue que no existe = %d", code)
	}

	// La interfaz cambia el estado; el token, con un comentario unido al cambio.
	if code, b := e.asUser(t, "ana", http.MethodPost, base+"/status", `{"status": "silenced"}`); code != http.StatusOK {
		t.Fatalf("silenciar = %d %s", code, b)
	}
	if code, b := e.do(t, http.MethodPost, base+"/status", `{"status": "resolved_next", "comment": "Arreglado en `+"`media()`"+`"}`); code != http.StatusOK {
		t.Fatalf("resolved_next = %d %s", code, b)
	}

	var h struct {
		Data []store.HistoryEntry `json:"data"`
	}
	e.get(t, base+"/history/", &h)
	if len(h.Data) != 4 {
		t.Fatalf("historial = %+v", h.Data)
	}
	if c := h.Data[0]; c.Type != "comment" || c.Body != "Pasa con **pedidos sin envíos**." || !c.Mine {
		t.Errorf("0 = %+v", c)
	}
	if c := h.Data[1]; c.Type != "comment" || c.Mine || c.Author.Name != "ana" {
		t.Errorf("1 = %+v", c)
	}
	if c := h.Data[2]; c.Type != "status" || c.Source != "ui" || c.From != "open" || c.To != "silenced" || c.Author.Name != "ana" || c.Author.Kind != "user" {
		t.Errorf("2 = %+v", c)
	}
	if c := h.Data[3]; c.Type != "status" || c.Source != "api" || c.To != "resolved_next" || c.Release == nil || *c.Release != "fixture-release-1" ||
		c.Comment == nil || c.Comment.Body != "Arreglado en `media()`" || !c.Comment.Mine {
		t.Errorf("3 = %+v", c)
	}

	// Solo se borra lo propio.
	path := base + "/comments/" + strconv.FormatInt(anas.ID, 10)
	if code, b := e.do(t, http.MethodDelete, path, ""); code != http.StatusForbidden || !strings.Contains(string(b), "tus comentarios") {
		t.Errorf("borrar el de otro = %d %s", code, b)
	}
	if code, _ := e.asUser(t, "ana", http.MethodDelete, path, ""); code != http.StatusNoContent {
		t.Errorf("borrar el propio = %d", code)
	}
	if code, _ := e.asUser(t, "ana", http.MethodDelete, path, ""); code != http.StatusNotFound {
		t.Errorf("borrarlo otra vez = %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, base+"/comments/"+strconv.FormatInt(h.Data[3].Comment.ID, 10), ""); code != http.StatusNoContent {
		t.Errorf("borrar el comentario de un cambio = %d", code)
	}
	e.get(t, base+"/history/", &h)
	if len(h.Data) != 3 || h.Data[2].Type != "status" || h.Data[2].Comment != nil {
		t.Errorf("tras borrar = %+v", h.Data)
	}
}

// ingestEnv manda fixtures por la ingesta de verdad, con los source maps de
// testdata/sourcemaps para el proyecto web.
type ingestEnv struct {
	*env
	ing *ingest.Ingester
}

func setupIngest(t *testing.T) ingestEnv {
	t.Helper()
	e := setupWithSession(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	maps := sourcemap.New(e.db.Store, log, sourcemap.Options{})
	return ingestEnv{env: e, ing: ingest.NewIngester(e.db.Pool(), maps, nil, log)}
}

func (e ingestEnv) send(t *testing.T, project int64, fixture string, edit func(*event.Event)) int64 {
	t.Helper()
	ev := storetest.Fixture(t, fixture)
	ev.ID = storetest.RandomHex(t, 16)
	if edit != nil {
		edit(&ev)
	}
	res, err := e.ing.Ingest(context.Background(), project, &ev)
	if err != nil {
		t.Fatal(err)
	}
	return res.IssueID
}

func putTestSourceMaps(t *testing.T, db *storetest.DB, project int64) {
	t.Helper()
	var files []store.SourceMapFile
	for _, js := range []string{"chunk-Z554BZQ6.js", "main-3TO43VUM.js"} {
		b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "sourcemaps", js+".map"))
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, store.SourceMapFile{File: js, Map: b})
	}
	if err := db.PutSourceMaps(context.Background(), project, "fixture-release-1", files); err != nil {
		t.Fatal(err)
	}
}

func (e *env) text(t *testing.T, path string) string {
	t.Helper()
	code, b := e.do(t, http.MethodGet, path, "")
	if code != http.StatusOK {
		t.Fatalf("GET %s = %d %s", path, code, b)
	}
	return string(b)
}

func wantAll(t *testing.T, what, doc string, parts ...string) {
	t.Helper()
	failed := false
	for _, p := range parts {
		if !strings.Contains(doc, p) {
			t.Errorf("%s no lleva %q", what, p)
			failed = true
		}
	}
	if failed {
		t.Logf("%s:\n%s", what, doc)
	}
}

func TestContextMarkdownPython(t *testing.T) {
	e := setupIngest(t)
	p := e.db.AddProject(t, "backend")
	id := e.send(t, p.ID, "api-excepcion-1", func(ev *event.Event) { ev.User = &event.User{ID: "u1"} })
	e.send(t, p.ID, "api-excepcion-1", func(ev *event.Event) { ev.User = &event.User{ID: "u2"} })
	staging := e.send(t, p.ID, "api-excepcion-1", func(ev *event.Event) {
		ev.Environment = "staging"
		ev.Release = "fixture-release-2"
		ev.User = &event.User{ID: "u3"}
	})
	if staging != id {
		t.Fatalf("el de staging fue a otro issue")
	}
	base := "/api/0/issues/" + strconv.FormatInt(id, 10)
	if code, b := e.do(t, http.MethodPost, base+"/comments/", `{"body": "Mirado:\n\n## Causa\n\nLíneas sin envíos."}`); code != http.StatusCreated {
		t.Fatalf("comentar = %d %s", code, b)
	}
	if code, b := e.asUser(t, "ana", http.MethodPost, base+"/status", `{"status": "silenced", "comment": "Ruido de pruebas"}`); code != http.StatusOK {
		t.Fatalf("silenciar = %d %s", code, b)
	}

	md := e.text(t, base+"/context.md")
	wantAll(t, "context.md", md,
		"# ZeroDivisionError: division by zero\n",
		"- Estado: silenced\n",
		"- Nivel: error\n",
		"- Proyecto: backend\n",
		"- Eventos: 3; usuarios afectados: 3\n",
		"- En la interfaz: https://errors.example.com/issues/"+strconv.FormatInt(id, 10)+"\n",
		"## Releases\n\n- Primera: fixture-release-1\n- Última: fixture-release-2\n- Resuelto con: (ninguna)\n",
		"## Entornos\n\n- production: 2 eventos\n- staging: 1 evento\n",
		"## Etiquetas más frecuentes",
		"- release: fixture-release-1 66.7 % (2), fixture-release-2 33.3 % (1)",
		"- environment: production 66.7 % (2), staging 33.3 % (1)",
		"## Evento (el último)",
		"- Release: fixture-release-2",
		`> 73 |     return {"pedido": pedido_id, "media": lineas["unidades"] / lineas["envios"]}`,
		"### Breadcrumbs",
		"## Historial",
		"agente (token) comentó:\n  > Mirado:\n  >\n  > ## Causa\n",
		"ana desde la interfaz: open → silenced\n  > Ruido de pruebas\n",
		"## Qué puedes hacer",
		"`set_issue_status` con `{\"issue_id\": "+strconv.FormatInt(id, 10)+", \"status\": \"resolved_next\"",
		"`add_comment`",
	)
	if strings.Contains(md, "Recortado") {
		t.Error("lo recorta sin pasar del tamaño")
	}

	// Con entorno, los recuentos y el último evento son los suyos.
	md = e.text(t, base+"/context.md?environment=production")
	wantAll(t, "context.md en production", md, "- Eventos en production: 2;", "## Evento (el último en production)", "- Release: fixture-release-1")

	// Con event, ese evento.
	var events store.EventPage
	e.get(t, base+"/events/", &events)
	oldest := events.Events[len(events.Events)-1]
	md = e.text(t, base+"/context.md?event="+oldest.EventID)
	wantAll(t, "context.md con event", md, "## Evento (el pedido)", "- Id: "+oldest.EventID)
	other := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "otro"})
	if code, _ := e.do(t, http.MethodGet, "/api/0/issues/"+strconv.FormatInt(other, 10)+"/context.md?event="+oldest.EventID, ""); code != http.StatusNotFound {
		t.Errorf("event de otro issue = %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/issues/999999/context.md", ""); code != http.StatusNotFound {
		t.Errorf("issue que no existe = %d", code)
	}

	// stacktrace.md sigue igual.
	st := e.text(t, base+"/stacktrace.md")
	if !strings.HasPrefix(st, "# ZeroDivisionError: division by zero\n\n- Issue: ") || strings.Contains(st, "## Historial") {
		t.Errorf("stacktrace.md =\n%s", st)
	}
}

func TestContextMarkdownBrowser(t *testing.T) {
	e := setupIngest(t)
	web := e.db.AddProject(t, "web")
	putTestSourceMaps(t, e.db, web.ID)
	id := e.send(t, web.ID, "web-typeerror-1", func(ev *event.Event) {
		ev.Request = &event.Request{URL: "https://app.example.com/pedidos", UserAgent: uaSafariMac}
		ev.User = &event.User{Email: "ana@example.com"}
	})
	e.send(t, web.ID, "web-typeerror-1", func(ev *event.Event) {
		ev.Request = &event.Request{URL: "https://app.example.com/pedidos", UserAgent: uaChromeMac}
	})
	if _, err := e.db.ChangeIssueStatus(context.Background(), id, "resolved_next",
		store.StatusChange{Actor: &store.Actor{Name: "agente", Kind: "token"}, Source: store.SourceMCP, Comment: "Arreglado en `src/carrito.ts`"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.db.AddDeploy(context.Background(), web.ID, "fixture-release-2", "production", time.Now()); err != nil {
		t.Fatal(err)
	}

	md := e.text(t, "/api/0/issues/"+strconv.FormatInt(id, 10)+"/context.md")
	wantAll(t, "context.md del navegador", md,
		"# TypeError: Cannot read properties of undefined (reading 'reduce')\n",
		"- Estado: resolved\n",
		"- Resuelto con: fixture-release-2\n",
		"- browser: Chrome 50 % (1), Safari 50 % (1)",
		"##### `src/carrito.ts:7",
		">  7 |   return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);",
		"- Request: https://app.example.com/pedidos",
		"### Breadcrumbs\n\nDel más viejo al más nuevo",
		"agente (token) desde el MCP: open → resolved_next (release fixture-release-1)\n  > Arreglado en `src/carrito.ts`\n",
		"intake-errors al desplegar: resolved_next → resolved (release fixture-release-2)\n",
	)
}

// Un issue con muchos breadcrumbs y comentarios largos pasa del tamaño: se
// recorta y lo dice.
func TestContextMarkdownTrimmed(t *testing.T) {
	e := setupIngest(t)
	web := e.db.AddProject(t, "web")
	putTestSourceMaps(t, e.db, web.ID)
	id := e.send(t, web.ID, "web-typeerror-1", func(ev *event.Event) {
		var crumbs []map[string]any
		for i := range 100 {
			crumbs = append(crumbs, map[string]any{"timestamp": 1795090879 + i, "category": "console", "message": strings.Repeat("ruido ", 40) + strconv.Itoa(i)})
		}
		ev.Breadcrumbs, _ = json.Marshal(crumbs)
	})
	base := "/api/0/issues/" + strconv.FormatInt(id, 10)
	for i := range 40 {
		body, _ := json.Marshal(map[string]string{"body": "Comentario " + strconv.Itoa(i) + ": " + strings.Repeat("texto largo ", 300)})
		if code, b := e.do(t, http.MethodPost, base+"/comments/", string(body)); code != http.StatusCreated {
			t.Fatalf("comentar = %d %s", code, b)
		}
	}
	md := e.text(t, base+"/context.md")
	if len(md) > 64<<10 {
		t.Errorf("ocupa %d bytes", len(md))
	}
	wantAll(t, "context.md recortado", md,
		"> Recortado para no pasar de 64 KB: breadcrumbs, fotogramas de librerías, historial viejo.",
		">  7 |   return pedido.lineas!.reduce(",
		"(100, quitados por el tamaño)",
		"entradas más viejas, quitadas por el tamaño)",
		"Comentario 39: ",
		"## Qué puedes hacer",
	)
	if strings.Contains(md, "Comentario 0: ") {
		t.Error("deja los comentarios más viejos")
	}
}

func TestLLMsTxt(t *testing.T) {
	e := setup(t)
	res, err := http.Get(e.srv.URL + "/llms.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("llms.txt = %d %s", res.StatusCode, res.Header.Get("Content-Type"))
	}
	wantAll(t, "llms.txt", string(b),
		"# intake-errors\n\n> ",
		"https://errors.example.com/mcp",
		"`get_issue_context`",
		"https://errors.example.com/api/0/issues/{id}/context.md",
		"docs/agentes.md",
	)
}
