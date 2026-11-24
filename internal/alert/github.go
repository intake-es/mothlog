package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/markdown"
	"github.com/intake-es/mothlog/internal/store"
)

const GitHubEndpoint = "https://api.github.com"

const (
	maxGitHubTitle = 256
	// maxGitHubBody deja sitio al enlace bajo el límite de 65536 caracteres de GitHub.
	maxGitHubBody = 60 << 10
)

var errGitHubNotFound = errors.New("the GitHub issue does not exist")

type githubAPI struct {
	endpoint string
	token    string
	client   *http.Client
}

// githubIssueRef es lo que se lee de una issue de GitHub.
type githubIssueRef struct {
	Number      int    `json:"number"`
	HTMLURL     string `json:"html_url"`
	State       string `json:"state"`
	StateReason string `json:"state_reason"`
}

// linkState traduce el estado de la issue al de un enlace: cerrada como
// «no planeada», cancelada; cerrada por otro motivo, cerrada.
func (r githubIssueRef) linkState() string {
	switch {
	case r.State == "open":
		return store.LinkOpen
	case r.StateReason == "not_planned":
		return store.LinkCanceled
	}
	return store.LinkClosed
}

func (g *githubAPI) do(ctx context.Context, method, path string, in, out any) error {
	var body io.Reader
	if in != nil {
		raw, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, g.endpoint+path, body)
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Authorization", "Bearer "+g.token)
	req.Header.Set("User-Agent", UserAgent)
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return fmt.Errorf("github: %w", cleanErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("github: %w", err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		return errGitHubNotFound
	case resp.StatusCode/100 != 2:
		var e struct{ Message string }
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = truncate(strings.TrimSpace(string(raw)), 300)
		}
		return fmt.Errorf("github: %s: %s", resp.Status, e.Message)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("github: unexpected response: %w", err)
	}
	return nil
}

// splitRef parte dueño/repo#número.
func splitRef(ref string) (repo string, number int, ok bool) {
	repo, n, found := strings.Cut(ref, "#")
	if !found {
		return "", 0, false
	}
	number, err := strconv.Atoi(n)
	return repo, number, err == nil && number > 0 && strings.Count(repo, "/") == 1
}

func GitHubRef(repo string, number int) string { return repo + "#" + strconv.Itoa(number) }

func (g *githubAPI) issue(ctx context.Context, ref string) (githubIssueRef, error) {
	repo, n, ok := splitRef(ref)
	if !ok {
		return githubIssueRef{}, fmt.Errorf("%w: %q", errGone, ref)
	}
	var out githubIssueRef
	err := g.do(ctx, http.MethodGet, fmt.Sprintf("/repos/%s/issues/%d", repo, n), nil, &out)
	return out, err
}

// setState abre o cierra la issue; canceled la cierra como «no planeada».
func (g *githubAPI) setState(ctx context.Context, ref, state string) (githubIssueRef, error) {
	repo, n, ok := splitRef(ref)
	if !ok {
		return githubIssueRef{}, fmt.Errorf("%w: %q", errGone, ref)
	}
	in := map[string]any{"state": "open"}
	switch state {
	case store.LinkClosed:
		in = map[string]any{"state": "closed", "state_reason": "completed"}
	case store.LinkCanceled:
		in = map[string]any{"state": "closed", "state_reason": "not_planned"}
	}
	var out githubIssueRef
	err := g.do(ctx, http.MethodPatch, fmt.Sprintf("/repos/%s/issues/%d", repo, n), in, &out)
	return out, err
}

func (g *githubAPI) comment(ctx context.Context, ref, body string) error {
	repo, n, ok := splitRef(ref)
	if !ok {
		return fmt.Errorf("%w: %q", errGone, ref)
	}
	return g.do(ctx, http.MethodPost, fmt.Sprintf("/repos/%s/issues/%d/comments", repo, n), map[string]any{"body": body}, nil)
}

// github es la acción de una regla que crea una issue en un repositorio.
type github struct {
	st       *store.Store
	api      *githubAPI
	repo     string
	labels   []string
	ruleID   int64
	actionID string
}

