package fakes

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	LinearTeamID = "team-errores"
	LinearOrgID  = "org-ejemplo"
)

type LinearState struct {
	ID       string  `json:"id"`
	Name     string  `json:"name"`
	Type     string  `json:"type"`
	Color    string  `json:"color"`
	Position float64 `json:"position"`
}

// LinearStates son los estados del equipo, como los que trae Linear por defecto.
var LinearStates = []LinearState{
	{ID: "state-triage", Name: "Triage", Type: "triage", Color: "#FC7840", Position: 0},
	{ID: "state-backlog", Name: "Backlog", Type: "backlog", Color: "#bec2c8", Position: 0},
	{ID: "state-todo", Name: "Todo", Type: "unstarted", Color: "#e2e2e2", Position: 1},
	{ID: "state-progress", Name: "In Progress", Type: "started", Color: "#f2c94c", Position: 2},
	{ID: "state-done", Name: "Done", Type: "completed", Color: "#5e6ad2", Position: 3},
	{ID: "state-canceled", Name: "Canceled", Type: "canceled", Color: "#95a2b3", Position: 4},
}

func linearState(id string) (LinearState, bool) {
	i := slices.IndexFunc(LinearStates, func(s LinearState) bool { return s.ID == id || s.Name == id })
	if i < 0 {
		return LinearState{}, false
	}
	return LinearStates[i], true
}

type LinearIssue struct {
	ID          string
	Identifier  string
	Number      int
	Title       string
	Description string
	LabelIDs    []string
	StateID     string
	Comments    []string
	CreatedAt   time.Time
	UpdatedAt   time.Time
	CompletedAt *time.Time
	CanceledAt  *time.Time
}

// LinearActor es quien hace los cambios: la persona de la clave de la API o
// alguien en la web de Linear.
type LinearActor struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Linear hace de la API GraphQL de Linear y manda sus webhooks de issues.
type Linear struct {
	APIKey string
	Secret string
	// APIUser es quien firma los cambios hechos con la clave de la API.
	APIUser LinearActor
	// WebhookURL es a dónde manda los webhooks.
	WebhookURL string
	// Now fecha los webhooks; por defecto, time.Now.
	Now func() time.Time

	mu     sync.Mutex
	issues map[string]*LinearIssue
	order  []string
	out    *outbox
	log    *Log
}

func NewLinear(log *Log, apiKey, secret string) *Linear {
	l := &Linear{APIKey: apiKey, Secret: secret, issues: map[string]*LinearIssue{}, log: log, Now: time.Now,
		APIUser: LinearActor{ID: "user-bot", Name: "Bot de errores"}}
	l.out = newOutbox("linear", log, func() string { return l.WebhookURL })
	return l
}

// Async manda los webhooks en segundo plano; sin él, con Flush.
func (l *Linear) Async()                { l.out.startAsync() }
func (l *Linear) Flush() ([]int, error) { return l.out.Flush() }
func (l *Linear) PendingWebhooks() int  { return l.out.Pending() }
func (l *Linear) URL(identifier string) string {
	return "https://linear.app/ejemplo/issue/" + identifier
}

// Handler es la API: POST /graphql.
func (l *Linear) Handler() http.Handler {
	return logged("linear", l.log, l.graphql)
}

// Issue devuelve una copia de la issue por su id o su identificador.
func (l *Linear) Issue(ref string) (LinearIssue, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	is := l.find(ref)
	if is == nil {
		return LinearIssue{}, false
	}
	c := *is
	c.Comments = slices.Clone(is.Comments)
	return c, true
}

func (l *Linear) Issues() []LinearIssue {
	l.mu.Lock()
	ids := slices.Clone(l.order)
	l.mu.Unlock()
	var out []LinearIssue
	for _, id := range ids {
		is, _ := l.Issue(id)
		out = append(out, is)
	}
	return out
}

func (l *Linear) find(ref string) *LinearIssue {
	if is, ok := l.issues[ref]; ok {
		return is
	}
	for _, is := range l.issues {
		if is.Identifier == ref {
			return is
		}
	}
	return nil
}

