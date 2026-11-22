package fakes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type GitHubIssue struct {
	Repo        string
	Number      int
	Title       string
	Body        string
	Labels      []string
	State       string
	StateReason string
	Comments    []string
	PullRequest bool
	Merged      bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
	ClosedAt    *time.Time
}

// GitHub hace de la API REST de issues de GitHub y manda sus webhooks de
// issues y pull requests.
type GitHub struct {
	// Tokens son los tokens que acepta, con el login al que pertenece cada uno.
	Tokens     map[string]string
	Secret     string
	WebhookURL string
	Now        func() time.Time

	mu    sync.Mutex
	repos map[string][]*GitHubIssue
	out   *outbox
	log   *Log
}

func NewGitHub(log *Log, secret string, tokens map[string]string) *GitHub {
	g := &GitHub{Tokens: tokens, Secret: secret, repos: map[string][]*GitHubIssue{}, log: log, Now: time.Now}
	g.out = newOutbox("github", log, func() string { return g.WebhookURL })
	return g
}

func (g *GitHub) Async()                { g.out.startAsync() }
func (g *GitHub) Flush() ([]int, error) { return g.out.Flush() }
func (g *GitHub) PendingWebhooks() int  { return g.out.Pending() }

// ReverseWebhooks da la vuelta a los webhooks pendientes, como cuando GitHub
// los entrega desordenados.
func (g *GitHub) ReverseWebhooks() { g.out.reverse() }

func (g *GitHub) Handler() http.Handler { return logged("github", g.log, g.api) }

func (g *GitHub) Issue(repo string, n int) (GitHubIssue, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	is := g.find(repo, n)
	if is == nil {
		return GitHubIssue{}, false
	}
	c := *is
	c.Comments = slices.Clone(is.Comments)
	return c, true
}

func (g *GitHub) Issues(repo string) []GitHubIssue {
	g.mu.Lock()
	n := len(g.repos[repo])
	g.mu.Unlock()
	var out []GitHubIssue
	for i := 1; i <= n; i++ {
		is, _ := g.Issue(repo, i)
		out = append(out, is)
	}
	return out
}

func (g *GitHub) find(repo string, n int) *GitHubIssue {
	list := g.repos[repo]
	if n < 1 || n > len(list) {
		return nil
	}
	return list[n-1]
}

// Close cierra la issue como alguien en GitHub (reason: completed o
// not_planned) y manda el webhook.
func (g *GitHub) Close(repo string, n int, reason, login string) error {
	return g.setState(repo, n, "closed", reason, login)
}

func (g *GitHub) Reopen(repo string, n int, login string) error {
	return g.setState(repo, n, "open", "", login)
}

func (g *GitHub) setState(repo string, n int, state, reason, login string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	is := g.find(repo, n)
	if is == nil || is.PullRequest {
		return fmt.Errorf("github: no existe la issue %s#%d", repo, n)
	}
	g.stateLocked(is, state, reason, login)
	return nil
}

func (g *GitHub) stateLocked(is *GitHubIssue, state, reason, login string) {
	if is.State == state && (state == "open" || is.StateReason == reason) {
		return
	}
	now := g.Now().UTC()
	action := "closed"
	is.State, is.UpdatedAt = state, now
	if state == "open" {
		action, is.StateReason, is.ClosedAt = "reopened", "reopened", nil
	} else {
		if reason == "" {
			reason = "completed"
		}
		is.StateReason, is.ClosedAt = reason, &now
	}
	g.hookLocked("issues", action, is, login)
}

// MergePR abre un PR en el repositorio con ese título y descripción, manda
// su webhook de abierto, lo mergea y manda el de cerrado. Devuelve su número.
func (g *GitHub) MergePR(repo, title, body, login string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := g.Now().UTC()
	pr := &GitHubIssue{Repo: repo, Number: len(g.repos[repo]) + 1, Title: title, Body: body, State: "open", PullRequest: true,
		CreatedAt: now, UpdatedAt: now}
	g.repos[repo] = append(g.repos[repo], pr)
	g.hookLocked("pull_request", "opened", pr, login)
	pr.State, pr.Merged, pr.ClosedAt = "closed", true, &now
	g.hookLocked("pull_request", "closed", pr, login)
	return pr.Number
}

func (g *GitHub) htmlURL(is *GitHubIssue) string {
	kind := "issues"
	if is.PullRequest {
		kind = "pull"
	}
	return fmt.Sprintf("https://github.com/%s/%s/%d", is.Repo, kind, is.Number)
}

func (g *GitHub) user(login string) map[string]any {
	return map[string]any{"login": login, "id": len(login) * 7919, "type": "User", "html_url": "https://github.com/" + login}
}

func (g *GitHub) repoJSON(repo string) map[string]any {
	owner, name, _ := strings.Cut(repo, "/")
	return map[string]any{"id": 4242, "name": name, "full_name": repo, "private": true, "owner": g.user(owner),
		"html_url": "https://github.com/" + repo}
}

