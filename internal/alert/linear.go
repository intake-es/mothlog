package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

const LinearEndpoint = "https://api.linear.app/graphql"

const maxLinearTitle = 255

type linear struct {
	st       *store.Store
	endpoint string
	key      string
	client   *http.Client
	team     string
	labels   []string

	mu        sync.Mutex
	unstarted string
}

var errLinearNotFound = errors.New("la issue de Linear no existe")

// linearRef es lo que se guarda de una issue de Linear: el identifier
// (ERR-123) y la url van en el botón del aviso de Slack.
type linearRef struct {
	ID         string
	Identifier string
	URL        string
}

// send crea la issue de Linear o, si ya hay una, la reabre en una regresión y
// la comenta en el resto de avisos. tx es nil en los avisos de prueba.
func (l *linear) send(ctx context.Context, tx pgx.Tx, p *pending) error {
	if p.cron != nil {
		return l.createCron(ctx, p)
	}
	if p.linear != "" && p.kind == "new" {
		return nil
	}
	var is linearRef
	var err error
	if p.linear != "" {
		if p.kind == "regressed" {
			is, err = l.reopen(ctx, p)
		} else {
			is, err = l.commentOnly(ctx, p)
		}
		if err != nil && !errors.Is(err, errLinearNotFound) {
			return err
		}
	}
	if p.linear == "" || err != nil {
		if is, err = l.create(ctx, p); err != nil {
			return err
		}
	}
	if tx == nil {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE issues SET linear_issue_id = $2,
			linear_issue_identifier = NULLIF($3, ''), linear_issue_url = NULLIF($4, '')
		WHERE id = $1`, p.issueID, is.ID, is.Identifier, is.URL)
	return err
}

// labelIDs son las etiquetas de LINEAR_LABEL_IDS más las del proyecto, sin repetir.
func (l *linear) labelIDs(p *pending) []string {
	var ids []string
	for _, id := range slices.Concat(l.labels, p.labels) {
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

func (l *linear) create(ctx context.Context, p *pending) (linearRef, error) {
	d, err := issueDetail(ctx, l.st, p)
	if err != nil {
		return linearRef{}, err
	}
	var desc strings.Builder
	if link := p.link; link != "" {
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
			Issue   linearRef
		}
	}
	err = l.do(ctx, `mutation($input: IssueCreateInput!) { issueCreate(input: $input) { success issue { id identifier url } } }`,
		map[string]any{"input": input}, &out)
	if err != nil {
		// Si un intento anterior la creó pero no llegó a guardarse su id, ya existe.
		if is, found, _, lookupErr := l.lookup(ctx, p.createID); lookupErr == nil && found {
			return is, nil
		}
		return linearRef{}, err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue.ID == "" {
		return linearRef{}, errors.New("linear: issueCreate sin éxito")
	}
	return out.IssueCreate.Issue, nil
}

func (l *linear) reopen(ctx context.Context, p *pending) (linearRef, error) {
	is, found, archived, err := l.lookup(ctx, p.linear)
	if err != nil {
		return is, err
	}
	if !found {
		return is, errLinearNotFound
	}
	if archived {
		var out struct{ IssueUnarchive struct{ Success bool } }
		if err := l.do(ctx, `mutation($id: String!) { issueUnarchive(id: $id) { success } }`,
			map[string]any{"id": p.linear}, &out); err != nil {
			return is, err
		}
	}
	return is, l.reopenAndComment(ctx, p)
}

func (l *linear) reopenAndComment(ctx context.Context, p *pending) error {
	state, err := l.unstartedState(ctx)
	if err != nil {
		return err
	}
	input := map[string]any{"stateId": state}
	if len(p.labels) > 0 {
		input["addedLabelIds"] = p.labels
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

	return l.comment(ctx, p, "Ha vuelto a ocurrir")
}

func (l *linear) commentOnly(ctx context.Context, p *pending) (linearRef, error) {
	is, found, _, err := l.lookup(ctx, p.linear)
	if err != nil {
		return is, err
	}
	if !found {
		return is, errLinearNotFound
	}
	what, _ := kindText(p)
	return is, l.comment(ctx, p, what)
}

func (l *linear) comment(ctx context.Context, p *pending, what string) error {
	body := fmt.Sprintf("%s: release %s, %s.",
		what, orNone(p.release, "(ninguno)"), p.eventAt.UTC().Format("2006-01-02 15:04 UTC"))
	if link := p.link; link != "" {
		body += fmt.Sprintf("\n\n[Ver en intake-errors](%s)", link)
	}
	var com struct{ CommentCreate struct{ Success bool } }
	err := l.do(ctx, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
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
func (l *linear) lookup(ctx context.Context, id string) (is linearRef, found, archived bool, err error) {
	var out struct {
		Issue *struct {
			linearRef
			Trashed    *bool
			ArchivedAt *string
		}
	}
	err = l.do(ctx, `query($id: String!) { issue(id: $id) { id identifier url trashed archivedAt } }`,
		map[string]any{"id": id}, &out)
	var gqlErr *gqlErrors
	if errors.As(err, &gqlErr) && gqlErr.notFound() {
		return is, false, false, nil
	}
	if err != nil {
		return is, false, false, err
	}
	if out.Issue == nil || (out.Issue.Trashed != nil && *out.Issue.Trashed) {
		return is, false, false, nil
	}
	return out.Issue.linearRef, true, out.Issue.ArchivedAt != nil, nil
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
