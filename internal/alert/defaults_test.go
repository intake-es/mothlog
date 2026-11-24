package alert

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/ingest"
	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

func (e *env) setDefaults(t *testing.T, slack, linear bool) {
	t.Helper()
	if _, err := e.db.SetDefaultAlerts(context.Background(), e.project, store.DefaultAlerts{Slack: slack, Linear: linear}); err != nil {
		t.Fatal(err)
	}
	e.alerter.DefaultsChanged(e.project)
}

// queued son los canales de los avisos encolados y si esperan a otro aviso.
func (e *env) queued(t *testing.T) []string {
	t.Helper()
	rows, err := e.db.Pool().Query(context.Background(), `SELECT channel, waits_for IS NOT NULL FROM alert_outbox ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := db.CollectRows(rows, func(r db.CollectableRow) (string, error) {
		var ch string
		var waits bool
		err := r.Scan(&ch, &waits)
		if waits {
			ch += " (espera)"
		}
		return ch, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestDefaultsOnAsToday(t *testing.T) {
	e := newEnv(t, nil)
	d, err := e.db.DefaultAlerts(context.Background(), e.project)
	if err != nil || !d.Slack || !d.Linear {
		t.Fatalf("un proyecto nuevo: %+v, %v", d, err)
	}
	e.ingest(t, "api-tarea-a-1")
	if q := e.queued(t); !slices.Equal(q, []string{"slack (espera)", "linear"}) {
		t.Fatalf("encolados = %v", q)
	}
	e.send(t)
	msgs := e.slack.payloads(t)
	if e.linear.created() != 1 || len(msgs) != 1 || !slices.Equal(buttons(msgs[0]), []string{"View error", "ERR-101 in Linear"}) {
		t.Fatalf("linear = %d, slack = %+v", e.linear.created(), msgs)
	}
}

func TestDefaultLinearOff(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	// El primer error lee los canales: apagar después tiene que valer al momento.
	e.ingest(t, "api-tarea-a-1")
	e.setDefaults(t, true, false)
	if _, err := e.db.Pool().Exec(ctx, `DELETE FROM alert_outbox`); err != nil {
		t.Fatal(err)
	}

	first := e.ingest(t, "api-validacion-1")
	if first.Outcome != ingest.New {
		t.Fatalf("outcome = %v", first.Outcome)
	}
	if q := e.queued(t); !slices.Equal(q, []string{"slack"}) {
		t.Fatalf("encolados = %v; quiero solo Slack, sin esperar", q)
	}
	if n := e.send(t); n != 1 {
		t.Fatalf("enviados = %d", n)
	}
	msgs := e.slack.payloads(t)
	if len(msgs) != 1 || !slices.Equal(buttons(msgs[0]), []string{"View error"}) {
		t.Fatalf("slack = %+v", msgs)
	}
	if e.linear.created() != 0 {
		t.Fatalf("Linear apagado ha creado %d issues", e.linear.created())
	}

	// Una regresión tampoco va a Linear.
	if _, err := e.db.SetIssueStatus(ctx, first.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	if res := e.ingest(t, "api-validacion-1"); res.Outcome != ingest.Regressed {
		t.Fatalf("regresión: %v", res.Outcome)
	}
	e.send(t)
	if len(e.slack.messages()) != 2 || e.linear.created() != 0 {
		t.Fatalf("slack = %d, linear = %d", len(e.slack.messages()), e.linear.created())
	}

	// El otro proyecto sigue como estaba.
	other := e.db.AddProject(t, "web")
	ev := storetest.Fixture(t, "web-typeerror-1")
	if _, err := e.ing.Ingest(ctx, other.ID, &ev); err != nil {
		t.Fatal(err)
	}
	e.send(t)
	if e.linear.created() == 0 {
		t.Fatal("apagar Linear en un proyecto lo ha apagado en otro")
	}
}

func TestDefaultSlackOffAndBothOff(t *testing.T) {
	e := newEnv(t, nil)
	e.setDefaults(t, false, true)
	e.ingest(t, "api-tarea-a-1")
	if q := e.queued(t); !slices.Equal(q, []string{"linear"}) {
		t.Fatalf("encolados = %v", q)
	}
	e.send(t)
	if len(e.slack.messages()) != 0 || e.linear.created() != 1 {
		t.Fatalf("slack = %d, linear = %d", len(e.slack.messages()), e.linear.created())
	}

	e.setDefaults(t, false, false)
	e.ingest(t, "api-validacion-1")
	if q := e.queued(t); len(q) != 1 {
		t.Fatalf("con todo apagado se ha encolado: %v", q)
	}
}

func TestDefaultsIgnoredWithRules(t *testing.T) {
	e := newEnv(t, nil)
	e.setDefaults(t, false, false)
	e.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true},
		Actions: []store.RuleAction{{Type: store.ActionLinear}, {Type: store.ActionSlack, URL: e.opts.SlackWebhookURL}}})
	e.ingest(t, "api-tarea-a-1")
	e.send(t)
	if e.linear.created() != 1 || len(e.slack.messages()) != 1 {
		t.Fatalf("con reglas: linear = %d, slack = %d", e.linear.created(), len(e.slack.messages()))
	}

	// Encenderlos tampoco cambia nada: avisa a donde digan las reglas.
	e2 := newEnv(t, nil)
	e2.addRule(t, store.Rule{Conditions: store.RuleConditions{New: true},
		Actions: []store.RuleAction{{Type: store.ActionSlack, URL: e2.opts.SlackWebhookURL}}})
	e2.setDefaults(t, true, true)
	e2.ingest(t, "api-tarea-a-1")
	e2.send(t)
	if e2.linear.created() != 0 || len(e2.slack.messages()) != 1 {
		t.Fatalf("regla solo de Slack: linear = %d, slack = %d", e2.linear.created(), len(e2.slack.messages()))
	}
}

func TestCreateLinearByHand(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	if err := e.db.SetProjectLabels(ctx, "backend", []string{"label-equipo"}); err != nil {
		t.Fatal(err)
	}
	e.setDefaults(t, true, false)
	res := e.ingest(t, "api-tarea-a-1")
	e.send(t)
	if e.linear.created() != 0 {
		t.Fatal("Linear apagado ha creado la issue")
	}

	by := store.Actor{Name: "ana", Kind: store.ActorUser}
	link, created, err := e.alerter.CreateLinear(ctx, res.IssueID, by)
	if err != nil || !created {
		t.Fatalf("crear: %+v, %v, %v", link, created, err)
	}
	issues, order := e.linear.snapshot()
	if len(order) != 1 {
		t.Fatalf("issues = %d", len(order))
	}
	li := issues[order[0]]
	if !strings.HasPrefix(li.Title, "[backend] task failed") || !strings.Contains(li.Description, "# task failed") ||
		!strings.Contains(li.Description, publicURL+"/issues/") || !slices.Equal(li.Labels, append(slices.Clone(baseLabels), "label-equipo")) {
		t.Fatalf("issue = %+v", li)
	}
	if link.ExternalID != order[0] || link.Identifier != li.Identifier || link.State != store.LinkOpen ||
		link.Author == nil || *link.Author != by {
		t.Fatalf("enlace = %+v", link)
	}

	// Otra vez: la misma, sin crear otra.
	again, created, err := e.alerter.CreateLinear(ctx, res.IssueID, store.Actor{Name: "luis", Kind: store.ActorUser})
	if err != nil || created || again.ID != link.ID || e.linear.created() != 1 {
		t.Fatalf("repetir: %+v, %v, %v; creadas %d", again, created, err, e.linear.created())
	}

	h, err := e.db.History(ctx, res.IssueID, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := slices.IndexFunc(h, func(x store.HistoryEntry) bool { return x.Type == "link" })
	if i < 0 || h[i].Author == nil || *h[i].Author != by || h[i].Link == nil || h[i].Link.Identifier != li.Identifier {
		t.Fatalf("historial = %+v", h)
	}

	// Una regresión ya no crea otra: reabre la de antes, aunque Linear siga apagado para los avisos.
	e.setDefaults(t, true, true)
	if _, err := e.db.SetIssueStatus(ctx, res.IssueID, "resolved"); err != nil {
		t.Fatal(err)
	}
	e.ingest(t, "api-tarea-b-1")
	e.send(t)
	if e.linear.created() != 1 {
		t.Fatalf("la regresión ha creado otra issue")
	}
	msgs := e.slack.payloads(t)
	if last := msgs[len(msgs)-1]; !slices.Contains(buttons(last), li.Identifier+" in Linear") {
		t.Errorf("el aviso de Slack no lleva la issue creada a mano: %v", buttons(last))
	}
}

func TestCreateLinearConcurrentCreatesOne(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	e.setDefaults(t, true, false)
	res := e.ingest(t, "api-tarea-a-1")
	var wg sync.WaitGroup
	errs := make([]error, 4)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, errs[i] = e.alerter.CreateLinear(context.Background(), res.IssueID, store.Actor{Name: "ana", Kind: store.ActorUser})
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	links, err := e.db.IssueLinks(context.Background(), res.IssueID)
	if err != nil || len(links) != 1 || e.linear.created() != 1 {
		t.Fatalf("enlaces = %+v, %v; creadas %d", links, err, e.linear.created())
	}
}

func TestCreateLinearFailsAndRetries(t *testing.T) {
	e := newEnv(t, nil)
	ctx := context.Background()
	e.setDefaults(t, true, false)
	res := e.ingest(t, "api-tarea-a-1")
	by := store.Actor{Name: "ana", Kind: store.ActorUser}

	e.linear.setFail(http.StatusInternalServerError)
	_, _, err := e.alerter.CreateLinear(ctx, res.IssueID, by)
	var le *LinearError
	if !errors.As(err, &le) {
		t.Fatalf("err = %v; quiero un LinearError", err)
	}
	if links, _ := e.db.IssueLinks(ctx, res.IssueID); len(links) != 0 {
		t.Fatalf("un fallo ha enlazado: %+v", links)
	}
	e.linear.setFail(0)
	if _, created, err := e.alerter.CreateLinear(ctx, res.IssueID, by); err != nil || !created || e.linear.created() != 1 {
		t.Fatalf("reintento: %v, %v; creadas %d", created, err, e.linear.created())
	}

	if _, _, err := e.alerter.CreateLinear(ctx, 999999, by); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("un issue que no existe: %v", err)
	}
	off := newEnv(t, func(o *Options) { o.LinearAPIKey = "" })
	if _, _, err := off.alerter.CreateLinear(ctx, res.IssueID, by); !errors.Is(err, ErrLinearOff) {
		t.Errorf("sin Linear: %v", err)
	}
}

// holdFirstCreate deja pasar el primer issueCreate a Linear solo cuando llega
// el segundo: así el aviso y el botón piden la issue a la vez.
func holdFirstCreate(t *testing.T, next http.Handler) http.Handler {
	var mu sync.Mutex
	creates := 0
	second := make(chan struct{})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(body))
		if bytes.Contains(body, []byte("issueCreate")) {
			mu.Lock()
			creates++
			n := creates
			mu.Unlock()
			switch n {
			case 1:
				select {
				case <-second:
				case <-time.After(5 * time.Second):
					t.Error("el segundo issueCreate no ha llegado")
				}
			case 2:
				close(second)
			}
		}
		next.ServeHTTP(w, r)
	})
}

func TestCreateLinearWhileAlertCreatesIt(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	srv := httptest.NewServer(holdFirstCreate(t, e.linear))
	t.Cleanup(srv.Close)
	e.opts.LinearAPIURL = srv.URL
	e.alerter = e.newAlerter(t)
	e.ing = ingest.NewIngester(e.db.Pool(), nil, e.alerter, quiet())
	ctx := context.Background()
	res := e.ingest(t, "api-tarea-a-1")

	done := make(chan error, 1)
	go func() {
		_, err := e.alerter.SendPending(ctx)
		done <- err
	}()
	// El aviso se queda esperando in Linear con su issueCreate; el botón ve el error sin issue.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var sending bool
		if err := e.db.Pool().QueryRow(ctx, `SELECT linear_create_id IS NOT NULL FROM issues WHERE id = $1`, res.IssueID).Scan(&sending); err != nil {
			t.Fatal(err)
		}
		if sending || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	by := store.Actor{Name: "ana", Kind: store.ActorUser}
	link, _, err := e.alerter.CreateLinear(ctx, res.IssueID, by)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if n := e.linear.created(); n != 1 {
		t.Fatalf("issues in Linear = %d; el aviso y el botón tienen que acabar en la misma", n)
	}
	links, err := e.db.IssueLinks(ctx, res.IssueID)
	if err != nil || len(links) != 1 || links[0].ExternalID != link.ExternalID || links[0].Author == nil || *links[0].Author != by {
		t.Fatalf("enlaces = %+v, %v", links, err)
	}
	h, err := e.db.History(ctx, res.IssueID, nil)
	if err != nil || !slices.ContainsFunc(h, func(x store.HistoryEntry) bool { return x.Type == "link" && *x.Author == by }) {
		t.Fatalf("historial sin quién la creó: %+v, %v", h, err)
	}
	for _, o := range e.outbox(t) {
		if !o.Sent {
			t.Errorf("aviso sin enviar: %+v", o)
		}
	}
}

// Una regla a Linear con otro equipo no comparte el id del error: crea su propia issue.
func TestRuleOtherTeamKeepsItsOwnIssue(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.SlackWebhookURL = "" })
	ctx := context.Background()
	res := e.ingest(t, "api-tarea-a-1")
	if _, _, err := e.alerter.CreateLinear(ctx, res.IssueID, store.Actor{Name: "ana", Kind: store.ActorUser}); err != nil {
		t.Fatal(err)
	}
	other := e.alerter.linearFor("team-2", nil)
	if other.shared || !e.alerter.channels[channelLinear].(*linear).shared {
		t.Fatalf("shared: otro equipo %v", other.shared)
	}
}
