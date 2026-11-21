package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/ingest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

const (
	publicURL = "https://errores.test"
	linearKey = "lin_api_prueba"
	teamID    = "team-1"
	todoState = "state-todo"
)

var baseLabels = []string{"label-bug", "label-errores"}

type fakeSlack struct {
	mu       sync.Mutex
	texts    []string
	bodies   []json.RawMessage
	failWith int
	delay    time.Duration
}

func (f *fakeSlack) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	time.Sleep(f.delay)
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "invalid_payload", http.StatusBadRequest)
		return
	}
	var body struct {
		Text   string
		Blocks []json.RawMessage
	}
	if err := json.Unmarshal(raw, &body); err != nil || body.Text == "" || len(body.Blocks) == 0 {
		http.Error(w, "invalid_payload", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != 0 {
		http.Error(w, "fallo de prueba", f.failWith)
		return
	}
	f.texts = append(f.texts, body.Text)
	f.bodies = append(f.bodies, raw)
	_, _ = w.Write([]byte("ok"))
}

func (f *fakeSlack) payloads(t *testing.T) []slackPayload {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]slackPayload, len(f.bodies))
	for i, b := range f.bodies {
		if err := json.Unmarshal(b, &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func (f *fakeSlack) setFail(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = code
}

func (f *fakeSlack) messages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.texts)
}

type linearIssue struct {
	Identifier         string
	Title, Description string
	Labels             []string
	State              string
	Comments           []string
}

type fakeLinear struct {
	mu       sync.Mutex
	issues   map[string]*linearIssue
	order    []string
	failWith int
	delay    time.Duration
	creates  int
	todo     string
}

func newFakeLinear() *fakeLinear {
	return &fakeLinear{issues: map[string]*linearIssue{}, todo: todoState}
}

func (f *fakeLinear) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	time.Sleep(f.delay)
	if r.Header.Get("Authorization") != linearKey {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]any{"errors": []map[string]any{{"message": "Authentication required"}}})
		return
	}
	var req struct {
		Query     string
		Variables map[string]json.RawMessage
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWith != 0 {
		http.Error(w, "fallo de prueba", f.failWith)
		return
	}
	reply := func(data any) { _ = json.NewEncoder(w).Encode(map[string]any{"data": data}) }
	notFound := func() {
		_ = json.NewEncoder(w).Encode(map[string]any{"data": nil,
			"errors": []map[string]any{{"message": "Entity not found: Issue"}}})
	}
	var id string
	if raw, ok := req.Variables["id"]; ok {
		_ = json.Unmarshal(raw, &id)
	}
	q := req.Query
	switch {
	case strings.Contains(q, "issueCreate"):
		var in struct{ Input map[string]any }
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		if in.Input["teamId"] != teamID {
			reply(map[string]any{"issueCreate": map[string]any{"success": false}})
			return
		}
		newID, _ := in.Input["id"].(string)
		if newID == "" {
			newID = fmt.Sprintf("lin-%d", f.creates+1)
		}
		if _, ok := f.issues[newID]; ok {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil,
				"errors": []map[string]any{{"message": "Argument Validation Error: id already exists"}}})
			return
		}
		f.creates++
		is := &linearIssue{Identifier: fmt.Sprintf("ERR-%d", 100+f.creates), Title: in.Input["title"].(string),
			Description: in.Input["description"].(string), State: "state-triage"}
		if ls, ok := in.Input["labelIds"].([]any); ok {
			for _, l := range ls {
				is.Labels = append(is.Labels, l.(string))
			}
		}
		f.issues[newID] = is
		f.order = append(f.order, newID)
		reply(map[string]any{"issueCreate": map[string]any{"success": true, "issue": map[string]any{
			"id": newID, "identifier": is.Identifier, "url": linearURL(is.Identifier)}}})
	case strings.Contains(q, "issueUpdate"):
		var in struct {
			Input struct {
				StateID       string   `json:"stateId"`
				AddedLabelIDs []string `json:"addedLabelIds"`
			}
		}
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		is, ok := f.issues[id]
		if !ok {
			notFound()
			return
		}
		if in.Input.StateID != f.todo && in.Input.StateID != "state-later" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": nil,
				"errors": []map[string]any{{"message": "Entity not found: WorkflowState"}}})
			return
		}
		is.State = in.Input.StateID
		for _, l := range in.Input.AddedLabelIDs {
			if !slices.Contains(is.Labels, l) {
				is.Labels = append(is.Labels, l)
			}
		}
		reply(map[string]any{"issueUpdate": map[string]any{"success": true}})
	case strings.Contains(q, "commentCreate"):
		var in struct {
			Input struct{ IssueID, Body string }
		}
		_ = json.Unmarshal(mustJSON(req.Variables), &in)
		is, ok := f.issues[in.Input.IssueID]
		if !ok {
			notFound()
			return
		}
		is.Comments = append(is.Comments, in.Input.Body)
		reply(map[string]any{"commentCreate": map[string]any{"success": true}})
	case strings.Contains(q, "team("):
		if !strings.Contains(q, `"unstarted"`) {
			http.Error(w, "se esperaba un filtro por tipo", http.StatusBadRequest)
			return
		}
		reply(map[string]any{"team": map[string]any{"states": map[string]any{"nodes": []map[string]any{
			{"id": "state-later", "position": 2.0}, {"id": f.todo, "position": 1.0},
		}}}})
	case strings.Contains(q, "issue("):
		is, ok := f.issues[id]
		if !ok {
			notFound()
			return
		}
		reply(map[string]any{"issue": map[string]any{"id": id, "identifier": is.Identifier, "url": linearURL(is.Identifier),
			"trashed": false, "archivedAt": nil}})
	default:
		http.Error(w, "consulta desconocida: "+q, http.StatusBadRequest)
	}
}

