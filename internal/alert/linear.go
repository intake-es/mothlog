package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

const LinearEndpoint = "https://api.linear.app/graphql"

const maxLinearTitle = 255

type linear struct {
	st         *store.Store
	base       string
	endpoint   string
	key        string
	client     *http.Client
	team       string
	labels     []string
	extraLabel string

	mu        sync.Mutex
	unstarted string
}

var errLinearNotFound = errors.New("la issue de Linear no existe")

func (l *linear) send(ctx context.Context, tx pgx.Tx, p *pending) error {
	if p.linear != "" {
		err := l.reopen(ctx, p)
		if !errors.Is(err, errLinearNotFound) {
			return err
		}
	}
	id, err := l.create(ctx, p)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE issues SET linear_issue_id = $2 WHERE id = $1`, p.issueID, id)
	return err
}

func (l *linear) labelIDs(p *pending) []string {
	ids := append([]string(nil), l.labels...)
	if p.linear_extra_label && l.extraLabel != "" {
		ids = append(ids, l.extraLabel)
	}
	return ids
}

func (l *linear) create(ctx context.Context, p *pending) (string, error) {
	d, err := l.st.IssueDetail(ctx, p.issueID, time.Now())
	if err != nil {
		return "", fmt.Errorf("leer issue %d: %w", p.issueID, err)
	}
	var desc strings.Builder
	if link := issueLink(l.base, p.issueID); link != "" {
		fmt.Fprintf(&desc, "[Ver en intake-errors](%s)\n\n", link)
	}
	desc.WriteString(markdown.Issue(d))

	input := map[string]any{
		"id":          p.createID,
		"teamId":      l.team,
		"title":       truncate("["+p.project+"] "+oneLine(p.title), maxLinearTitle),
		"description": desc.String(),
	}
	if ids := l.labelIDs(p); len(ids) > 0 {
		input["labelIds"] = ids
	}
	var out struct {
		IssueCreate struct {
			Success bool
			Issue   struct{ ID string }
		}
	}
	err = l.do(ctx, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id } } }`,
		map[string]any{"input": input}, &out)
	if err != nil {
		// Si un intento anterior la creó pero no llegó a guardarse su id, ya existe.
		if found, _, lookupErr := l.lookup(ctx, p.createID); lookupErr == nil && found {
			return p.createID, nil
		}
		return "", err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue.ID == "" {
		return "", errors.New("linear: issueCreate sin éxito")
	}
	return out.IssueCreate.Issue.ID, nil
}

func (l *linear) reopen(ctx context.Context, p *pending) error {
	found, archived, err := l.lookup(ctx, p.linear)
	if err != nil {
		return err
	}
	if !found {
		return errLinearNotFound
	}
	if archived {
		var out struct{ IssueUnarchive struct{ Success bool } }
		if err := l.do(ctx, `mutation($id: String!) { issueUnarchive(id: $id) { success } }`,
			map[string]any{"id": p.linear}, &out); err != nil {
			return err
		}
	}

	state, err := l.unstartedState(ctx)
	if err != nil {
		return err
	}
	input := map[string]any{"stateId": state}
	if p.linear_extra_label && l.extraLabel != "" {
		input["addedLabelIds"] = []string{l.extraLabel}
	}
	var upd struct{ IssueUpdate struct{ Success bool } }
	err = l.do(ctx, `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`,
		map[string]any{"id": p.linear, "input": input}, &upd)
	if err == nil && !upd.IssueUpdate.Success {
		err = errors.New("linear: issueUpdate sin éxito")
	}
	if err != nil {
		// El estado guardado puede haberse borrado en Linear: se vuelve a buscar.
		l.forgetUnstarted()
		return err
	}

	body := fmt.Sprintf("Ha vuelto a ocurrir: release %s, %s.",
		orNone(p.release, "(ninguno)"), p.eventAt.UTC().Format("2006-01-02 15:04 UTC"))
	if link := issueLink(l.base, p.issueID); link != "" {
		body += fmt.Sprintf("\n\n[Ver en intake-errors](%s)", link)
	}
	var com struct{ CommentCreate struct{ Success bool } }
	err = l.do(ctx, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
		map[string]any{"input": map[string]any{"issueId": p.linear, "body": body}}, &com)
	if err != nil {
		return err
	}
	if !com.CommentCreate.Success {
		return errors.New("linear: commentCreate sin éxito")
	}
	return nil
}

// lookup dice si la issue existe en Linear (sin contar la papelera) y si está archivada.
func (l *linear) lookup(ctx context.Context, id string) (found, archived bool, err error) {
	var out struct {
		Issue *struct {
			ID         string
			Trashed    *bool
			ArchivedAt *string
		}
	}
	err = l.do(ctx, `query($id: String!) { issue(id: $id) { id trashed archivedAt } }`,
		map[string]any{"id": id}, &out)
	var gqlErr *gqlErrors
	if errors.As(err, &gqlErr) && gqlErr.notFound() {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	if out.Issue == nil || (out.Issue.Trashed != nil && *out.Issue.Trashed) {
		return false, false, nil
	}
	return true, out.Issue.ArchivedAt != nil, nil
}

// unstartedState busca el primer estado de tipo unstarted del equipo; el
// nombre ("Todo", "Por hacer"…) cambia de un equipo a otro.
func (l *linear) unstartedState(ctx context.Context) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.unstarted != "" {
		return l.unstarted, nil
	}
	var out struct {
		Team struct {
			States struct {
				Nodes []struct {
					ID       string
					Position float64
				}
			}
		}
	}
	err := l.do(ctx, `query($team: String!) { team(id: $team) { states(filter: {type: {eq: "unstarted"}}) { nodes { id position } } } }`,
		map[string]any{"team": l.team}, &out)
	if err != nil {
		return "", err
	}
	nodes := out.Team.States.Nodes
	if len(nodes) == 0 {
		return "", errors.New("linear: el equipo no tiene ningún estado unstarted")
	}
	best := nodes[0]
	for _, n := range nodes[1:] {
		if n.Position < best.Position {
			best = n
		}
	}
	l.unstarted = best.ID
	return best.ID, nil
}

func (l *linear) forgetUnstarted() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.unstarted = ""
}

type gqlError struct {
	Message string `json:"message"`
}

type gqlErrors []gqlError

func (e *gqlErrors) Error() string {
	msgs := make([]string, len(*e))
	for i, m := range *e {
		msgs[i] = m.Message
	}
	return "linear: " + strings.Join(msgs, "; ")
}

func (e *gqlErrors) notFound() bool {
	for _, m := range *e {
		if strings.Contains(strings.ToLower(m.Message), "not found") {
			return true
		}
	}
	return false
}

func (l *linear) do(ctx context.Context, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.endpoint, bytes.NewReader(body))
	if err != nil {
		return cleanErr(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", l.key)
	resp, err := l.client.Do(req)
	if err != nil {
		return fmt.Errorf("linear: %w", cleanErr(err))
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("linear: %w", err)
	}
	var res struct {
		Data   json.RawMessage `json:"data"`
		Errors gqlErrors       `json:"errors"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return fmt.Errorf("linear: %s: %s", resp.Status, truncate(strings.TrimSpace(string(raw)), 300))
	}
	if len(res.Errors) > 0 {
		return &res.Errors
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("linear: %s", resp.Status)
	}
	if err := json.Unmarshal(res.Data, out); err != nil {
		return fmt.Errorf("linear: respuesta inesperada: %w", err)
	}
	return nil
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}
