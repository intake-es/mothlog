package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
	"github.com/intake-es/intake-errors/internal/webhooks"
	"github.com/intake-es/intake-errors/internal/webhooks/fakes"
)

const (
	linearKey     = "lin_api_prueba"
	linearSecret  = "lin_wh_secreto"
	githubToken   = "ghp_global"
	ruleToken     = "ghp_de_la_regla"
	githubSecret  = "gh_wh_secreto"
	repo          = "ejemplo/backend"
	publicURLBase = "https://errores.ejemplo.test"
)

type world struct {
	db      *storetest.DB
	log     *fakes.Log
	linear  *fakes.Linear
	gh      *fakes.GitHub
	alerter *alert.Alerter
	ing     *ingest.Ingester
	hooks   *httptest.Server
	project int64
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{db: storetest.NewDB(t), log: &fakes.Log{}}
	w.linear = fakes.NewLinear(w.log, linearKey, linearSecret)
	w.gh = fakes.NewGitHub(w.log, githubSecret, map[string]string{githubToken: "bot-global", ruleToken: "bot-de-la-regla"})
	linSrv := httptest.NewServer(w.linear.Handler())
	t.Cleanup(linSrv.Close)
	ghSrv := httptest.NewServer(w.gh.Handler())
	t.Cleanup(ghSrv.Close)

	a, err := alert.New(w.db.Store, quiet(), alert.Options{
		PublicURL:    publicURLBase,
		LinearAPIKey: linearKey, LinearAPIURL: linSrv.URL, LinearTeamID: fakes.LinearTeamID,
		GitHubToken: githubToken, GitHubAPIURL: ghSrv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	w.alerter = a
	w.ing = ingest.NewIngester(w.db.Pool(), nil, a, quiet())
	h := webhooks.New(w.db.Store, webhooks.Options{LinearSecret: linearSecret, GitHubSecret: githubSecret,
		PublicURL: publicURLBase, Log: quiet()})
	mux := http.NewServeMux()
	h.Register(mux)
	w.hooks = httptest.NewServer(mux)
	t.Cleanup(w.hooks.Close)
	w.linear.WebhookURL = w.hooks.URL + webhooks.LinearPath
	w.gh.WebhookURL = w.hooks.URL + webhooks.GitHubPath
	w.project = w.db.AddProject(t, "backend").ID
	if out := os.Getenv("WEBHOOKS_TEST_LOG"); out != "" {
		f, err := os.Create(out)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = f.Close() })
		w.log.Out = f
	}
	return w
}

// settle envía avisos y cambios de estado y entrega los webhooks de los dos
// lados hasta que no queda nada; si no se para, hay un bucle.
func (w *world) settle(t *testing.T) {
	t.Helper()
	ctx := context.Background()
	for round := 0; round < 10; round++ {
		n1, err := w.alerter.SendPending(ctx)
		if err != nil {
			t.Fatal(err)
		}
		n2, err := w.alerter.SyncLinks(ctx)
		if err != nil {
			t.Fatal(err)
		}
		c1, err := w.linear.Flush()
		if err != nil {
			t.Fatal(err)
		}
		c2, err := w.gh.Flush()
		if err != nil {
			t.Fatal(err)
		}
		for _, c := range append(c1, c2...) {
			if c != http.StatusOK {
				t.Fatalf("un webhook ha respondido %d", c)
			}
		}
		if n1+n2+len(c1)+len(c2) == 0 {
			return
		}
	}
	t.Fatal("tras 10 vueltas sigue habiendo cambios: hay un bucle")
}

func (w *world) newIssue(t *testing.T, fixture, release string) int64 {
	t.Helper()
	ev := storetest.Fixture(t, fixture)
	ev.ID = ""
	ev.Release = release
	res, err := w.ing.Ingest(context.Background(), w.project, &ev)
	if err != nil {
		t.Fatal(err)
	}
	if res.Outcome != ingest.New {
		t.Fatalf("%s: %v", fixture, res.Outcome)
	}
	return res.IssueID
}