// SetState mueve la issue como lo haría alguien en Linear y manda el webhook.
func (l *Linear) SetState(ref, state string, by LinearActor) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	is := l.find(ref)
	if is == nil {
		return fmt.Errorf("linear: no existe la issue %s", ref)
	}
	st, ok := linearState(state)
	if !ok {
		return fmt.Errorf("linear: no existe el estado %s", state)
	}
	l.moveLocked(is, st, by)
	return nil
}

func (l *Linear) moveLocked(is *LinearIssue, st LinearState, by LinearActor) {
	if is.StateID == st.ID {
		return
	}
	now := l.Now().UTC()
	from := map[string]any{"stateId": is.StateID, "updatedAt": is.UpdatedAt.Format(time.RFC3339Nano)}
	old, _ := linearState(is.StateID)
	if old.Type == "completed" {
		from["completedAt"] = is.CompletedAt.Format(time.RFC3339Nano)
		is.CompletedAt = nil
	}
	if old.Type == "canceled" {
		from["canceledAt"] = is.CanceledAt.Format(time.RFC3339Nano)
		is.CanceledAt = nil
	}
	switch st.Type {
	case "completed":
		is.CompletedAt = &now
		from["completedAt"] = nil
	case "canceled":
		is.CanceledAt = &now
		from["canceledAt"] = nil
	}
	is.StateID = st.ID
	is.UpdatedAt = now
	l.hookLocked("update", is, from, by)
}

// hookLocked deja en la cola el webhook de la issue, con la forma de los de Linear.
func (l *Linear) hookLocked(action string, is *LinearIssue, from map[string]any, by LinearActor) {
	st, _ := linearState(is.StateID)
	ts := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.Format(time.RFC3339Nano)
	}
	now := l.Now()
	payload := map[string]any{
		"action":    action,
		"type":      "Issue",
		"createdAt": now.UTC().Format(time.RFC3339Nano),
		"actor": map[string]any{"id": by.ID, "type": "user", "name": by.Name,
			"email": strings.ToLower(strings.ReplaceAll(by.Name, " ", ".")) + "@ejemplo.com", "url": "https://linear.app/ejemplo/profiles/" + by.ID},
		"data": map[string]any{
			"id": is.ID, "identifier": is.Identifier, "number": is.Number, "title": is.Title, "description": is.Description,
			"url": l.URL(is.Identifier), "priority": 0, "priorityLabel": "No priority", "labelIds": is.LabelIDs,
			"stateId": is.StateID, "teamId": LinearTeamID,
			"state":     map[string]any{"id": st.ID, "name": st.Name, "type": st.Type, "color": st.Color},
			"team":      map[string]any{"id": LinearTeamID, "key": "ERR", "name": "Errores"},
			"createdAt": is.CreatedAt.Format(time.RFC3339Nano), "updatedAt": is.UpdatedAt.Format(time.RFC3339Nano),
			"completedAt": ts(is.CompletedAt), "canceledAt": ts(is.CanceledAt),
		},
		"url":              l.URL(is.Identifier),
		"organizationId":   LinearOrgID,
		"webhookTimestamp": now.UnixMilli(),
		"webhookId":        "webhook-errores",
	}
	if from != nil {
		payload["updatedFrom"] = from
	}
	body, _ := json.Marshal(payload)
	m := hmac.New(sha256.New, []byte(l.Secret))
	m.Write(body)
	l.out.push(hook{event: "Issue." + action, body: body, headers: map[string]string{
		"Content-Type":     "application/json; charset=utf-8",
		"User-Agent":       "Linear-Webhook",
		"Linear-Delivery":  newUUID(),
		"Linear-Event":     "Issue",
		"Linear-Signature": hex.EncodeToString(m.Sum(nil)),
	}})
}