func linearURL(identifier string) string {
	return "https://linear.app/ejemplo/issue/" + identifier
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

func (f *fakeLinear) setFail(code int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failWith = code
}

func (f *fakeLinear) created() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.creates
}

func (f *fakeLinear) snapshot() (map[string]linearIssue, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]linearIssue{}
	for k, v := range f.issues {
		c := *v
		c.Labels = slices.Clone(v.Labels)
		c.Comments = slices.Clone(v.Comments)
		out[k] = c
	}
	return out, slices.Clone(f.order)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

type env struct {
	db      *storetest.DB
	slack   *fakeSlack
	linear  *fakeLinear
	clock   *clock
	alerter *Alerter
	ing     *ingest.Ingester
	project int64
	opts    Options
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// newEnv prepara una base, los dos servidores falsos y un Alerter con los
// canales que deje configure (por defecto, los dos).
func newEnv(t *testing.T, configure func(*Options)) *env {
	t.Helper()
	db := storetest.NewDB(t)
	e := &env{db: db, slack: &fakeSlack{}, linear: newFakeLinear(), clock: &clock{t: time.Now().UTC()}}
	slackSrv := httptest.NewServer(e.slack)
	t.Cleanup(slackSrv.Close)
	linearSrv := httptest.NewServer(e.linear)
	t.Cleanup(linearSrv.Close)
	e.opts = Options{
		PublicURL:       publicURL + "/",
		SlackWebhookURL: slackSrv.URL + "/services/T000/B000/secreto",
		LinearAPIKey:    linearKey,
		LinearAPIURL:    linearSrv.URL,
		LinearTeamID:    teamID,
		LinearLabelIDs:  baseLabels,
	}
	if configure != nil {
		configure(&e.opts)
	}
	e.alerter = e.newAlerter(t)
	e.ing = ingest.NewIngester(db.Pool(), nil, e.alerter, quiet())
	e.project = db.AddProject(t, "backend").ID
	return e
}

func (e *env) newAlerter(t *testing.T) *Alerter {
	t.Helper()
	a, err := New(e.db.Store, quiet(), e.opts)
	if err != nil {
		t.Fatal(err)
	}
	a.now = e.clock.now
	return a
}

func (e *env) ingest(t *testing.T, fixture string) ingest.Result {
	t.Helper()
	ev := storetest.Fixture(t, fixture)
	ev.ID = ""
	res, err := e.ing.Ingest(context.Background(), e.project, &ev)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func (e *env) send(t *testing.T) int {
	t.Helper()
	n, err := e.alerter.SendPending(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return n
}

type outboxRow struct {
	Channel, Kind string
	Attempts      int
	Next          time.Time
	Sent, Failed  bool
	LastError     string
}

func (e *env) outbox(t *testing.T) []outboxRow {
	t.Helper()
	rows, err := e.db.Pool().Query(context.Background(), `
		SELECT channel, kind, attempts, next_attempt_at, sent_at IS NOT NULL, failed_at IS NOT NULL, COALESCE(last_error, '')
		FROM alert_outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (outboxRow, error) {
		var o outboxRow
		err := r.Scan(&o.Channel, &o.Kind, &o.Attempts, &o.Next, &o.Sent, &o.Failed, &o.LastError)
		return o, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestNewRepeatAndRegression(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()

	first := e.ingest(t, "api-tarea-a-1")
	if first.Outcome != ingest.New {
		t.Fatalf("outcome = %v", first.Outcome)
	}
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d, quiero 2", n)
	}
	link := fmt.Sprintf("%s/issues/%d", publicURL, first.IssueID)
	msgs := e.slack.payloads(t)
	if len(msgs) != 1 {
		t.Fatalf("slack = %+v", msgs)
	}
	for _, want := range []string{"*Nuevo error* · backend · production", "*task failed id=88dfc065281046dfa0098d0fa91bbdf4",
		"*Release*\nfixture-release-1", "Ver el error " + link, "ERR-101 en Linear " + linearURL("ERR-101")} {
		if got := flatten(msgs[0]); !strings.Contains(got, want) {
			t.Errorf("slack sin %q:\n%s", want, got)
		}
	}
	if !strings.HasPrefix(msgs[0].Text, "Nuevo error en backend: task failed id=88dfc065281046dfa0098d0fa91bbdf4") {
		t.Errorf("text = %q", msgs[0].Text)
	}
	issues, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("issues de Linear = %d", len(order))
	}
	li := issues[order[0]]
	if !strings.HasPrefix(li.Title, "[backend] task failed id=88dfc065281046dfa0098d0fa91bbdf4") {
		t.Errorf("título = %q", li.Title)
	}
	if !strings.Contains(li.Description, "("+link+")") || !strings.Contains(li.Description, "# task failed") || !strings.Contains(li.Description, "- Proyecto: backend") {
		t.Errorf("descripción:\n%s", li.Description)
	}
	if !slices.Equal(li.Labels, baseLabels) {
		t.Errorf("etiquetas = %v", li.Labels)
	}
	var stored string
	if err := e.db.Pool().QueryRow(ctx, `SELECT linear_issue_id FROM issues WHERE id = $1`, first.IssueID).Scan(&stored); err != nil || stored != order[0] {
		t.Fatalf("linear_issue_id = %q, %v", stored, err)
	}

	// Repetir el error no avisa.
	if res := e.ingest(t, "api-tarea-a-1"); res.Outcome != ingest.Unchanged || res.IssueID != first.IssueID {
		t.Fatalf("repetido: %+v", res)
	}
	if n := e.send(t); n != 0 {
		t.Fatalf("un error repetido envía %d avisos", n)
	}
	if len(e.slack.messages()) != 1 || e.linear.created() != 1 || len(e.outbox(t)) != 2 {
		t.Fatal("un error repetido ha avisado")
	}

	if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	second := e.ingest(t, "api-tarea-b-1")
	if second.Outcome != ingest.Regressed || second.IssueID != first.IssueID {
		t.Fatalf("regresión: %+v", second)
	}
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d, quiero 2", n)
	}
	msgs = e.slack.payloads(t)
	if len(msgs) != 2 || !strings.Contains(flatten(msgs[1]), "*Ha vuelto* · backend · production · se resolvió el ") ||
		!strings.Contains(flatten(msgs[1]), "*Eventos*\n3 en total") {
		t.Fatalf("slack = %+v", msgs)
	}
	issues, order = e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("la regresión ha creado otra issue: %d", len(order))
	}
	li = issues[order[0]]
	if li.State != todoState {
		t.Errorf("estado = %q, quiero el unstarted de menor posición", li.State)
	}
	if len(li.Comments) != 1 || !strings.Contains(li.Comments[0], "release ") || !strings.Contains(li.Comments[0], " UTC") || !strings.Contains(li.Comments[0], link) {
		t.Errorf("comentarios = %q", li.Comments)
	}
	for _, o := range e.outbox(t) {
		if !o.Sent || o.Attempts != 1 || o.LastError != "" {
			t.Errorf("fila = %+v", o)
		}
	}
}

func TestRegressionWithoutLinearIssueCreatesAnother(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	first := e.ingest(t, "api-tarea-a-1")
	e.send(t)
	_, order := e.linear.snapshot()
	e.linear.mu.Lock()
	delete(e.linear.issues, order[0])
	e.linear.mu.Unlock()

	if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-b-1")
	e.send(t)
	issues, order := e.linear.snapshot()
	if len(issues) != 1 || len(order) != 2 {
		t.Fatalf("issues = %v, creadas %v", issues, order)
	}
	var stored string
	if err := e.db.Pool().QueryRow(ctx, `SELECT linear_issue_id FROM issues WHERE id = $1`, first.IssueID).Scan(&stored); err != nil || stored != order[1] {
		t.Fatalf("linear_issue_id = %q, %v", stored, err)
	}
}

func TestDeletedStateRetriesWithoutDuplicating(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	ctx := context.Background()
	first := e.ingest(t, "api-tarea-a-1")
	regress := func() {
		if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
			t.Fatal(err)
		}
		e.ingest(t, "api-tarea-b-1")
	}
	regress()
	e.send(t)

	// El estado unstarted que el alerter tiene guardado se borra en Linear.
	e.linear.mu.Lock()
	e.linear.todo = "state-todo-nuevo"
	e.linear.mu.Unlock()
	regress()
	e.send(t)
	rows := e.outbox(t)
	last := rows[len(rows)-1]
	if last.Sent || last.Failed || last.Attempts != 1 || !strings.Contains(last.LastError, "not found") {
		t.Fatalf("fila = %+v", last)
	}
	e.clock.set(e.clock.now().Add(time.Minute))
	e.send(t)
	issues, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("un estado borrado ha creado %d issues", len(order))
	}
	li := issues[order[0]]
	if li.State != "state-todo-nuevo" || len(li.Comments) != 2 {
		t.Fatalf("issue = %+v", li)
	}
}

func TestCreateRetryReusesLinearIssue(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	ctx := context.Background()
	// Falla el guardado del id después de crear la issue en Linear, como si se
	// cayera Postgres o se parara la réplica entre la petición y el commit.
	if _, err := e.db.Pool().Exec(ctx, `
		CREATE FUNCTION rompe() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'caída'; END $$;
		CREATE TRIGGER rompe BEFORE UPDATE OF linear_issue_id ON issues FOR EACH ROW EXECUTE FUNCTION rompe();`); err != nil {
		t.Fatal(err)
	}
	first := e.ingest(t, "api-tarea-a-1")
	if _, err := e.alerter.SendPending(ctx); err == nil {
		t.Fatal("el guardado debería fallar")
	}
	if e.linear.created() != 1 {
		t.Fatalf("creadas = %d", e.linear.created())
	}
	if _, err := e.db.Pool().Exec(ctx, `DROP TRIGGER rompe ON issues`); err != nil {
		t.Fatal(err)
	}
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	_, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("el reintento ha creado otra issue: %v", order)
	}
	var stored string
	if err := e.db.Pool().QueryRow(ctx, `SELECT linear_issue_id FROM issues WHERE id = $1`, first.IssueID).Scan(&stored); err != nil || stored != order[0] {
		t.Fatalf("linear_issue_id = %q, %v", stored, err)
	}
	if len(stored) != 36 || strings.Count(stored, "-") != 4 {
		t.Errorf("issueCreate sin id propio: %q", stored)
	}
}

type failAfter struct{ n ingest.Notifier }

func (f failAfter) IssueChanged(ctx context.Context, tx pgx.Tx, ev *event.Event, res ingest.Result) error {
	if err := f.n.IssueChanged(ctx, tx, ev, res); err != nil {
		return err
	}
	return errors.New("fallo después de encolar")
}

func TestRollbackLeavesNothing(t *testing.T) {
	e := newEnv(t, nil)
	ing := ingest.NewIngester(e.db.Pool(), nil, failAfter{e.alerter}, quiet())
	ev := storetest.Fixture(t, "api-tarea-a-1")
	if _, err := ing.Ingest(context.Background(), e.project, &ev); err == nil {
		t.Fatal("la ingesta debería fallar")
	}
	if rows := e.outbox(t); len(rows) != 0 {
		t.Fatalf("outbox tras rollback = %+v", rows)
	}
	if n := e.send(t); n != 0 || len(e.slack.messages()) != 0 {
		t.Fatal("se ha avisado de un evento que no se guardó")
	}
}

func TestQueueFailureDoesNotFailIngest(t *testing.T) {
	e := newEnv(t, nil)
	if _, err := e.db.Pool().Exec(context.Background(), `ALTER TABLE alert_outbox ADD CONSTRAINT rompe CHECK (false)`); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(t, "api-tarea-a-1"); res.Outcome != ingest.New {
		t.Fatalf("res = %+v", res)
	}
	var n int
	if err := e.db.Pool().QueryRow(context.Background(), `SELECT count(*) FROM events`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("eventos = %d, %v", n, err)
	}
}

func TestRetriesWithBackoffAndGivesUp(t *testing.T) {
	e := newEnv(t, nil)
	e.slack.setFail(http.StatusInternalServerError)
	e.linear.setFail(http.StatusServiceUnavailable)
	e.ingest(t, "api-tarea-a-1")
	// Aquí se miden los reintentos de cada canal por separado: Slack no espera a Linear.
	if _, err := e.db.Pool().Exec(context.Background(), `UPDATE alert_outbox SET waits_for = NULL`); err != nil {
		t.Fatal(err)
	}

	want := []time.Duration{time.Minute, 5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour,
		12 * time.Hour, 24 * time.Hour, 24 * time.Hour, 24 * time.Hour}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		now := e.clock.now()
		if n := e.send(t); n != 2 {
			t.Fatalf("intento %d: enviados = %d", attempt, n)
		}
		rows := e.outbox(t)
		for _, o := range rows {
			if o.Attempts != attempt || o.Sent || o.LastError == "" {
				t.Fatalf("intento %d: %+v", attempt, o)
			}
			if attempt < MaxAttempts {
				if d := o.Next.Sub(now.Add(want[attempt-1])).Abs(); o.Failed || d > time.Microsecond {
					t.Fatalf("intento %d: siguiente %v, quiero %v", attempt, o.Next.Sub(now), want[attempt-1])
				}
			} else if !o.Failed {
				t.Fatalf("intento %d: no se ha abandonado: %+v", attempt, o)
			}
		}
		if attempt < MaxAttempts {
			e.clock.set(now.Add(want[attempt-1] - time.Second))
			if n := e.send(t); n != 0 {
				t.Fatalf("intento %d: se reintenta antes de tiempo", attempt)
			}
			e.clock.set(now.Add(want[attempt-1]))
		}
	}
	rows := e.outbox(t)
	if !strings.Contains(rows[0].LastError, "500") || !strings.Contains(rows[1].LastError, "503") {
		t.Errorf("errores = %q, %q", rows[0].LastError, rows[1].LastError)
	}
	e.slack.setFail(0)
	e.linear.setFail(0)
	e.clock.set(e.clock.now().Add(30 * 24 * time.Hour))
	if n := e.send(t); n != 0 || len(e.slack.messages()) != 0 {
		t.Fatal("se ha reintentado un aviso abandonado")
	}
}

func TestErrorDoesNotLeakWebhookURL(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	url := dead.URL + "/services/T000/B000/secreto"
	dead.Close()
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = url; o.LinearAPIKey = "" })
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	rows := e.outbox(t)
	if len(rows) != 1 || rows[0].LastError == "" || strings.Contains(rows[0].LastError, "secreto") {
		t.Fatalf("filas = %+v", rows)
	}
}

func TestRegressionWaitsForPendingNew(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	e.linear.setFail(http.StatusInternalServerError)
	first := e.ingest(t, "api-tarea-a-1")
	e.send(t)
	if _, err := e.db.SetIssueStatus(context.Background(), first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-b-1")
	if n := e.send(t); n != 0 {
		t.Fatalf("la regresión se ha enviado antes que el aviso de error nuevo")
	}
	e.linear.setFail(0)
	e.clock.set(e.clock.now().Add(time.Minute))
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}
	issues, order := e.linear.snapshot()
	if len(order) != 1 || len(issues[order[0]].Comments) != 1 {
		t.Fatalf("issues = %v", issues)
	}
}

func TestConcurrentLoopsSendOnce(t *testing.T) {
	e := newEnv(t, nil)
	e.slack.delay = 5 * time.Millisecond
	e.linear.delay = 5 * time.Millisecond
	const n = 15
	for i := range n {
		ev := event.Event{Level: "error", Message: "fallo de prueba", Fingerprint: []string{fmt.Sprint("huella-", i)}}
		if _, err := e.ing.Ingest(context.Background(), e.project, &ev); err != nil {
			t.Fatal(err)
		}
	}
	other := e.newAlerter(t)
	var wg sync.WaitGroup
	counts := make([]int, 2)
	for i, a := range []*Alerter{e.alerter, other} {
		wg.Go(func() {
			c, err := a.SendPending(context.Background())
			if err != nil {
				t.Error(err)
			}
			counts[i] = c
		})
	}
	wg.Wait()
	if counts[0]+counts[1] != 2*n || counts[0] == 0 || counts[1] == 0 {
		t.Errorf("enviados por bucle = %v", counts)
	}
	if got := len(e.slack.messages()); got != n {
		t.Errorf("mensajes de slack = %d, quiero %d", got, n)
	}
	if e.linear.created() != n {
		t.Errorf("issues de Linear = %d, quiero %d", e.linear.created(), n)
	}
}

func TestUnconfiguredChannelsAreNotQueued(t *testing.T) {
	cases := map[string]struct {
		configure func(*Options)
		want      []string
	}{
		"sin slack":  {func(o *Options) { o.SlackWebhookURL = "" }, []string{"linear"}},
		"sin linear": {func(o *Options) { o.LinearAPIKey = "" }, []string{"slack"}},
		"ninguno":    {func(o *Options) { o.SlackWebhookURL, o.LinearAPIKey = "", "" }, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t, c.configure)
			e.ingest(t, "api-tarea-a-1")
			var got []string
			for _, o := range e.outbox(t) {
				got = append(got, o.Channel)
			}
			if !slices.Equal(got, c.want) {
				t.Fatalf("encolado = %v, quiero %v", got, c.want)
			}
			e.send(t)
			if slices.Contains(c.want, "slack") != (len(e.slack.messages()) == 1) {
				t.Error("slack no coincide con su configuración")
			}
			if slices.Contains(c.want, "linear") != (e.linear.created() == 1) {
				t.Error("linear no coincide con su configuración")
			}
		})
	}
}

func TestLinearNeedsTeam(t *testing.T) {
	if _, err := New(nil, quiet(), Options{LinearAPIKey: "x"}); err == nil {
		t.Fatal("LINEAR_API_KEY sin LINEAR_TEAM_ID debería fallar")
	}
}

func TestProjectLabels(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	// label-errores ya está en LINEAR_LABEL_IDS: no se repite.
	if err := e.db.SetProjectLabels(ctx, "backend", []string{"label-equipo", "label-errores", "label-prioridad"}); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-a-1")

	other := e.db.AddProject(t, "web")
	ev := storetest.Fixture(t, "web-typeerror-1")
	if _, err := e.ing.Ingest(ctx, other.ID, &ev); err != nil {
		t.Fatal(err)
	}
	e.send(t)

	issues, order := e.linear.snapshot()
	if len(order) != 2 {
		t.Fatalf("issues = %d", len(order))
	}
	want := []string{"label-bug", "label-errores", "label-equipo", "label-prioridad"}
	if l := issues[order[0]].Labels; !slices.Equal(l, want) {
		t.Errorf("con etiquetas propias: etiquetas = %v; quiero %v", l, want)
	}
	if l := issues[order[1]].Labels; !slices.Equal(l, baseLabels) {
		t.Errorf("sin etiquetas propias: etiquetas = %v", l)
	}
}

func TestProjectLabelsWithoutGlobal(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.LinearLabelIDs = nil })
	if err := e.db.SetProjectLabels(context.Background(), "backend", []string{"label-equipo"}); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	issues, order := e.linear.snapshot()
	if len(order) != 1 || !slices.Equal(issues[order[0]].Labels, []string{"label-equipo"}) {
		t.Fatalf("issues = %+v", issues)
	}
}

func TestRegressionAddsProjectLabels(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	first := e.ingest(t, "api-tarea-a-1")
	e.send(t)
	issues, order := e.linear.snapshot()
	if len(order) != 1 || !slices.Equal(issues[order[0]].Labels, baseLabels) {
		t.Fatalf("issue nueva: %+v", issues)
	}

	if err := e.db.SetProjectLabels(ctx, "backend", []string{"label-equipo", "label-bug"}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(t, "api-tarea-b-1"); res.Outcome != ingest.Regressed {
		t.Fatalf("res = %+v", res)
	}
	e.send(t)

	issues, order = e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("la regresión ha creado otra issue: %d", len(order))
	}
	li := issues[order[0]]
	want := []string{"label-bug", "label-errores", "label-equipo"}
	if !slices.Equal(li.Labels, want) || li.State != todoState || len(li.Comments) != 1 {
		t.Errorf("tras la regresión: etiquetas = %v (quiero %v), estado = %q, comentarios = %d", li.Labels, want, li.State, len(li.Comments))
	}
}

func buttons(p slackPayload) []string {
	var out []string
	for _, b := range p.Blocks {
		if b.Type != "actions" {
			continue
		}
		for _, el := range b.Elements {
			m := el.(map[string]any)
			out = append(out, m["text"].(map[string]any)["text"].(string))
		}
	}
	return out
}

func TestSlackWaitsForLinear(t *testing.T) {
	e := newEnv(t, nil)
	e.linear.setFail(http.StatusInternalServerError)
	e.ingest(t, "api-tarea-a-1")

	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d; solo debería intentarse Linear", n)
	}
	if len(e.slack.messages()) != 0 {
		t.Fatal("Slack no ha esperado a Linear")
	}
	e.linear.setFail(0)
	e.clock.set(e.clock.now().Add(time.Minute))
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}
	msgs := e.slack.payloads(t)
	if len(msgs) != 1 || !slices.Equal(buttons(msgs[0]), []string{"Ver el error", "ERR-101 en Linear"}) {
		t.Fatalf("botones = %v", buttons(msgs[0]))
	}
}

func TestSlackStopsWaitingForLinear(t *testing.T) {
	e := newEnv(t, nil)
	e.linear.setFail(http.StatusInternalServerError)
	start := e.clock.now()
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	e.clock.set(start.Add(time.Minute))
	e.send(t)
	e.clock.set(start.Add(SlackWaitsForLinear - time.Second))
	e.send(t)
	if len(e.slack.messages()) != 0 {
		t.Fatal("Slack ha salido antes del tope")
	}
	e.clock.set(start.Add(SlackWaitsForLinear))
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	msgs := e.slack.payloads(t)
	if len(msgs) != 1 || !slices.Equal(buttons(msgs[0]), []string{"Ver el error"}) {
		t.Fatalf("mensajes = %+v", msgs)
	}
}

func TestResolvedAt(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	id := e.ingest(t, "api-tarea-a-1").IssueID
	resolvedAt := func() *time.Time {
		var at *time.Time
		if err := e.db.Pool().QueryRow(ctx, `SELECT resolved_at FROM issues WHERE id = $1`, id).Scan(&at); err != nil {
			t.Fatal(err)
		}
		return at
	}
	set := func(status string) {
		if _, err := e.db.SetIssueStatus(ctx, id, status); err != nil {
			t.Fatal(err)
		}
	}
	if resolvedAt() != nil {
		t.Fatal("un issue nuevo no está resuelto")
	}
	set("resolved")
	first := resolvedAt()
	if first == nil {
		t.Fatal("resolved sin resolved_at")
	}
	set("resolved_next")
	if at := resolvedAt(); at == nil || !at.Equal(*first) {
		t.Errorf("pasar de resolved a resolved_next cambia resolved_at: %v → %v", first, at)
	}
	set("open")
	if resolvedAt() != nil {
		t.Error("reabrir no borra resolved_at")
	}
	set("resolved")
	res := e.ingest(t, "api-tarea-b-1")
	if res.Outcome != ingest.Regressed || res.ResolvedAt == nil || resolvedAt() != nil {
		t.Errorf("regresión: %+v, resolved_at = %v", res, resolvedAt())
	}
}

// TestEndToEndTaskFailed manda el primer task failed del worker y, tras
// resolverlo, el segundo.
func TestEndToEndTaskFailed(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	first := e.ingest(t, "api-tarea-a-1")
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}
	if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(t, "api-tarea-b-1"); res.Outcome != ingest.Regressed {
		t.Fatalf("res = %+v", res)
	}
	if n := e.send(t); n != 2 {
		t.Fatalf("enviados = %d", n)
	}

	e.slack.mu.Lock()
	bodies := slices.Clone(e.slack.bodies)
	e.slack.mu.Unlock()
	if dir := os.Getenv("TEST_EVIDENCE_DIR"); dir != "" {
		for i, b := range bodies {
			var pretty bytes.Buffer
			_ = json.Indent(&pretty, b, "", "  ")
			name := filepath.Join(dir, fmt.Sprintf("slack-falso-%d-%s.json", i+1, []string{"nuevo", "regresion"}[i]))
			if err := os.WriteFile(name, append(pretty.Bytes(), '\n'), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	link := fmt.Sprintf("%s/issues/%d", publicURL, first.IssueID)
	msgs := e.slack.payloads(t)
	if len(msgs) != 2 {
		t.Fatalf("mensajes = %d", len(msgs))
	}
	today := spanishDate(e.clock.now(), e.clock.now())
	want := []struct {
		text   string
		blocks []string
		parts  []string
	}{
		{
			"Nuevo error en backend: task failed id=88dfc065281046dfa0098d0fa91bbdf4",
			[]string{"context", "section", "section", "actions"},
			[]string{"*Nuevo error* · backend · production\n", "*task failed id=88dfc065281046dfa0098d0fa91bbdf4 name='enviar_factura'",
				"*Release*\nfixture-release-1\n", "Ver el error " + link, "ERR-101 en Linear " + linearURL("ERR-101")},
		},
		{
			"Ha vuelto un error en backend: task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35",
			[]string{"context", "section", "section", "actions"},
			[]string{"*Ha vuelto* · backend · production · se resolvió el " + today + "\n", "*task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35",
				"*Release*\nfixture-release-1\n", "*Eventos*\n2 en total\n", "Ver el error " + link, "ERR-101 en Linear " + linearURL("ERR-101")},
		},
	}
	for i, m := range msgs {
		if !strings.HasPrefix(m.Text, want[i].text) {
			t.Errorf("%d: text = %q", i, m.Text)
		}
		var types []string
		for _, b := range m.Blocks {
			types = append(types, b.Type)
		}
		if !slices.Equal(types, want[i].blocks) {
			t.Errorf("%d: bloques = %v", i, types)
		}
		all := flatten(m)
		for _, p := range want[i].parts {
			if !strings.Contains(all, p) {
				t.Errorf("%d: falta %q en\n%s", i, p, all)
			}
		}
		if strings.Contains(string(bodies[i]), "attachments") || strings.Contains(string(bodies[i]), `"color"`) {
			t.Errorf("%d: lleva attachments", i)
		}
	}
}

// Pasado el tope por issue, los eventos repetidos siguen sin avisar: un solo
// aviso por canal para todo el bucle.
func TestLoopPastIssueLimitAlertsOnce(t *testing.T) {
	e := newEnv(t, nil)
	const n = 3 * store.DefaultEventsPerIssuePerHour
	var issue int64
	for i := range n {
		res := e.ingest(t, "api-excepcion-1")
		if i == 0 {
			issue = res.IssueID
		}
		if (i == 0) != (res.Outcome == ingest.New) || res.IssueID != issue {
			t.Fatalf("evento %d: %+v", i, res)
		}
	}
	rows := e.outbox(t)
	if len(rows) != 2 || rows[0].Kind != "new" || rows[1].Kind != "new" {
		t.Fatalf("outbox = %+v", rows)
	}
	var count, stored int64
	if err := e.db.Pool().QueryRow(context.Background(), `
		SELECT event_count, (SELECT count(*) FROM events WHERE issue_id = $1) FROM issues WHERE id = $1`, issue).Scan(&count, &stored); err != nil {
		t.Fatal(err)
	}
	// Si el bucle cruza una hora en punto, el tope vuelve a empezar.
	if count != n || stored > 2*store.DefaultEventsPerIssuePerHour {
		t.Errorf("count %d, guardados %d", count, stored)
	}
	if sent := e.send(t); sent != 2 {
		t.Errorf("%d avisos enviados, quiero 2 (Slack y Linear)", sent)
	}
}