func (g *GitHub) issueJSON(is *GitHubIssue) map[string]any {
	labels := []map[string]any{}
	for _, l := range is.Labels {
		labels = append(labels, map[string]any{"name": l, "color": "ededed"})
	}
	var closed any
	if is.ClosedAt != nil {
		closed = is.ClosedAt.Format(time.RFC3339)
	}
	var reason any
	if is.StateReason != "" {
		reason = is.StateReason
	}
	return map[string]any{
		"id": int64(is.Number) + 7_000_000, "node_id": "I_" + strconv.Itoa(is.Number),
		"url":      "https://api.github.com/repos/" + is.Repo + "/issues/" + strconv.Itoa(is.Number),
		"html_url": g.htmlURL(is), "number": is.Number, "title": is.Title, "body": is.Body, "labels": labels,
		"state": is.State, "state_reason": reason, "locked": false, "comments": len(is.Comments),
		"created_at": is.CreatedAt.Format(time.RFC3339), "updated_at": is.UpdatedAt.Format(time.RFC3339), "closed_at": closed,
		"user": g.user("intake-errors-bot"), "author_association": "NONE",
	}
}

func (g *GitHub) hookLocked(event, action string, is *GitHubIssue, login string) {
	payload := map[string]any{"action": action, "repository": g.repoJSON(is.Repo), "sender": g.user(login)}
	if event == "pull_request" {
		pr := g.issueJSON(is)
		pr["merged"] = is.Merged
		pr["draft"] = false
		pr["head"] = map[string]any{"ref": "arreglo-" + strconv.Itoa(is.Number)}
		pr["base"] = map[string]any{"ref": "main"}
		if is.Merged {
			pr["merged_at"] = is.ClosedAt.Format(time.RFC3339)
			pr["merged_by"] = g.user(login)
		}
		payload["number"] = is.Number
		payload["pull_request"] = pr
	} else {
		payload["issue"] = g.issueJSON(is)
	}
	body, _ := json.Marshal(payload)
	m := hmac.New(sha256.New, []byte(g.Secret))
	m.Write(body)
	g.out.push(hook{event: event + "." + action, body: body, headers: map[string]string{
		"Content-Type":                           "application/json",
		"User-Agent":                             "GitHub-Hookshot/f1b2c3d",
		"X-GitHub-Event":                         event,
		"X-GitHub-Delivery":                      newUUID(),
		"X-GitHub-Hook-ID":                       "512",
		"X-Hub-Signature-256":                    "sha256=" + hex.EncodeToString(m.Sum(nil)),
		"X-GitHub-Hook-Installation-Target-Type": "repository",
	}})
}

// api atiende /repos/{dueño}/{repo}/issues[/{n}[/comments]].
func (g *GitHub) api(w http.ResponseWriter, r *http.Request, body []byte) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	login, ok := g.Tokens[token]
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"message": "Bad credentials", "documentation_url": "https://docs.github.com/rest"})
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) < 4 || parts[0] != "repos" || parts[3] != "issues" {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	repo := parts[1] + "/" + parts[2]
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(parts) == 4 && r.Method == http.MethodPost {
		var in struct {
			Title  string   `json:"title"`
			Body   string   `json:"body"`
			Labels []string `json:"labels"`
		}
		if err := json.Unmarshal(body, &in); err != nil || in.Title == "" {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]any{"message": "Validation Failed"})
			return
		}
		now := g.Now().UTC()
		is := &GitHubIssue{Repo: repo, Number: len(g.repos[repo]) + 1, Title: in.Title, Body: in.Body, Labels: in.Labels,
			State: "open", CreatedAt: now, UpdatedAt: now}
		g.repos[repo] = append(g.repos[repo], is)
		g.hookLocked("issues", "opened", is, login)
		writeJSON(w, http.StatusCreated, g.issueJSON(is))
		return
	}
	if len(parts) < 5 {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	n, _ := strconv.Atoi(parts[4])
	is := g.find(repo, n)
	if is == nil {
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
		return
	}
	switch {
	case len(parts) == 5 && r.Method == http.MethodGet:
		writeJSON(w, http.StatusOK, g.issueJSON(is))
	case len(parts) == 5 && r.Method == http.MethodPatch:
		var in struct {
			State       string `json:"state"`
			StateReason string `json:"state_reason"`
		}
		_ = json.Unmarshal(body, &in)
		if in.State == "open" || in.State == "closed" {
			g.stateLocked(is, in.State, in.StateReason, login)
		}
		writeJSON(w, http.StatusOK, g.issueJSON(is))
	case len(parts) == 6 && parts[5] == "comments" && r.Method == http.MethodPost:
		var in struct {
			Body string `json:"body"`
		}
		_ = json.Unmarshal(body, &in)
		is.Comments = append(is.Comments, in.Body)
		writeJSON(w, http.StatusCreated, map[string]any{"id": len(is.Comments), "body": in.Body, "user": g.user(login)})
	default:
		writeJSON(w, http.StatusNotFound, map[string]any{"message": "Not Found"})
	}
}