func (w *world) status(t *testing.T, id int64) string {
	t.Helper()
	is, err := w.db.Issue(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return is.Status
}

func (w *world) statusChanges(t *testing.T, id int64) []store.HistoryEntry {
	t.Helper()
	h, err := w.db.History(context.Background(), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []store.HistoryEntry
	for _, e := range h {
		if e.Type == "status" {
			out = append(out, e)
		}
	}
	return out
}

func (w *world) linkOf(t *testing.T, id int64, provider, kind string) store.IssueLink {
	t.Helper()
	links, err := w.db.IssueLinks(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range links {
		if l.Provider == provider && l.Kind == kind {
			return l
		}
	}
	t.Fatalf("issue %d sin enlace %s/%s: %+v", id, provider, kind, links)
	return store.IssueLink{}
}

// requests cuenta las peticiones que ha recibido el servidor desde from.
func (w *world) requests(server string, from int) int {
	n := 0
	for _, e := range w.log.Entries()[from:] {
		if e.Server == server && e.Dir == "recibe" {
			n++
		}
	}
	return n
}

// mutations cuenta las peticiones que cambian algo y ha recibido el servidor desde from.
func (w *world) mutations(server string, from int) int {
	n := 0
	for _, e := range w.log.Entries()[from:] {
		if e.Server != server || e.Dir != "recibe" {
			continue
		}
		body := string(e.Body)
		if e.Method == http.MethodPatch || (e.Method == http.MethodPost && server == "github") ||
			strings.Contains(body, "issueUpdate") || strings.Contains(body, "issueCreate") {
			n++
		}
	}
	return n
}

func (w *world) addGitHubRule(t *testing.T) {
	t.Helper()
	_, _, err := w.db.CreateRule(context.Background(), w.project, store.Rule{
		Name: "Errores nuevos a GitHub y Linear", Enabled: true,
		Conditions: store.RuleConditions{New: true, Regressed: true},
		Actions: []store.RuleAction{
			{Type: store.ActionLinear},
			{Type: store.ActionGitHub, Repo: repo, Labels: []string{"bug", "errores"}, Token: ruleToken},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.alerter.RulesChanged(w.project)
}

func TestRoundTripLinearAndGitHub(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.addGitHubRule(t)

	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)

	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	gh := w.linkOf(t, id, store.ProviderGitHub, store.LinkIssue)
	li, ok := w.linear.Issue(lin.ExternalID)
	if !ok || lin.Identifier != li.Identifier || lin.URL != w.linear.URL(li.Identifier) {
		t.Fatalf("enlace de Linear %+v; issue %+v", lin, li)
	}
	if gh.ExternalID != repo+"#1" || gh.URL != "https://github.com/"+repo+"/issues/1" || gh.State != store.LinkOpen {
		t.Fatalf("enlace de GitHub %+v", gh)
	}
	gi, _ := w.gh.Issue(repo, 1)
	if !strings.Contains(gi.Body, publicURLBase+"/issues/"+strconv.FormatInt(id, 10)) || !strings.Contains(gi.Body, "## Qué puedes hacer") ||
		strings.Join(gi.Labels, ",") != "bug,errores" {
		t.Errorf("issue de GitHub sin el context.md o sin etiquetas: %v\n%s", gi.Labels, gi.Body)
	}
	if len(w.statusChanges(t, id)) != 0 {
		t.Fatal("crear las issues no debería cambiar el estado")
	}

	// 1. Se cierra en Linear: se resuelve aquí y se cierra en GitHub, sin volver a Linear.
	mark := len(w.log.Entries())
	if err := w.linear.SetState(lin.ExternalID, "Done", fakes.LinearActor{ID: "user-ana", Name: "Ana Ruiz"}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras cerrar en Linear: %s", s)
	}
	h := w.statusChanges(t, id)
	if len(h) != 1 || h[0].Source != store.SourceLinear || h[0].Author == nil || h[0].Author.Name != "Ana Ruiz" || h[0].To != "resolved" {
		t.Fatalf("historial: %+v", h)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "closed" || gi.StateReason != "completed" {
		t.Fatalf("GitHub: %s %s", gi.State, gi.StateReason)
	}
	if n := w.requests("linear", mark); n != 0 {
		t.Errorf("el cambio de Linear ha vuelto a Linear: %d peticiones", n)
	}
	if l := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue); l.State != store.LinkClosed || l.StateName != "Done" {
		t.Errorf("estado del enlace de Linear: %+v", l)
	}

	// 2. Se reabre aquí: se reabren las dos, y sus webhooks no apuntan nada.
	if _, err := w.db.ChangeIssueStatus(ctx, id, "open", store.StatusChange{Actor: &store.Actor{Name: "dev", Kind: store.ActorUser}, Source: store.SourceUI}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-todo" {
		t.Fatalf("Linear tras reabrir aquí: %s", li.StateID)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "open" {
		t.Fatalf("GitHub tras reabrir aquí: %s", gi.State)
	}
	if h := w.statusChanges(t, id); len(h) != 2 || h[1].Source != store.SourceUI {
		t.Fatalf("historial: %+v", h)
	}

	// 3. Se silencia aquí: cancelada en Linear, cerrada como no planeada en GitHub.
	if _, err := w.db.ChangeIssueStatus(ctx, id, "silenced", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-canceled" {
		t.Fatalf("Linear tras silenciar: %s", li.StateID)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "closed" || gi.StateReason != "not_planned" {
		t.Fatalf("GitHub tras silenciar: %s %s", gi.State, gi.StateReason)
	}
	if s := w.status(t, id); s != "silenced" || len(w.statusChanges(t, id)) != 3 {
		t.Fatalf("tras silenciar: %s, %+v", s, w.statusChanges(t, id))
	}

	// 4. Se reabre en GitHub: se reabre aquí y en Linear, sin volver a GitHub.
	mark = len(w.log.Entries())
	if err := w.gh.Reopen(repo, 1, "luis"); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "open" {
		t.Fatalf("tras reabrir en GitHub: %s", s)
	}
	h = w.statusChanges(t, id)
	if last := h[len(h)-1]; len(h) != 4 || last.Source != store.SourceGitHub || last.Author == nil || last.Author.Name != "luis" {
		t.Fatalf("historial: %+v", h)
	}
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-todo" {
		t.Fatalf("Linear tras reabrir en GitHub: %s", li.StateID)
	}
	if n := w.requests("github", mark); n != 0 {
		t.Errorf("el cambio de GitHub ha vuelto a GitHub: %d peticiones", n)
	}

	// 5. Se cierra en GitHub: resuelto aquí y en Linear, con una sola escritura
	// en Linear, y el webhook con el que Linear lo cuenta no apunta nada.
	mark = len(w.log.Entries())
	if err := w.gh.Close(repo, 1, "completed", "luis"); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras cerrar en GitHub: %s", s)
	}
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-done" {
		t.Fatalf("Linear tras cerrar en GitHub: %s", li.StateID)
	}
	if n, m := w.mutations("linear", mark), w.requests("github", mark); n != 1 || m != 0 {
		t.Errorf("escrituras en Linear = %d (quiero 1); peticiones a GitHub = %d (quiero 0)", n, m)
	}
	if h := w.statusChanges(t, id); len(h) != 5 || h[4].Source != store.SourceGitHub {
		t.Fatalf("historial: %+v", h)
	}

	// 7. Reabierto aquí, pasarla en Linear de Todo a In Progress no cambia nada.
	if _, err := w.db.ChangeIssueStatus(ctx, id, "open", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if err := w.linear.SetState(lin.ExternalID, "In Progress", fakes.LinearActor{ID: "user-ana", Name: "Ana Ruiz"}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "open" || len(w.statusChanges(t, id)) != 6 {
		t.Fatalf("pasar a In Progress ha cambiado algo: %s %+v", s, w.statusChanges(t, id))
	}
}

func TestRegressionReopensLinkedIssues(t *testing.T) {
	w := newWorld(t)
	ctx := context.Background()
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "1.0.0")
	w.settle(t)
	if _, err := w.db.ChangeIssueStatus(ctx, id, "resolved", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-done" {
		t.Fatalf("Linear tras resolver: %s", li.StateID)
	}
	ev := storetest.Fixture(t, "api-tarea-a-1")
	ev.ID, ev.Release = "", "1.1.0"
	res, err := w.ing.Ingest(ctx, w.project, &ev)
	if err != nil || res.Outcome != ingest.Regressed {
		t.Fatalf("%v %v", res.Outcome, err)
	}
	w.settle(t)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-todo" {
		t.Fatalf("Linear tras la regresión: %s", li.StateID)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "open" || len(gi.Comments) == 0 {
		t.Fatalf("GitHub tras la regresión: %s, %d comentarios", gi.State, len(gi.Comments))
	}
	if s := w.status(t, id); s != "open" {
		t.Fatalf("estado: %s", s)
	}
}

func TestMergedPullRequestResolvesNext(t *testing.T) {
	w := newWorld(t)
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "2.3.0")
	other := w.newIssue(t, "api-excepcion-1", "")
	untouched := w.newIssue(t, "api-log-error-1", "")
	w.settle(t)
	url := fmt.Sprintf("%s/issues/%d", publicURLBase, id)
	body := fmt.Sprintf("Arregla el reintento de la tarea.\n\nFixes %s\n\nVisto también en http://errores.ejemplo.test/issues/%d.\n"+
		"No es https://github.com/%s/issues/%d ni https://otro.test/issues/%d.", url, other, repo, untouched, untouched)
	n := w.gh.MergePR(repo, "Reintento de la tarea A", body, "marta")
	w.settle(t)

	is, err := w.db.Issue(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if is.Status != "resolved_next" || is.ResolvedInRelease == nil || *is.ResolvedInRelease != "2.3.0" {
		t.Fatalf("issue con release: %s %v", is.Status, is.ResolvedInRelease)
	}
	// Sin release en el último evento, resuelto sin más.
	if s := w.status(t, other); s != "resolved" {
		t.Fatalf("issue sin release: %s", s)
	}
	if s := w.status(t, untouched); s != "open" {
		t.Fatalf("un error no enlazado ha cambiado: %s", s)
	}
	pr := w.linkOf(t, id, store.ProviderGitHub, store.LinkPullRequest)
	if pr.ExternalID != fmt.Sprintf("%s#%d", repo, n) || pr.State != store.LinkMerged || pr.URL != fmt.Sprintf("https://github.com/%s/pull/%d", repo, n) {
		t.Fatalf("enlace del PR: %+v", pr)
	}
	h := w.statusChanges(t, id)
	if len(h) != 1 || h[0].Source != store.SourceGitHub || h[0].Author == nil || h[0].Author.Name != "marta" || h[0].To != "resolved_next" {
		t.Fatalf("historial: %+v", h)
	}
	// El PR mergeado deja la issue de Linear completada y cierra las issues de
	// GitHub de los errores que arregla, aunque el cambio venga de GitHub: el
	// PR no es ninguna de ellas.
	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-done" {
		t.Fatalf("Linear tras el PR: %s", li.StateID)
	}
	for _, c := range []struct {
		issue int64
		state string
	}{{id, "closed"}, {other, "closed"}, {untouched, "open"}} {
		gh := w.linkOf(t, c.issue, store.ProviderGitHub, store.LinkIssue)
		_, num, _ := strings.Cut(gh.ExternalID, "#")
		n, _ := strconv.Atoi(num)
		if gi, _ := w.gh.Issue(repo, n); gi.State != c.state {
			t.Errorf("issue de GitHub %s del error %d: %s; quiero %s", gh.ExternalID, c.issue, gi.State, c.state)
		}
	}
	if h := w.statusChanges(t, id); len(h) != 1 {
		t.Fatalf("el webhook del cierre de la issue de GitHub ha apuntado algo: %+v", h)
	}
}

func post(t *testing.T, url string, headers map[string]string, body []byte) int {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func linearBody(t *testing.T, linearID string, at time.Time) []byte {
	t.Helper()
	b, _ := json.Marshal(map[string]any{
		"action": "update", "type": "Issue", "actor": map[string]any{"id": "u1", "type": "user", "name": "Intruso"},
		"data":             map[string]any{"id": linearID, "identifier": "ERR-1", "state": map[string]any{"id": "state-done", "name": "Done", "type": "completed"}},
		"updatedFrom":      map[string]any{"stateId": "state-triage", "completedAt": nil},
		"webhookTimestamp": at.UnixMilli(), "webhookId": "w",
	})
	return b
}

func TestBadOrOldSignaturesAreRejected(t *testing.T) {
	w := newWorld(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)
	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	lurl := w.hooks.URL + webhooks.LinearPath
	gurl := w.hooks.URL + webhooks.GitHubPath

	fresh := linearBody(t, lin.ExternalID, time.Now())
	cases := []struct {
		name string
		code int
		do   func() int
	}{
		{"linear sin firma", 401, func() int { return post(t, lurl, nil, fresh) }},
		{"linear con otra clave", 401, func() int {
			return post(t, lurl, map[string]string{"Linear-Signature": webhooks.Sign("otra", fresh)}, fresh)
		}},
		{"linear con el cuerpo cambiado", 401, func() int {
			return post(t, lurl, map[string]string{"Linear-Signature": webhooks.Sign(linearSecret, fresh)}, append(fresh, ' '))
		}},
		{"linear vieja", 401, func() int {
			old := linearBody(t, lin.ExternalID, time.Now().Add(-2*time.Minute))
			return post(t, lurl, map[string]string{"Linear-Signature": webhooks.Sign(linearSecret, old)}, old)
		}},
		{"linear del futuro", 401, func() int {
			future := linearBody(t, lin.ExternalID, time.Now().Add(5*time.Minute))
			return post(t, lurl, map[string]string{"Linear-Signature": webhooks.Sign(linearSecret, future)}, future)
		}},
		{"github sin firma", 401, func() int {
			return post(t, gurl, map[string]string{"X-GitHub-Event": "issues", "X-GitHub-Delivery": "d1"}, []byte(`{}`))
		}},
		{"github con otra clave", 401, func() int {
			b := []byte(`{"action":"closed","issue":{"number":1,"state":"closed"},"repository":{"full_name":"ejemplo/backend"}}`)
			return post(t, gurl, map[string]string{"X-GitHub-Event": "issues", "X-GitHub-Delivery": "d2",
				"X-Hub-Signature-256": "sha256=" + webhooks.Sign("otra", b)}, b)
		}},
		{"github con firma sha1", 401, func() int {
			b := []byte(`{}`)
			return post(t, gurl, map[string]string{"X-GitHub-Event": "issues", "X-GitHub-Delivery": "d3",
				"X-Hub-Signature": "sha1=0000"}, b)
		}},
	}
	for _, c := range cases {
		if got := c.do(); got != c.code {
			t.Errorf("%s: %d; quiero %d", c.name, got, c.code)
		}
	}
	if s := w.status(t, id); s != "open" {
		t.Fatalf("una entrega rechazada ha cambiado el estado: %s", s)
	}

	// La buena se aplica una vez; repetida, no hace nada.
	headers := map[string]string{"Linear-Signature": webhooks.Sign(linearSecret, fresh), "Linear-Delivery": "entrega-1"}
	if code := post(t, lurl, headers, fresh); code != 200 {
		t.Fatalf("la buena: %d", code)
	}
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras la buena: %s", s)
	}
	if _, err := w.db.ChangeIssueStatus(context.Background(), id, "open", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
	if code := post(t, lurl, headers, fresh); code != 200 {
		t.Fatalf("repetida: %d", code)
	}
	if s := w.status(t, id); s != "open" {
		t.Fatalf("la entrega repetida se ha aplicado otra vez: %s", s)
	}

	// Sin secreto en el servidor, no se acepta nada.
	h := webhooks.New(w.db.Store, webhooks.Options{Log: quiet()})
	mux := http.NewServeMux()
	h.Register(mux)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	if code := post(t, srv.URL+webhooks.LinearPath, headers, fresh); code != http.StatusServiceUnavailable {
		t.Errorf("sin LINEAR_WEBHOOK_SECRET: %d", code)
	}
}

func TestIssueIDsIn(t *testing.T) {
	text := "Fixes https://errores.test/issues/12\nVer http://errores.test/issues/12 y https://errores.test/issues/7/events. " +
		"https://github.com/o/r/issues/3 https://errores.test.mal/issues/4 https://errores.test/issuesx/5"
	got := webhooks.IssueIDsIn("https://errores.test/", text)
	if fmt.Sprint(got) != "[12 7]" {
		t.Fatalf("ids = %v", got)
	}
}

// syncOnly lleva la cola a los sitios sin entregar aún sus webhooks.
func (w *world) syncOnly(t *testing.T) {
	t.Helper()
	if _, err := w.alerter.SyncLinks(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func (w *world) setStatus(t *testing.T, id int64, status string) {
	t.Helper()
	if _, err := w.db.ChangeIssueStatus(context.Background(), id, status,
		store.StatusChange{Actor: &store.Actor{Name: "dev", Kind: store.ActorUser}, Source: store.SourceUI}); err != nil {
		t.Fatal(err)
	}
}

// El webhook que provoca nuestro propio cambio no se aplica aunque llegue
// cuando aquí ya hay uno más nuevo.
func TestOwnEchoAfterNewerChangeIsIgnored(t *testing.T) {
	w := newWorld(t)
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)

	// Resuelto aquí: se cierra en GitHub y su «closed» se queda por entregar.
	w.setStatus(t, id, "resolved")
	w.syncOnly(t)
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "closed" {
		t.Fatalf("GitHub tras resolver: %s", gi.State)
	}
	// Reabierto aquí antes de que llegue: el «closed» viejo no lo resuelve.
	w.setStatus(t, id, "open")
	if _, err := w.gh.Flush(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.linear.Flush(); err != nil {
		t.Fatal(err)
	}
	if s := w.status(t, id); s != "open" {
		t.Fatalf("el eco del cierre ha vuelto a resolverlo: %s", s)
	}
	w.settle(t)
	h := w.statusChanges(t, id)
	if len(h) != 2 || h[0].Source != store.SourceUI || h[1].Source != store.SourceUI {
		t.Fatalf("historial con cambios que no son de nadie: %+v", h)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "open" {
		t.Fatalf("GitHub al final: %s", gi.State)
	}
	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	if li, _ := w.linear.Issue(lin.ExternalID); li.StateID != "state-todo" {
		t.Fatalf("Linear al final: %s", li.StateID)
	}

	// Una persona que de verdad la cierra y la reabre en GitHub sigue contando.
	if err := w.gh.Close(repo, 1, "completed", "luis"); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if err := w.gh.Reopen(repo, 1, "luis"); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if err := w.gh.Close(repo, 1, "completed", "luis"); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras cerrarla luis: %s", s)
	}
	if h := w.statusChanges(t, id); len(h) != 5 || h[4].Source != store.SourceGitHub || h[4].Author.Name != "luis" {
		t.Fatalf("historial: %+v", h)
	}
}

func TestOwnEchoesOutOfOrderAreIgnored(t *testing.T) {
	w := newWorld(t)
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)

	w.setStatus(t, id, "resolved")
	w.syncOnly(t)
	w.setStatus(t, id, "open")
	w.syncOnly(t)
	if n := w.gh.PendingWebhooks(); n != 2 {
		t.Fatalf("webhooks de GitHub por entregar: %d", n)
	}
	// GitHub entrega «reopened» antes que «closed».
	w.gh.ReverseWebhooks()
	w.settle(t)
	if s := w.status(t, id); s != "open" {
		t.Fatalf("aquí: %s", s)
	}
	if gi, _ := w.gh.Issue(repo, 1); gi.State != "open" {
		t.Fatalf("GitHub: %s", gi.State)
	}
	if h := w.statusChanges(t, id); len(h) != 2 {
		t.Fatalf("historial: %+v", h)
	}
}
