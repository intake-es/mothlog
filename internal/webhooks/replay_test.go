package webhooks_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
	"github.com/intake-es/intake-errors/internal/webhooks"
)

// postText es post con la respuesta en texto.
func postText(t *testing.T, url string, headers map[string]string, body []byte) (int, string) {
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
	defer func() { _ = resp.Body.Close() }()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(b))
}

func githubIssueBody(t *testing.T, number int, action string, updated time.Time) []byte {
	t.Helper()
	issue := map[string]any{"number": number, "state": "closed", "state_reason": "completed"}
	if !updated.IsZero() {
		issue["updated_at"] = updated.UTC().Format(time.RFC3339)
	}
	b, err := json.Marshal(map[string]any{"action": action, "issue": issue,
		"repository": map[string]any{"full_name": repo}, "sender": map[string]any{"login": "intruso"}})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func githubHeaders(body []byte, delivery string) map[string]string {
	h := map[string]string{"X-GitHub-Event": "issues", "X-Hub-Signature-256": "sha256=" + webhooks.Sign(githubSecret, body)}
	if delivery != "" {
		h["X-GitHub-Delivery"] = delivery
	}
	return h
}

func (w *world) reopen(t *testing.T, id int64) {
	t.Helper()
	if _, err := w.db.ChangeIssueStatus(context.Background(), id, "open", store.StatusChange{Source: store.SourceAPI}); err != nil {
		t.Fatal(err)
	}
}

// La cabecera con el id de la entrega no va firmada: el mismo cuerpo firmado
// con otro id, o sin él, es la misma entrega.
func TestSignedBodyReplayedWithOtherDeliveryIsIgnored(t *testing.T) {
	w := newWorld(t)
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)

	gurl := w.hooks.URL + webhooks.GitHubPath
	body := githubIssueBody(t, 1, "closed", time.Now())
	if code, msg := postText(t, gurl, githubHeaders(body, "gh-1"), body); code != 200 || msg != "ok" {
		t.Fatalf("GitHub, la primera: %d %q", code, msg)
	}
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras la primera de GitHub: %s", s)
	}
	w.reopen(t, id)
	for _, delivery := range []string{"gh-1", "gh-otra", ""} {
		code, msg := postText(t, gurl, githubHeaders(body, delivery), body)
		if code != 200 || msg != "ya recibida" {
			t.Errorf("GitHub reenviada con entrega %q: %d %q", delivery, code, msg)
		}
		if s := w.status(t, id); s != "open" {
			t.Fatalf("GitHub reenviada con entrega %q ha vuelto a cambiar el error: %s", delivery, s)
		}
	}

	lin := w.linkOf(t, id, store.ProviderLinear, store.LinkIssue)
	lurl := w.hooks.URL + webhooks.LinearPath
	fresh := linearBody(t, lin.ExternalID, time.Now())
	sig := webhooks.Sign(linearSecret, fresh)
	if code, msg := postText(t, lurl, map[string]string{"Linear-Signature": sig, "Linear-Delivery": "lin-1"}, fresh); code != 200 || msg != "ok" {
		t.Fatalf("Linear, la primera: %d %q", code, msg)
	}
	if s := w.status(t, id); s != "resolved" {
		t.Fatalf("tras la primera de Linear: %s", s)
	}
	w.reopen(t, id)
	for _, delivery := range []string{"lin-otra", ""} {
		headers := map[string]string{"Linear-Signature": sig}
		if delivery != "" {
			headers["Linear-Delivery"] = delivery
		}
		code, msg := postText(t, lurl, headers, fresh)
		if code != 200 || msg != "ya recibida" {
			t.Errorf("Linear reenviada con entrega %q: %d %q", delivery, code, msg)
		}
		if s := w.status(t, id); s != "open" {
			t.Fatalf("Linear reenviada con entrega %q ha vuelto a cambiar el error: %s", delivery, s)
		}
	}
}

// GitHub no fecha las entregas: la issue o el PR firmados más viejos de lo
// que se guardan las entregas, o sin updated_at, se rechazan.
func TestOldGitHubDeliveryIsRejected(t *testing.T) {
	w := newWorld(t)
	w.addGitHubRule(t)
	id := w.newIssue(t, "api-tarea-a-1", "")
	w.settle(t)
	gurl := w.hooks.URL + webhooks.GitHubPath

	old := githubIssueBody(t, 1, "closed", time.Now().Add(-store.WebhookDeliveryRetention-time.Hour))
	undated := githubIssueBody(t, 1, "closed", time.Time{})
	oldPR, _ := json.Marshal(map[string]any{"action": "closed", "number": 9,
		"pull_request": map[string]any{"number": 9, "state": "closed", "merged": true,
			"body":       "Fixes " + publicURLBase + "/issues/" + itoa(id),
			"updated_at": time.Now().Add(-store.WebhookDeliveryRetention - time.Hour).UTC().Format(time.RFC3339)},
		"repository": map[string]any{"full_name": repo}})
	for name, c := range map[string]struct {
		event string
		body  []byte
	}{"issue vieja": {"issues", old}, "issue sin updated_at": {"issues", undated}, "PR viejo": {"pull_request", oldPR}} {
		h := githubHeaders(c.body, "vieja-"+name)
		h["X-GitHub-Event"] = c.event
		if code, msg := postText(t, gurl, h, c.body); code != http.StatusUnauthorized || !strings.Contains(msg, "entrega vieja") {
			t.Errorf("%s: %d %q", name, code, msg)
		}
	}
	if s := w.status(t, id); s != "open" {
		t.Fatalf("una entrega vieja ha cambiado el error: %s", s)
	}
	recent := githubIssueBody(t, 1, "closed", time.Now().Add(-store.WebhookDeliveryRetention+time.Hour))
	if code, msg := postText(t, gurl, githubHeaders(recent, "reciente"), recent); code != 200 || msg != "ok" {
		t.Fatalf("una dentro del plazo: %d %q", code, msg)
	}
}

