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

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/markdown"
	"github.com/intake-es/mothlog/internal/store"
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
	// shared: crea con el id de creación del error (issues.linear_create_id),
	// el mismo que usa la issue creada a mano.
	shared bool

	mu        sync.Mutex
	unstarted string
}

var errLinearNotFound = errors.New("the Linear issue does not exist")

// linearRef es lo que se guarda de una issue de Linear: el identifier
// (ERR-123) y la url van en el botón del aviso de Slack.
type linearRef struct {
	ID         string
	Identifier string
	URL        string
}

// send crea la issue de Linear o, si ya hay una, la reabre en una regresión y
// la comenta en el resto de avisos. Los avisos de prueba no guardan el enlace.
func (l *linear) send(ctx context.Context, p *pending) error {
	if p.isMonitor() {
		return l.createCron(ctx, p)
	}
	if p.linear != "" && p.kind == "new" {
		return nil
	}
	var is linearRef
	var err error
	state := ""
	if p.linear != "" {
		if p.kind == "regressed" {
			is, err = l.reopen(ctx, p)
			state = store.LinkOpen
		} else {
			is, err = l.commentOnly(ctx, p)
		}
		if err != nil && !errors.Is(err, errLinearNotFound) {
			return err
		}
	}
	if p.linear == "" || err != nil {
		// Sin issue aún, el id es el del error: si a la vez se crea a mano, Linear
		// rechaza la segunda con el mismo id y las dos acaban en la misma.
		if p.linear == "" && !p.test && l.shared {
			id, err := l.st.ClaimLinearCreateID(ctx, p.issueID, p.createID)
			if err != nil {
				return err
			}
			p.createID = id
		}
		if is, err = l.create(ctx, p); err != nil {
			return err
		}
		state = store.LinkOpen
	}
	if p.test {
		return nil
	}
	p.after = append(p.after, func(ctx context.Context, tx db.Tx) error {
		_, err := store.SaveLink(ctx, tx, store.IssueLink{IssueID: p.issueID, Provider: store.ProviderLinear, Kind: store.LinkIssue,
			ExternalID: is.ID, Identifier: is.Identifier, URL: is.URL, State: state}, true)
		return err
	})
	return nil
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
		desc.WriteString(viewIn(p.l, link) + "\n\n")
	}
	desc.WriteString(markdown.IssueInert(d, p.l))

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
		is, found, _, lookupErr := l.lookup(ctx, p.createID)
		if lookupErr == nil && found {
			return is, nil
		}
		// Si Linear no la tiene, el siguiente intento puede pedir otro id.
		if lookupErr == nil && !p.test && l.shared && p.issueID != 0 {
			if err := l.st.ForgetLinearCreateID(ctx, p.issueID, p.createID); err != nil {
				return linearRef{}, err
			}
		}
		return linearRef{}, err
	}
	if !out.IssueCreate.Success || out.IssueCreate.Issue.ID == "" {
		return linearRef{}, errors.New("linear: issueCreate was not successful")
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
		err = errors.New("linear: issueUpdate was not successful")
	}
	if err != nil {
		// El estado guardado puede haberse borrado en Linear: se vuelve a buscar.
		l.forgetUnstarted()
		return err
	}

	return l.comment(ctx, p, p.l.T("It happened again", "Ha vuelto a ocurrir"))
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
		what, orNone(markdown.InertText(p.release), p.l.T("(none)", "(ninguno)")), p.eventAt.UTC().Format("2006-01-02 15:04 UTC"))
	if link := p.link; link != "" {
		body += "\n\n" + viewIn(p.l, link)
	}
	var com struct{ CommentCreate struct{ Success bool } }
	err := l.do(ctx, `mutation($input: CommentCreateInput!) { commentCreate(input: $input) { success } }`,
		map[string]any{"input": map[string]any{"issueId": p.linear, "body": body}}, &com)
	if err != nil {
		return err
	}
	if !com.CommentCreate.Success {
		return errors.New("linear: commentCreate was not successful")
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
		return "", errors.New("linear: the team has no unstarted state")
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
		return fmt.Errorf("linear: unexpected response: %w", err)
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

// linearTargetType es el tipo de estado al que se mueve la issue para cada
// categoría; al reabrir, el primer unstarted del equipo, como en una regresión.
var linearTargetType = map[string]string{
	store.LinkClosed:   "completed",
	store.LinkCanceled: "canceled",
	store.LinkOpen:     "unstarted",
}

type linearState struct {
	ID       string
	Name     string
	Type     string
	Position float64
}

// moveTo pone la issue en el primer estado del equipo de la categoría, salvo
// que ya esté en uno de ella. Devuelve el estado en que queda y si la ha movido.
func (l *linear) moveTo(ctx context.Context, id, category string) (linearState, bool, error) {
	var out struct {
		Issue *struct {
			State linearState
			Team  struct {
				States struct{ Nodes []linearState }
			}
		}
	}
	err := l.do(ctx, `query($id: String!) { issue(id: $id) { state { id name type } team { states { nodes { id name type position } } } } }`,
		map[string]any{"id": id}, &out)
	var gqlErr *gqlErrors
	if (errors.As(err, &gqlErr) && gqlErr.notFound()) || (err == nil && out.Issue == nil) {
		return linearState{}, false, errLinearNotFound
	}
	if err != nil {
		return linearState{}, false, err
	}
	cur := out.Issue.State
	if store.LinearStateCategory(cur.Type) == category {
		return cur, false, nil
	}
	want := linearTargetType[category]
	var best *linearState
	for i, s := range out.Issue.Team.States.Nodes {
		if s.Type == want && (best == nil || s.Position < best.Position) {
			best = &out.Issue.Team.States.Nodes[i]
		}
	}
	if best == nil {
		return cur, false, fmt.Errorf("linear: the team has no %s state", want)
	}
	var upd struct{ IssueUpdate struct{ Success bool } }
	err = l.do(ctx, `mutation($id: String!, $input: IssueUpdateInput!) { issueUpdate(id: $id, input: $input) { success } }`,
		map[string]any{"id": id, "input": map[string]any{"stateId": best.ID}}, &upd)
	if err == nil && !upd.IssueUpdate.Success {
		err = errors.New("linear: issueUpdate was not successful")
	}
	if err != nil {
		return cur, false, err
	}
	return *best, true, nil
}
