package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/store"
)

func TestMCPInstructionsAndAnnotations(t *testing.T) {
	e := setup(t)
	cs := connect(t, e.srv.URL, e.token)
	init := cs.InitializeResult()
	for _, want := range []string{"list_issues", "get_issue_context", "add_comment", "set_issue_status", "resolved_next", "is:open environment:production"} {
		if !strings.Contains(init.Instructions, want) {
			t.Errorf("las instrucciones no dicen %q", want)
		}
	}
	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	readOnly := map[string]bool{
		"list_issues": true, "get_issue": true, "get_issue_context": true, "get_issue_tags": true, "get_stacktrace": true,
		"list_events": true, "get_event": true, "list_comments": true, "list_releases": true, "get_release": true, "list_assignees": true,
		"list_monitors": true, "get_monitor": true, "get_digest": true,
	}
	for _, tool := range tools.Tools {
		a := tool.Annotations
		if a == nil {
			t.Errorf("%s sin anotaciones", tool.Name)
			continue
		}
		if a.ReadOnlyHint != readOnly[tool.Name] {
			t.Errorf("%s: readOnlyHint = %v", tool.Name, a.ReadOnlyHint)
		}
		switch tool.Name {
		case "set_issue_status":
			if !a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("set_issue_status: %+v", a)
			}
		case "add_comment":
			if a.IdempotentHint || a.DestructiveHint == nil || *a.DestructiveHint {
				t.Errorf("add_comment: %+v", a)
			}
		}
	}
	for _, name := range []string{"list_issues", "list_releases", "get_issue_context"} {
		for _, tool := range tools.Tools {
			if tool.Name == name && !strings.Contains(string(mustJSON(t, tool.InputSchema)), "environment") {
				t.Errorf("%s no acepta environment", name)
			}
		}
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestMCPNewTools(t *testing.T) {
	e := setupIngest(t)
	web := e.db.AddProject(t, "web")
	putTestSourceMaps(t, e.db, web.ID)
	id := e.send(t, web.ID, "web-typeerror-1", func(ev *event.Event) { ev.User = &event.User{Email: "ana@example.com"} })
	e.send(t, web.ID, "web-typeerror-1", func(ev *event.Event) { ev.Environment = "staging" })
	cs := connect(t, e.srv.URL, e.token)

	md, isErr := call(t, cs, "get_issue_context", map[string]any{"issue_id": id})
	if isErr || !strings.HasPrefix(md, "# TypeError: Cannot read properties of undefined (reading 'reduce')\n") ||
		!strings.Contains(md, ">  7 |   return pedido.lineas!.reduce(") ||
		!strings.Contains(md, "- In the UI: https://errors.example.com/issues/"+strconv.FormatInt(id, 10)) {
		t.Errorf("get_issue_context =\n%s", md)
	}
	md, _ = call(t, cs, "get_issue_context", map[string]any{"issue_id": id, "environment": "staging"})
	if !strings.Contains(md, "- Events in staging: 1;") || !strings.Contains(md, "- Environment: staging") {
		t.Errorf("get_issue_context en staging =\n%s", md)
	}
	if out, isErr := call(t, cs, "get_issue_context", map[string]any{"issue_id": 999999}); !isErr || !strings.Contains(out, "no such issue") {
		t.Errorf("get_issue_context de un issue que no existe = %v %s", isErr, out)
	}

	out, _ := call(t, cs, "list_events", map[string]any{"issue_id": id})
	var events store.EventPage
	if err := json.Unmarshal([]byte(out), &events); err != nil || len(events.Events) != 2 {
		t.Fatalf("list_events = %s", out)
	}
	out, isErr = call(t, cs, "get_event", map[string]any{"event_id": events.Events[1].EventID})
	var ev store.StoredEvent
	if err := json.Unmarshal([]byte(out), &ev); err != nil || isErr || ev.IssueID != id || len(ev.Event.Exceptions) != 1 ||
		len(ev.Event.Breadcrumbs) == 0 {
		t.Errorf("get_event = %s", out)
	}
	if out, isErr := call(t, cs, "get_event", map[string]any{"event_id": "nada"}); !isErr || !strings.Contains(out, "no such event") {
		t.Errorf("get_event que no existe = %v %s", isErr, out)
	}
	md, _ = call(t, cs, "get_issue_context", map[string]any{"issue_id": id, "event": strconv.FormatInt(events.Events[1].ID, 10)})
	if !strings.Contains(md, "## Event (the requested one)") {
		t.Errorf("get_issue_context con event =\n%s", md)
	}

	out, isErr = call(t, cs, "add_comment", map[string]any{"issue_id": id, "body": "La causa es **el pedido sin líneas**."})
	var c store.Comment
	if err := json.Unmarshal([]byte(out), &c); err != nil || isErr || c.Author != (store.Actor{Name: "agente", Kind: "token"}) {
		t.Errorf("add_comment = %s", out)
	}
	if out, isErr := call(t, cs, "add_comment", map[string]any{"issue_id": id, "body": " "}); !isErr || !strings.Contains(out, "empty") {
		t.Errorf("add_comment vacío = %v %s", isErr, out)
	}
	out, isErr = call(t, cs, "set_issue_status", map[string]any{"issue_id": id, "status": "silenced", "comment": "Solo en pruebas"})
	if isErr || !strings.Contains(out, `"status": "silenced"`) {
		t.Errorf("set_issue_status = %s", out)
	}
	out, isErr = call(t, cs, "list_comments", map[string]any{"issue_id": id})
	var h struct {
		Data []store.HistoryEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &h); err != nil || isErr || len(h.Data) != 2 ||
		h.Data[0].Type != "comment" || !h.Data[0].Mine ||
		h.Data[1].Source != "mcp" || h.Data[1].To != "silenced" || h.Data[1].Comment == nil || h.Data[1].Comment.Body != "Solo en pruebas" {
		t.Errorf("list_comments = %s", out)
	}

	out, isErr = call(t, cs, "list_releases", map[string]any{"project": "web"})
	var rels store.ReleasePage
	if err := json.Unmarshal([]byte(out), &rels); err != nil || isErr || len(rels.Releases) != 1 || rels.Releases[0].Version != "fixture-release-1" {
		t.Errorf("list_releases = %s", out)
	}
	out, _ = call(t, cs, "list_releases", map[string]any{"environment": "otro"})
	if err := json.Unmarshal([]byte(out), &rels); err != nil || len(rels.Releases) != 0 {
		t.Errorf("list_releases en otro entorno = %s", out)
	}
	for _, project := range []string{"web", strconv.FormatInt(web.ID, 10)} {
		out, isErr = call(t, cs, "get_release", map[string]any{"project": project, "version": "fixture-release-1"})
		var d store.ReleaseDetail
		if err := json.Unmarshal([]byte(out), &d); err != nil || isErr || len(d.New) != 1 || d.New[0].ID != id {
			t.Errorf("get_release con %s = %s", project, out)
		}
	}
	out, _ = call(t, cs, "get_release", map[string]any{"project": "web", "version": "fixture-release-1", "environment": "staging"})
	var d store.ReleaseDetail
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.Events != 1 {
		t.Errorf("get_release en staging = %s", out)
	}
	if out, isErr := call(t, cs, "get_release", map[string]any{"project": "web", "version": "nada"}); !isErr || !strings.Contains(out, "no such release") {
		t.Errorf("get_release que no existe = %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "get_release", map[string]any{"project": "nadie", "version": "x"}); !isErr || !strings.Contains(out, "no such project") {
		t.Errorf("get_release de un proyecto que no existe = %v %s", isErr, out)
	}
}

// De punta a punta, como lo haría un agente: encuentra el error con una
// búsqueda, lee su contexto, comenta, lo marca resuelto en la siguiente
// release y, tras desplegarla, el historial lo cuenta todo.
func TestMCPAgentFlow(t *testing.T) {
	e := setupIngest(t)
	api := e.db.AddProject(t, "api")
	web := e.db.AddProject(t, "web")
	putTestSourceMaps(t, e.db, web.ID)
	zero := e.send(t, api.ID, "api-excepcion-1", nil)
	e.send(t, api.ID, "api-validacion-1", nil)
	e.send(t, web.ID, "web-typeerror-1", nil)
	cs := connect(t, e.srv.URL, e.token)
	ctx := context.Background()

	out, _ := call(t, cs, "list_issues", map[string]any{"query": `is:open project:api "division by zero"`})
	var page store.IssuePage
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != 1 || page.Issues[0].ID != zero {
		t.Fatalf("list_issues = %s", out)
	}
	found := page.Issues[0]

	md, _ := call(t, cs, "get_issue_context", map[string]any{"issue_id": found.ID})
	wantAll(t, "contexto", md, "## Summary", "## Releases", "## Environments", "## Most frequent tags", "### Exceptions",
		"### Breadcrumbs", "## History\n\n(no comments or status changes)", "## What you can do", "`set_issue_status`")

	if _, isErr := call(t, cs, "add_comment", map[string]any{"issue_id": found.ID,
		"body": "La media divide entre `envios`, que es 0 en los pedidos sin enviar."}); isErr {
		t.Fatal("add_comment falló")
	}
	out, isErr := call(t, cs, "set_issue_status", map[string]any{"issue_id": found.ID, "status": "resolved_next",
		"comment": "Arreglado: `media()` devuelve 0 sin envíos."})
	var is store.Issue
	if err := json.Unmarshal([]byte(out), &is); err != nil || isErr || is.Status != "resolved_next" {
		t.Fatalf("set_issue_status = %s", out)
	}
	// Repetirlo no apunta nada nuevo: es idempotente.
	call(t, cs, "set_issue_status", map[string]any{"issue_id": found.ID, "status": "resolved_next"})

	code, b := e.do(t, http.MethodPost, "/api/0/projects/"+strconv.FormatInt(api.ID, 10)+"/releases/",
		`{"version": "fixture-release-2", "environment": "production"}`)
	if code != http.StatusCreated {
		t.Fatalf("desplegar = %d %s", code, b)
	}

	md, _ = call(t, cs, "get_issue_context", map[string]any{"issue_id": found.ID})
	wantAll(t, "contexto al final", md,
		"- Status: resolved\n",
		"- Resolved in: fixture-release-2\n",
		"agente (token) commented:\n  > La media divide entre `envios`",
		"agente (token) from the MCP: open → resolved_next (release fixture-release-1)\n  > Arreglado: `media()` devuelve 0 sin envíos.\n",
		"Mothlog on deploy: resolved_next → resolved (release fixture-release-2)\n",
	)
	out, _ = call(t, cs, "list_comments", map[string]any{"issue_id": found.ID})
	var h struct {
		Data []store.HistoryEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &h); err != nil || len(h.Data) != 3 {
		t.Fatalf("list_comments = %s", out)
	}
	if h.Data[2].Source != "deploy" || h.Data[2].Author != nil {
		t.Errorf("el despliegue = %+v", h.Data[2])
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_issues", Arguments: map[string]any{"query": "is:resolved"}})
	if err != nil || !strings.Contains(text(t, res), "division by zero") {
		t.Errorf("tras desplegar no sale como resuelto: %v", err)
	}
}
