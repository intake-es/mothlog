package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type bearer struct{ token string }

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

func connect(t *testing.T, url, token string) *mcp.ClientSession {
	t.Helper()
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   url + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{token}},
		MaxRetries: -1,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func text(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) != 1 {
		t.Fatalf("%d contenidos", len(res.Content))
	}
	tc, ok := res.Content[0].(*mcp.TextContent)
	if !ok {
		t.Fatalf("contenido %T", res.Content[0])
	}
	return tc.Text
}

func call(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) (string, bool) {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return text(t, res), res.IsError
}

func TestMCP(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	back := e.db.AddProject(t, "backend")
	zero := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "ZeroDivisionError: division by zero", Culprit: "app in media", Count: 1})
	e.db.AddEvent(t, back.ID, zero, storetest.Fixture(t, "api-excepcion-1"), time.Now())
	e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "KeyError: 'numero'", Status: "resolved"})

	cs := connect(t, e.srv.URL, e.token)

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
		if !strings.Contains(tool.Description, "Úsala") {
			t.Errorf("%s no dice cuándo usarla: %q", tool.Name, tool.Description)
		}
	}
	slices.Sort(names)
	if want := []string{"add_comment", "get_event", "get_issue", "get_issue_context", "get_issue_tags", "get_release", "get_stacktrace",
		"list_comments", "list_events", "list_issues", "list_releases", "set_issue_status"}; !slices.Equal(names, want) {
		t.Errorf("herramientas = %v", names)
	}

	out, isErr := call(t, cs, "list_issues", map[string]any{})
	var page store.IssuePage
	if err := json.Unmarshal([]byte(out), &page); err != nil || isErr {
		t.Fatalf("list_issues = %s (%v)", out, err)
	}
	if len(page.Issues) != 1 || page.Issues[0].ID != zero || page.Issues[0].LastRelease == nil {
		t.Errorf("list_issues = %+v", page)
	}
	out, _ = call(t, cs, "list_issues", map[string]any{"status": "resolved", "query": "keyerror"})
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != 1 || page.Issues[0].Title != "KeyError: 'numero'" {
		t.Errorf("list_issues resueltos = %s", out)
	}
	if out, isErr := call(t, cs, "list_issues", map[string]any{"status": "cerrado"}); !isErr || !strings.Contains(out, "status") {
		t.Errorf("status no válido = %v %s", isErr, out)
	}

	md, isErr := call(t, cs, "get_stacktrace", map[string]any{"issue_id": zero})
	if isErr || !strings.HasPrefix(md, "# ZeroDivisionError: division by zero\n") ||
		!strings.Contains(md, `> 73 |     return {"pedido": pedido_id, "media": lineas["unidades"] / lineas["envios"]}`) {
		t.Errorf("get_stacktrace =\n%s", md)
	}
	if out, isErr := call(t, cs, "get_stacktrace", map[string]any{"issue_id": 999999}); !isErr || !strings.Contains(out, "no existe") {
		t.Errorf("issue que no existe = %v %s", isErr, out)
	}

	out, isErr = call(t, cs, "get_issue", map[string]any{"issue_id": zero})
	var d store.IssueDetail
	if err := json.Unmarshal([]byte(out), &d); err != nil || isErr || len(d.Daily) != store.DailyDays || d.LatestEvent == nil {
		t.Errorf("get_issue = %s", out)
	}
	out, isErr = call(t, cs, "list_events", map[string]any{"issue_id": zero})
	var events store.EventPage
	if err := json.Unmarshal([]byte(out), &events); err != nil || isErr || len(events.Events) != 1 {
		t.Errorf("list_events = %s", out)
	}

	env := storetest.Fixture(t, "api-excepcion-1").Environment
	for _, tc := range []struct {
		env  string
		want int
	}{{env, 1}, {"otro", 0}} {
		out, _ = call(t, cs, "list_issues", map[string]any{"environment": tc.env})
		if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != tc.want {
			t.Errorf("list_issues en %q = %s", tc.env, out)
		}
		out, _ = call(t, cs, "list_events", map[string]any{"issue_id": zero, "environment": tc.env})
		if err := json.Unmarshal([]byte(out), &events); err != nil || len(events.Events) != tc.want {
			t.Errorf("list_events en %q = %s", tc.env, out)
		}
		out, _ = call(t, cs, "get_issue", map[string]any{"issue_id": zero, "environment": tc.env})
		if err := json.Unmarshal([]byte(out), &d); err != nil || d.Count != int64(tc.want) {
			t.Errorf("get_issue en %q = %s", tc.env, out)
		}
	}

	out, isErr = call(t, cs, "set_issue_status", map[string]any{"issue_id": zero, "status": "resolved_next"})
	var is store.Issue
	if err := json.Unmarshal([]byte(out), &is); err != nil || isErr || is.Status != "resolved_next" ||
		is.ResolvedInRelease == nil || *is.ResolvedInRelease != "fixture-release-1" {
		t.Errorf("set_issue_status = %s", out)
	}
	got, err := e.db.Issue(ctx, zero)
	if err != nil || got.Status != "resolved_next" {
		t.Errorf("en la base = %+v, %v", got, err)
	}
	out, _ = call(t, cs, "list_issues", map[string]any{})
	if err := json.Unmarshal([]byte(out), &page); err != nil || len(page.Issues) != 0 {
		t.Errorf("tras resolver, abiertos = %s", out)
	}
}

func TestMCPUnauthorized(t *testing.T) {
	e := setup(t)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	_, err := client.Connect(context.Background(), &mcp.StreamableClientTransport{
		Endpoint:   e.srv.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer{"ie_malo"}},
		MaxRetries: -1,
	}, nil)
	if err == nil {
		t.Fatal("conecta con un token malo")
	}
}