// send crea la issue del error en el repositorio o, si ya la tiene, la
// comenta (y la reabre en una regresión). tx es nil en los avisos de prueba.
func (g *github) send(ctx context.Context, p *pending) error {
	if p.cron != nil {
		if p.kind == store.KindCronRecovered {
			return nil
		}
		what, _ := kindText(p)
		_, err := g.create(ctx, "["+p.project+"] "+what+": "+oneLine(p.cron.name), cronMarkdown(p))
		return err
	}
	if p.test {
		_, err := g.create(ctx, "["+p.project+"] "+oneLine(p.title), g.body(ctx, p))
		return err
	}
	var ref string
	var linkID int64
	// El repositorio es exacto: con LIKE, «_» y «%» del nombre serían comodines.
	err := g.st.Pool().QueryRow(ctx, `SELECT id, external_id FROM issue_links
		WHERE issue_id = $1 AND provider = 'github' AND kind = 'issue'
			AND substr(external_id, 1, length(CAST($2 AS text)) + 1) = CAST($2 AS text) || '#'
		ORDER BY id DESC LIMIT 1`, p.issueID, g.repo).Scan(&linkID, &ref)
	if err != nil && !errors.Is(err, db.ErrNoRows) {
		return err
	}
	if ref != "" {
		if p.kind == "new" {
			return nil
		}
		err := g.update(ctx, p, linkID, ref)
		if !errors.Is(err, errGitHubNotFound) {
			return err
		}
		p.after = append(p.after, func(ctx context.Context, tx db.Tx) error {
			_, err := tx.Exec(ctx, `DELETE FROM issue_links WHERE id = $1`, linkID)
			return err
		})
	}
	is, err := g.create(ctx, "["+p.project+"] "+oneLine(p.title), g.body(ctx, p))
	if err != nil {
		return err
	}
	ruleID := &g.ruleID
	if g.ruleID == 0 {
		ruleID = nil
	}
	p.after = append(p.after, func(ctx context.Context, tx db.Tx) error {
		_, err := store.SaveLink(ctx, tx, store.IssueLink{IssueID: p.issueID, Provider: store.ProviderGitHub, Kind: store.LinkIssue,
			ExternalID: GitHubRef(g.repo, is.Number), Identifier: GitHubRef(g.repo, is.Number), URL: is.HTMLURL,
			State: store.LinkOpen, StateName: "open", RuleID: ruleID, ActionID: g.actionID}, false)
		return err
	})
	return nil
}

func (g *github) update(ctx context.Context, p *pending, linkID int64, ref string) error {
	if p.kind == "regressed" {
		is, err := g.api.issue(ctx, ref)
		if err != nil {
			return err
		}
		if is.State != "open" {
			if _, err := g.api.setState(ctx, ref, store.LinkOpen); err != nil {
				return err
			}
		}
		p.after = append(p.after, func(ctx context.Context, tx db.Tx) error {
			_, err := tx.Exec(ctx, `UPDATE issue_links SET state = 'open', state_name = 'open', updated_at = now() WHERE id = $1`,
				linkID)
			return err
		})
	}
	what, _ := kindText(p)
	body := fmt.Sprintf("%s: release %s, %s.", what, orNone(markdown.InertText(p.release), p.l.T("(none)", "(ninguno)")), p.eventAt.UTC().Format("2006-01-02 15:04 UTC"))
	if p.link != "" {
		body += "\n\n" + viewIn(p.l, p.link)
	}
	return g.api.comment(ctx, ref, body)
}

// body es el context.md del error, con el enlace a la interfaz delante y lo
// que trae el evento sin menciones, enlaces ni HTML vivos.
func (g *github) body(ctx context.Context, p *pending) string {
	var b strings.Builder
	if p.link != "" {
		b.WriteString(viewIn(p.l, p.link) + "\n\n")
	}
	if p.issueID == 0 && p.issue != nil {
		b.WriteString(markdown.IssueInert(*p.issue, p.l))
		return b.String()
	}
	c, err := g.st.IssueContextIn(ctx, p.issueID, "", "", nil, time.Now())
	if err != nil {
		d, derr := issueDetail(ctx, g.st, p)
		if derr == nil {
			b.WriteString(markdown.IssueInert(d, p.l))
		}
		return b.String()
	}
	b.WriteString(markdown.ContextInert(c, p.link, maxGitHubBody-b.Len(), p.l))
	return b.String()
}

func (g *github) create(ctx context.Context, title, body string) (githubIssueRef, error) {
	in := map[string]any{"title": truncate(title, maxGitHubTitle), "body": body}
	if len(g.labels) > 0 {
		in["labels"] = g.labels
	}
	var out githubIssueRef
	if err := g.api.do(ctx, http.MethodPost, "/repos/"+g.repo+"/issues", in, &out); err != nil {
		if errors.Is(err, errGitHubNotFound) {
			return out, fmt.Errorf("github: repository %s does not exist or the token can't reach it", g.repo)
		}
		return out, err
	}
	if out.Number == 0 {
		return out, errors.New("github: the response has no issue number")
	}
	return out, nil
}
