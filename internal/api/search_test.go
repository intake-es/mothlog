package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

const (
	uaChromeMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	uaSafariMac  = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Safari/605.1.15"
	uaSafariIOS  = "Mozilla/5.0 (iPhone; CPU iPhone OS 17_1 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.1 Mobile/15E148 Safari/604.1"
	uaFirefoxWin = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0"
)

type searchEnv struct {
	*env
	zero, warning, typeErr, valid, keyErr int64
}

// setupSearch manda los fixtures varias veces, con navegadores y usuarios
// distintos, por la ingesta de verdad.
func setupSearch(t *testing.T) searchEnv {
	t.Helper()
	e := setup(t)
	ctx := context.Background()
	api := e.db.AddProject(t, "api")
	web := e.db.AddProject(t, "web")
	ing := ingest.NewIngester(e.db.Pool(), nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	send := func(project int64, fixture string, edit func(*event.Event)) int64 {
		t.Helper()
		ev := storetest.Fixture(t, fixture)
		ev.ID = storetest.RandomHex(t, 16)
		if edit != nil {
			edit(&ev)
		}
		res, err := ing.Ingest(ctx, project, &ev)
		if err != nil {
			t.Fatal(err)
		}
		return res.IssueID
	}
	user := func(u event.User) func(*event.Event) {
		return func(ev *event.Event) { ev.User = &u }
	}
	ua := func(agent string, u *event.User) func(*event.Event) {
		return func(ev *event.Event) {
			ev.Request = &event.Request{URL: "https://app.example.com/", UserAgent: agent}
			ev.User = u
		}
	}
	s := searchEnv{env: e}

	s.zero = send(api.ID, "api-excepcion-1", user(event.User{ID: "u1"}))
	send(api.ID, "api-excepcion-1", user(event.User{ID: "u2"}))
	send(api.ID, "api-excepcion-1", func(ev *event.Event) {
		ev.User = &event.User{ID: "u1"}
		ev.Environment = "staging"
	})
	s.warning = send(api.ID, "api-log-error-1", func(ev *event.Event) { ev.Level = "warning" })

	s.typeErr = send(web.ID, "web-typeerror-1", ua(uaChromeMac, &event.User{Email: "ana@example.com"}))
	send(web.ID, "web-typeerror-1", ua(uaSafariMac, &event.User{Email: "luis@example.com"}))
	send(web.ID, "web-typeerror-1", ua(uaSafariIOS, &event.User{Email: "luis@example.com"}))
	send(web.ID, "web-typeerror-1", ua(uaFirefoxWin, &event.User{IPAddress: "203.0.113.9"}))

	s.valid = send(api.ID, "api-validacion-1", nil)
	if _, err := e.db.SetIssueStatus(ctx, s.valid, "resolved"); err != nil {
		t.Fatal(err)
	}
	s.keyErr = send(api.ID, "api-log-exception-1", nil)
	if _, err := e.db.SetIssueStatus(ctx, s.keyErr, "resolved"); err != nil {
		t.Fatal(err)
	}
	send(api.ID, "api-log-exception-1", nil)
	return s
}

func sorted(ids []int64) []int64 {
	out := slices.Clone(ids)
	slices.Sort(out)
	return out
}

func TestSearchExamples(t *testing.T) {
	s := setupSearch(t)
	for _, tc := range []struct {
		params string
		want   []int64
	}{
		{"", []int64{s.zero, s.warning, s.typeErr, s.keyErr}},
		{"query=is:resolved", []int64{s.valid}},
		{"query=is:regressed", []int64{s.keyErr}},
		{"query=!is:open", []int64{s.valid}},
		{"query=is:open is:resolved", []int64{}},
		{"query=browser:Safari", []int64{s.typeErr}},
		{`query=os:"Mac OS"`, []int64{s.typeErr}},
		{`query=os:"Mac"`, []int64{}},
		{"query=browser:safari", []int64{}},
		{"query=device:iPhone", []int64{s.typeErr}},
		{"query=!browser:Chrome", []int64{s.zero, s.warning, s.keyErr}},
		{"query=release:fixture-release-1 environment:staging", []int64{s.zero}},
		{"query=!environment:staging", []int64{s.warning, s.typeErr, s.keyErr}},
		{"query=level:warning", []int64{s.warning}},
		{"query=project:web", []int64{s.typeErr}},
		{"query=project:api is:resolved", []int64{s.valid}},
		{"query=user:u1", []int64{s.zero}},
		{"query=user:luis@example.com", []int64{s.typeErr}},
		{"query=user:LUIS@Example.com", []int64{s.typeErr}},
		{"query=user:203.0.113.9", []int64{s.typeErr}},
		{"query=user:nadie", []int64{}},
		{"query=!user:u2", []int64{s.warning, s.typeErr, s.keyErr}},
		{"query=component:api", []int64{s.zero, s.warning, s.keyErr}},
		{"query=runtime:CPython !environment:staging", []int64{s.warning, s.keyErr}},
		{"query=division zero", []int64{s.zero}},
		{`query="by zero"`, []int64{s.zero}},
		{`query="zero by"`, []int64{}},
		{"query=!division", []int64{s.warning, s.typeErr, s.keyErr}},
		{"query=sincronizar_stock", []int64{s.keyErr}},
		{"query=https://app.example.com/chunk", []int64{s.typeErr}},
		// Un título pegado tal cual es texto, con sus dos puntos.
		{"query=ZeroDivisionError: division by zero", []int64{s.zero}},
		{"query=TypeError: Cannot read properties", []int64{s.typeErr}},
		{"query=TypeError: Cannot browser:Safari", []int64{s.typeErr}},
		// Los parámetros de antes siguen igual.
		{"query=zerodivision", []int64{s.zero}},
		{"status=resolved", []int64{s.valid}},
		{"status=all&query=project:api", []int64{s.zero, s.warning, s.valid, s.keyErr}},
		{"status=resolved&query=is:open", []int64{}},
		{"project=web&query=is:open", []int64{s.typeErr}},
	} {
		t.Run(tc.params, func(t *testing.T) {
			q, _ := url.ParseQuery(tc.params)
			var page store.IssuePage
			s.get(t, "/api/0/issues/?"+q.Encode(), &page)
			if got := sorted(ids(page.Issues)); !slices.Equal(got, sorted(tc.want)) {
				t.Errorf("= %v; quiero %v", got, sorted(tc.want))
			}
		})
	}

	t.Run("usuarios afectados en la lista y el detalle", func(t *testing.T) {
		var page store.IssuePage
		s.get(t, "/api/0/issues/", &page)
		users := map[int64]int64{}
		for _, is := range page.Issues {
			users[is.ID] = is.UserCount
		}
		if users[s.zero] != 2 || users[s.typeErr] != 3 || users[s.warning] != 0 {
			t.Errorf("user_count = %v", users)
		}
		var d store.IssueDetail
		s.get(t, "/api/0/issues/"+itoa(s.typeErr)+"/", &d)
		if d.UserCount != 3 {
			t.Errorf("detalle user_count = %d", d.UserCount)
		}
	})

	for _, tc := range []struct{ query, code, inMsg string }{
		{`os:"Mac OS`, "unclosed_quote", "character 4"},
		{"! browser:Chrome", "lone_negation", "!browser:Chrome"},
		{"is:abierto", "bad_status", "is:abierto does not exist"},
	} {
		t.Run("error de sintaxis "+tc.query, func(t *testing.T) {
			code, b := s.do(t, http.MethodGet, "/api/0/issues/?query="+url.QueryEscape(tc.query), "")
			var body struct {
				Error      string `json:"error"`
				QueryError struct {
					Code     string `json:"code"`
					Position int    `json:"position"`
				} `json:"query_error"`
			}
			if code != http.StatusBadRequest || json.Unmarshal(b, &body) != nil ||
				body.QueryError.Code != tc.code || !strings.Contains(body.Error, tc.inMsg) {
				t.Errorf("= %d %s", code, b)
			}
		})
	}
}

func TestIssueTagsAPI(t *testing.T) {
	s := setupSearch(t)
	var tags store.IssueTags
	s.get(t, "/api/0/issues/"+itoa(s.typeErr)+"/tags/", &tags)
	if tags.Events != 4 || tags.UserCount != 3 {
		t.Errorf("eventos %d, usuarios %d", tags.Events, tags.UserCount)
	}
	byKey := map[string]store.TagDistribution{}
	for _, d := range tags.Tags {
		byKey[d.Key] = d
	}
	b := byKey["browser"]
	if b.Total != 4 || b.Distinct != 4 || len(b.Values) != 4 {
		t.Fatalf("browser = %+v", b)
	}
	for _, v := range b.Values {
		if v.Count != 1 || v.Percent != 25 {
			t.Errorf("browser %s = %d, %.1f%%", v.Value, v.Count, v.Percent)
		}
	}
	if os := byKey["os"]; len(os.Values) != 3 || os.Values[0].Value != "Mac OS" || os.Values[0].Percent != 50 {
		t.Errorf("os = %+v", os)
	}
	if _, ok := byKey["user"]; ok {
		t.Error("el usuario no va como etiqueta")
	}

	code, _ := s.do(t, http.MethodGet, "/api/0/issues/999999/tags/", "")
	if code != http.StatusNotFound {
		t.Errorf("issue que no existe = %d", code)
	}

	var keys struct{ Data []store.TagKey }
	s.get(t, "/api/0/tags/", &keys)
	var names []string
	for _, k := range keys.Data {
		names = append(names, k.Key)
	}
	if names[0] != "environment" || !slices.Contains(names, "browser") || !slices.Contains(names, "component") {
		t.Errorf("claves = %v", names)
	}
	s.get(t, "/api/0/tags/?project=web", &keys)
	if slices.ContainsFunc(keys.Data, func(k store.TagKey) bool { return k.Key == "component" }) {
		t.Errorf("claves de web = %+v", keys.Data)
	}

	var vals struct{ Data []store.TagValueCount }
	s.get(t, "/api/0/tags/browser/values/?prefix=s", &vals)
	if len(vals.Data) != 1 || vals.Data[0].Value != "Safari" || vals.Data[0].Count != 1 || vals.Data[0].Issues != 1 {
		t.Errorf("valores = %+v", vals.Data)
	}
	s.get(t, "/api/0/tags/os/values/", &vals)
	if len(vals.Data) != 3 || vals.Data[0].Value != "Mac OS" || vals.Data[0].Count != 2 {
		t.Errorf("valores de os = %+v", vals.Data)
	}
	if code, _ := s.do(t, http.MethodGet, "/api/0/tags/no%20vale/values/", ""); code != http.StatusBadRequest {
		t.Errorf("clave no válida = %d", code)
	}
}

func TestMCPSearch(t *testing.T) {
	s := setupSearch(t)
	cs := connect(t, s.srv.URL, s.token)

	out, isErr := call(t, cs, "list_issues", map[string]any{"query": `browser:Safari os:"Mac OS" !user:u1`})
	var page store.IssuePage
	if err := json.Unmarshal([]byte(out), &page); err != nil || isErr || !slices.Equal(ids(page.Issues), []int64{s.typeErr}) {
		t.Errorf("list_issues = %s", out)
	}
	if page.Issues[0].UserCount != 3 {
		t.Errorf("user_count = %d", page.Issues[0].UserCount)
	}
	out, isErr = call(t, cs, "list_issues", map[string]any{"query": "ZeroDivisionError: division by zero"})
	if err := json.Unmarshal([]byte(out), &page); err != nil || isErr || !slices.Equal(ids(page.Issues), []int64{s.zero}) {
		t.Errorf("título pegado = %s", out)
	}
	out, isErr = call(t, cs, "list_issues", map[string]any{"query": "is:regressed"})
	if err := json.Unmarshal([]byte(out), &page); err != nil || isErr || !slices.Equal(ids(page.Issues), []int64{s.keyErr}) {
		t.Errorf("is:regressed = %s", out)
	}
	if out, isErr := call(t, cs, "list_issues", map[string]any{"query": `os:"Mac`}); !isErr || !strings.Contains(out, "closing quotes") {
		t.Errorf("error de sintaxis = %v %s", isErr, out)
	}

	out, isErr = call(t, cs, "get_issue_tags", map[string]any{"issue_id": s.typeErr})
	var tags store.IssueTags
	if err := json.Unmarshal([]byte(out), &tags); err != nil || isErr || tags.UserCount != 3 || len(tags.Tags) == 0 {
		t.Errorf("get_issue_tags = %s", out)
	}

	tools, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "list_issues" {
			for _, w := range []string{"is:open", "browser:Safari", "!browser:Chrome", "user:", "project:"} {
				if !strings.Contains(tool.Description, w) {
					t.Errorf("la descripción de list_issues no explica %s", w)
				}
			}
		}
	}
}
