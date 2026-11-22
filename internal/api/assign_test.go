package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

func TestAssignAPI(t *testing.T) {
	e := setupWithSession(t)
	ctx := context.Background()
	p := e.db.AddProject(t, "web")
	issue := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError"})
	other := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "Timeout"})
	if err := e.db.SavePerson(ctx, store.Person{Login: "ana", Name: "Ana"}); err != nil {
		t.Fatal(err)
	}
	path := "/api/0/issues/" + itoa(issue) + "/assignee"

	var list struct {
		Data []store.Assignee `json:"data"`
	}
	e.get(t, "/api/0/assignees/", &list)
	if len(list.Data) != 2 || list.Data[0].Name != "ana" || list.Data[0].Kind != "user" ||
		list.Data[1].Name != "agente" || list.Data[1].Kind != "token" {
		t.Fatalf("asignables = %+v", list.Data)
	}

	// Una persona se lo asigna a sí misma con me; no constaba en people y queda guardada.
	code, b := e.asUser(t, "luis", http.MethodPost, path, `{"assignee": "me"}`)
	var is store.Issue
	if code != http.StatusOK || json.Unmarshal(b, &is) != nil || is.Assignee == nil ||
		is.Assignee.Name != "luis" || is.Assignee.Kind != "user" {
		t.Fatalf("asignarse = %d %s", code, b)
	}
	e.get(t, "/api/0/assignees/", &list)
	if len(list.Data) != 3 {
		t.Errorf("luis no queda entre los asignables: %+v", list.Data)
	}

	// Con el token, a una persona y luego a sí mismo.
	if code, b := e.do(t, http.MethodPost, path, `{"assignee": "ana"}`); code != http.StatusOK || !strings.Contains(string(b), `"name":"ana"`) {
		t.Errorf("asignar a ana = %d %s", code, b)
	}
	if code, b := e.do(t, http.MethodPost, path, `{"assignee": "me"}`); code != http.StatusOK ||
		!strings.Contains(string(b), `"assignee":{"name":"agente","kind":"token","active":true}`) {
		t.Errorf("el token se asigna = %d %s", code, b)
	}
	if code, b := e.do(t, http.MethodPost, path, `{"assignee": "nadie"}`); code != http.StatusBadRequest {
		t.Errorf("asignar a nadie = %d %s", code, b)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/0/issues/999999/assignee", `{"assignee": "ana"}`); code != http.StatusNotFound {
		t.Errorf("issue que no existe = %d", code)
	}
	if code, _ := e.do(t, http.MethodPost, path, `nada`); code != http.StatusBadRequest {
		t.Errorf("cuerpo roto = %d", code)
	}

	// La lista: assigned:me es distinto para cada uno.
	var page store.IssuePage
	e.get(t, "/api/0/issues/?query="+url.QueryEscape("assigned:me"), &page)
	if len(page.Issues) != 1 || page.Issues[0].ID != issue || page.Issues[0].Assignee.Name != "agente" {
		t.Errorf("assigned:me del token = %+v", page.Issues)
	}
	code, b = e.asUser(t, "luis", http.MethodGet, "/api/0/issues/?query="+url.QueryEscape("assigned:me"), "")
	if code != http.StatusOK || !strings.Contains(string(b), `"data":[]`) {
		t.Errorf("assigned:me de luis = %d %s", code, b)
	}
	e.get(t, "/api/0/issues/?query="+url.QueryEscape("!assigned:me"), &page)
	if len(page.Issues) != 1 || page.Issues[0].ID != other || page.Issues[0].Assignee != nil {
		t.Errorf("!assigned:me = %+v", page.Issues)
	}

	// Desasignar, con DELETE o con assignee null.
	if code, b := e.asUser(t, "luis", http.MethodDelete, path, ""); code != http.StatusOK || !strings.Contains(string(b), `"assignee":null`) {
		t.Errorf("desasignar = %d %s", code, b)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(other)+"/assignee", `{"assignee": "ana"}`); code != http.StatusOK {
		t.Fatal(code)
	}
	if code, b := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(other)+"/assignee", `{"assignee": null}`); code != http.StatusOK || !strings.Contains(string(b), `"assignee":null`) {
		t.Errorf("desasignar con null = %d %s", code, b)
	}

	var h struct {
		Data []store.HistoryEntry `json:"data"`
	}
	e.get(t, "/api/0/issues/"+itoa(issue)+"/history/", &h)
	var got []string
	for _, x := range h.Data {
		to := "-"
		if x.Assignee != nil {
			to = x.Assignee.Name
		}
		got = append(got, x.Type+" "+x.Author.Name+" "+x.Source+" "+to)
	}
	want := []string{"assignment luis ui luis", "assignment agente api ana", "assignment agente api agente", "assignment luis ui -"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("historial = %v", got)
	}

	md := e.text(t, "/api/0/issues/"+itoa(other)+"/context.md")
	wantAll(t, "contexto", md, "- Asignado: (nadie)\n", "agente (token) desde la API: asignó a ana\n",
		"agente (token) desde la API: desasignó a ana\n", "`assign_issue` con `{\"issue_id\": "+itoa(other)+", \"assignee\": \"me\"}`")
}