func (l *Linear) graphql(w http.ResponseWriter, r *http.Request, body []byte) {
	if r.Method != http.MethodPost {
		http.Error(w, "solo POST", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get("Authorization") != l.APIKey {
		writeJSON(w, http.StatusUnauthorized, map[string]any{"errors": []map[string]any{{"message": "Authentication required, not authenticated"}}})
		return
	}
	var req struct {
		Query     string                     `json:"query"`
		Variables map[string]json.RawMessage `json:"variables"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var id string
	if raw, ok := req.Variables["id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	reply := func(data any) { writeJSON(w, http.StatusOK, map[string]any{"data": data}) }
	fail := func(msg string) {
		writeJSON(w, http.StatusOK, map[string]any{"data": nil, "errors": []map[string]any{{"message": msg}}})
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	q := req.Query
	switch {
	case strings.Contains(q, "issueCreate"):
		var in struct {
			Input struct {
				ID          string   `json:"id"`
				TeamID      string   `json:"teamId"`
				Title       string   `json:"title"`
				Description string   `json:"description"`
				LabelIDs    []string `json:"labelIds"`
			} `json:"input"`
		}
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		if in.Input.TeamID != LinearTeamID {
			fail("Entity not found: Team")
			return
		}
		newID := in.Input.ID
		if newID == "" {
			newID = newUUID()
		}
		if _, ok := l.issues[newID]; ok {
			fail("Argument Validation Error: id already exists")
			return
		}
		now := l.Now().UTC()
		n := len(l.issues) + 1
		is := &LinearIssue{ID: newID, Identifier: fmt.Sprintf("ERR-%d", n), Number: n, Title: in.Input.Title,
			Description: in.Input.Description, LabelIDs: in.Input.LabelIDs, StateID: "state-triage", CreatedAt: now, UpdatedAt: now}
		l.issues[newID] = is
		l.order = append(l.order, newID)
		l.hookLocked("create", is, nil, l.APIUser)
		reply(map[string]any{"issueCreate": map[string]any{"success": true, "issue": map[string]any{
			"id": is.ID, "identifier": is.Identifier, "url": l.URL(is.Identifier)}}})
	case strings.Contains(q, "issueUpdate"):
		var in struct {
			Input struct {
				StateID string `json:"stateId"`
			} `json:"input"`
		}
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		is := l.find(id)
		if is == nil {
			fail("Entity not found: Issue")
			return
		}
		st, ok := linearState(in.Input.StateID)
		if !ok {
			fail("Entity not found: WorkflowState")
			return
		}
		l.moveLocked(is, st, l.APIUser)
		reply(map[string]any{"issueUpdate": map[string]any{"success": true}})
	case strings.Contains(q, "issueUnarchive"):
		reply(map[string]any{"issueUnarchive": map[string]any{"success": true}})
	case strings.Contains(q, "commentCreate"):
		var in struct {
			Input struct {
				IssueID string `json:"issueId"`
				Body    string `json:"body"`
			} `json:"input"`
		}
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		is := l.find(in.Input.IssueID)
		if is == nil {
			fail("Entity not found: Issue")
			return
		}
		is.Comments = append(is.Comments, in.Input.Body)
		reply(map[string]any{"commentCreate": map[string]any{"success": true}})
	case strings.Contains(q, "team("):
		var nodes []LinearState
		for _, s := range LinearStates {
			if !strings.Contains(q, `"unstarted"`) || s.Type == "unstarted" {
				nodes = append(nodes, s)
			}
		}
		reply(map[string]any{"team": map[string]any{"states": map[string]any{"nodes": nodes}}})
	case strings.Contains(q, "issue("):
		is := l.find(id)
		if is == nil {
			fail("Entity not found: Issue - Could not find referenced Issue.")
			return
		}
		st, _ := linearState(is.StateID)
		reply(map[string]any{"issue": map[string]any{"id": is.ID, "identifier": is.Identifier, "url": l.URL(is.Identifier),
			"trashed": false, "archivedAt": nil, "state": st,
			"team": map[string]any{"id": LinearTeamID, "states": map[string]any{"nodes": LinearStates}}}})
	default:
		http.Error(w, "consulta desconocida", http.StatusBadRequest)
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