// Un PR no enlaza más de MaxLinkedIssues errores, tampoco editándolo una y
// otra vez con ids distintos; los que ya tiene se siguen actualizando.
func TestPullRequestLinksAtMostMaxLinkedIssues(t *testing.T) {
	w := newWorld(t)
	var ids []int64
	for range 3 * webhooks.MaxLinkedIssues {
		ids = append(ids, w.db.AddIssue(t, storetest.Issue{ProjectID: w.project, Title: "Error " + itoa(int64(len(ids)))}))
	}
	urls := func(ids []int64) string {
		var out []string
		for _, id := range ids {
			out = append(out, publicURLBase+"/issues/"+itoa(id))
		}
		return strings.Join(out, "\n")
	}
	send := func(n int, action, state string, merged bool, linked []int64) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"action": action, "number": 3,
			"pull_request": map[string]any{"number": 3, "state": state, "merged": merged, "title": "Arregla todo",
				"body": urls(linked), "html_url": "https://github.com/" + repo + "/pull/3",
				"updated_at": time.Now().UTC().Format(time.RFC3339)},
			"repository": map[string]any{"full_name": repo}})
		h := githubHeaders(body, "pr-3-"+itoa(int64(n)))
		h["X-GitHub-Event"] = "pull_request"
		if code, msg := postText(t, w.hooks.URL+webhooks.GitHubPath, h, body); code != 200 || msg != "ok" {
			t.Fatalf("PR, entrega %d: %d %q", n, code, msg)
		}
	}
	count := func() int {
		t.Helper()
		var n int
		if err := w.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM issue_links
			WHERE kind = 'pull_request' AND external_id = $1`, repo+"#3").Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// Abierto con 15 ids, y editado dos veces con 20 ids distintos cada vez.
	send(1, "opened", "open", false, ids[:15])
	if n := count(); n != 15 {
		t.Fatalf("tras abrirlo: %d enlaces", n)
	}
	send(2, "edited", "open", false, ids[15:35])
	send(3, "edited", "open", false, ids[35:55])
	if n := count(); n != webhooks.MaxLinkedIssues {
		t.Fatalf("tras editarlo con ids distintos: %d enlaces; quiero %d", n, webhooks.MaxLinkedIssues)
	}

	// Mergeado con los primeros: resuelve los que ya tenía enlazados y no los nuevos.
	send(4, "closed", "closed", true, append(append([]int64{}, ids[:2]...), ids[50:52]...))
	if n := count(); n != webhooks.MaxLinkedIssues {
		t.Fatalf("tras mergearlo: %d enlaces", n)
	}
	for _, id := range ids[:2] {
		if s := w.status(t, id); s != "resolved" && s != "resolved_next" {
			t.Errorf("el error %d, enlazado, ha quedado %s", id, s)
		}
	}
	for _, id := range ids[50:52] {
		if s := w.status(t, id); s != "open" {
			t.Errorf("el error %d, pasado el tope, ha quedado %s", id, s)
		}
	}
}

// La acción de GitHub busca la issue de su repositorio exacto: «_» no es un comodín.
func TestGitHubActionMatchesExactRepo(t *testing.T) {
	w := newWorld(t)
	const exact = "ejemplo/back_end"
	_, _, err := w.db.CreateRule(context.Background(), w.project, store.Rule{
		Name: "A GitHub", Enabled: true, Conditions: store.RuleConditions{New: true},
		Actions: []store.RuleAction{{Type: store.ActionGitHub, Repo: exact, Token: ruleToken}},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.alerter.RulesChanged(w.project)
	id := w.newIssue(t, "api-tarea-a-1", "")
	// Un enlace a otro repositorio cuyo nombre encaja con «back_end» como patrón LIKE.
	if _, err := store.SaveLink(context.Background(), w.db.Pool(), store.IssueLink{IssueID: id, Provider: store.ProviderGitHub,
		Kind: store.LinkIssue, ExternalID: "ejemplo/backXend#1", Identifier: "ejemplo/backXend#1", State: store.LinkOpen}, false); err != nil {
		t.Fatal(err)
	}
	w.settle(t)
	if got := len(w.gh.Issues(exact)); got != 1 {
		t.Fatalf("issues en %s = %d; quiero 1: el enlace de otro repositorio no cuenta", exact, got)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