// Un agente se asigna un error por el MCP con me y lo encuentra con assigned:me.
func TestMCPAssignMe(t *testing.T) {
	e := setupWithSession(t)
	p := e.db.AddProject(t, "web")
	issue := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError: x is undefined"})
	e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "Timeout"})
	cs := connect(t, e.srv.URL, e.token)

	out, isErr := call(t, cs, "list_issues", map[string]any{"query": "assigned:me"})
	if isErr || !strings.Contains(out, `"data": []`) {
		t.Fatalf("antes, assigned:me = %s", out)
	}
	out, isErr = call(t, cs, "assign_issue", map[string]any{"issue_id": issue, "assignee": "me"})
	var is store.Issue
	if isErr || json.Unmarshal([]byte(out), &is) != nil || is.Assignee == nil ||
		*is.Assignee != (store.Assignee{Name: "agente", Kind: "token", Active: true}) {
		t.Fatalf("assign_issue = %s", out)
	}
	out, _ = call(t, cs, "list_issues", map[string]any{"query": "assigned:me"})
	var page store.IssuePage
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != 1 || page.Issues[0].ID != issue {
		t.Fatalf("assigned:me = %s", out)
	}
	out, _ = call(t, cs, "list_issues", map[string]any{"query": "!assigned:me"})
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != 1 || page.Issues[0].Title != "Timeout" {
		t.Errorf("!assigned:me = %s", out)
	}
	out, _ = call(t, cs, "get_issue", map[string]any{"issue_id": issue})
	if !strings.Contains(out, `"assignee": {`) || !strings.Contains(out, `"name": "agente"`) {
		t.Errorf("get_issue sin assignee = %s", out)
	}
	md, _ := call(t, cs, "get_issue_context", map[string]any{"issue_id": issue})
	wantAll(t, "contexto", md, "- Asignado: agente (token)\n", "agente (token) desde el MCP: asignó a agente (token)\n")

	out, isErr = call(t, cs, "list_assignees", map[string]any{})
	if isErr || !strings.Contains(out, `"name": "agente"`) {
		t.Errorf("list_assignees = %s", out)
	}
	if out, isErr := call(t, cs, "assign_issue", map[string]any{"issue_id": issue, "assignee": "nadie"}); !isErr || !strings.Contains(out, "nadie") {
		t.Errorf("asignar a nadie = %v %s", isErr, out)
	}
	if out, isErr := call(t, cs, "assign_issue", map[string]any{"issue_id": 999999, "assignee": "me"}); !isErr || !strings.Contains(out, "no existe") {
		t.Errorf("issue que no existe = %v %s", isErr, out)
	}
	out, isErr = call(t, cs, "assign_issue", map[string]any{"issue_id": issue, "assignee": ""})
	if isErr || !strings.Contains(out, `"assignee": null`) {
		t.Errorf("desasignar = %s", out)
	}

	// Revocado el token, la asignación conserva el nombre y se ve inactiva.
	call(t, cs, "assign_issue", map[string]any{"issue_id": issue, "assignee": "me"})
	if err := e.db.RevokeToken(context.Background(), "agente"); err != nil {
		t.Fatal(err)
	}
	code, b := e.asUser(t, "ana", http.MethodGet, "/api/0/issues/"+itoa(issue)+"/", "")
	if code != http.StatusOK || !strings.Contains(string(b), `"assignee":{"name":"agente","kind":"token","active":false}`) {
		t.Errorf("tras revocar = %d %s", code, b)
	}
}
