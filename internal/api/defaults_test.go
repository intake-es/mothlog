package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
	"github.com/intake-es/intake-errors/internal/webhooks/fakes"
)

const fakeLinearKey = "lin_api_prueba"

// setupChannels es la API con un Alerter con Slack y, si linear, el Linear falso.
func setupChannels(t *testing.T, linear bool) (*env, *fakes.Linear, *fakes.Log) {
	t.Helper()
	db := storetest.NewDB(t)
	log := &fakes.Log{}
	lin := fakes.NewLinear(log, fakeLinearKey, "secreto")
	linSrv := httptest.NewServer(lin.Handler())
	t.Cleanup(linSrv.Close)
	o := alert.Options{PublicURL: "https://errors.example.com", SlackWebhookURL: "https://hooks.slack.test/services/T/B/x"}
	if linear {
		o.LinearAPIKey, o.LinearAPIURL, o.LinearTeamID = fakeLinearKey, linSrv.URL, fakes.LinearTeamID
	}
	alerts, err := alert.New(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), o)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com", Alerts: alerts, Channels: alerts}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(context.Background(), "tests")
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, srv: srv, token: token}, lin, log
}

type defaultsBody struct {
	Slack      bool     `json:"slack"`
	Linear     bool     `json:"linear"`
	Configured []string `json:"configured"`
	HasRules   bool     `json:"has_rules"`
}

func TestDefaultAlertsAPI(t *testing.T) {
	e, _, _ := setupChannels(t, true)
	p := e.db.AddProject(t, "web")
	base := "/api/0/projects/" + itoa(p.ID) + "/default-alerts"

	var d defaultsBody
	e.get(t, base, &d)
	if !d.Slack || !d.Linear || strings.Join(d.Configured, ",") != "slack,linear" || d.HasRules {
		t.Fatalf("por defecto = %+v", d)
	}

	code, b := e.do(t, http.MethodPatch, base, `{"linear": false}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", code, b)
	}
	if err := json.Unmarshal(b, &d); err != nil || !d.Slack || d.Linear {
		t.Fatalf("tras apagar Linear = %s", b)
	}
	if got, _ := e.db.DefaultAlerts(context.Background(), p.ID); got != (store.DefaultAlerts{Slack: true, Linear: false}) {
		t.Fatalf("guardado = %+v", got)
	}

	for _, body := range []string{`{"linear": null}`, `{"linear": "no"}`, `{"email": false}`, `[]`, `null`, `{`} {
		if code, b := e.do(t, http.MethodPatch, base, body); code != http.StatusBadRequest {
			t.Errorf("PATCH %s = %d: %s", body, code, b)
		}
	}
	if got, _ := e.db.DefaultAlerts(context.Background(), p.ID); got != (store.DefaultAlerts{Slack: true, Linear: false}) {
		t.Fatalf("un PATCH no válido ha cambiado algo: %+v", got)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/999999/default-alerts", ""); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe = %d", code)
	}

	if _, _, err := e.db.CreateRule(context.Background(), p.ID, store.Rule{Name: "r", Enabled: false,
		Conditions: store.RuleConditions{New: true}, Actions: []store.RuleAction{{Type: store.ActionLinear}}}); err != nil {
		t.Fatal(err)
	}
	e.get(t, base, &d)
	if !d.HasRules {
		t.Errorf("con una regla de errores (aunque esté apagada) = %+v", d)
	}
}

func TestCreateLinearAPI(t *testing.T) {
	e, lin, _ := setupChannels(t, true)
	p := e.db.AddProject(t, "web")
	at := time.Now().UTC()
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError: x", Type: "error", FirstSeen: at, LastSeen: at, Count: 1})
	e.db.AddEvent(t, p.ID, id, storetest.Fixture(t, "web-typeerror-1"), at)
	path := "/api/0/issues/" + itoa(id) + "/linear"

	code, b := e.do(t, http.MethodPost, path, "")
	if code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", code, b)
	}
	var link store.IssueLink
	if err := json.Unmarshal(b, &link); err != nil || link.Provider != "linear" || link.Identifier == "" ||
		link.Author == nil || link.Author.Name != "tests" || link.Author.Kind != store.ActorToken {
		t.Fatalf("enlace = %s", b)
	}
	code, b = e.do(t, http.MethodPost, path, "")
	var again store.IssueLink
	if code != http.StatusOK || json.Unmarshal(b, &again) != nil || again.ID != link.ID || len(lin.Issues()) != 1 {
		t.Fatalf("otra vez = %d: %s; issues %d", code, b, len(lin.Issues()))
	}
	li := lin.Issues()[0]
	if !strings.HasPrefix(li.Title, "[web] ") || !strings.Contains(li.Description, "https://errors.example.com/issues/"+itoa(id)) {
		t.Errorf("issue = %+v", li)
	}

	var d store.IssueDetail
	e.get(t, "/api/0/issues/"+itoa(id)+"/", &d)
	if len(d.Links) != 1 || d.Links[0].ID != link.ID {
		t.Errorf("links del detalle = %+v", d.Links)
	}
	var h struct{ Data []store.HistoryEntry }
	e.get(t, "/api/0/issues/"+itoa(id)+"/history/", &h)
	if len(h.Data) != 1 || h.Data[0].Type != "link" || h.Data[0].Author.Name != "tests" || h.Data[0].Link.Identifier != li.Identifier {
		t.Errorf("historial = %+v", h.Data)
	}
	if code, _ := e.do(t, http.MethodPost, "/api/0/issues/999999/linear", ""); code != http.StatusNotFound {
		t.Errorf("issue que no existe = %d", code)
	}
}

func TestCreateLinearAPIErrors(t *testing.T) {
	e, _, _ := setupChannels(t, false)
	p := e.db.AddProject(t, "web")
	at := time.Now().UTC()
	id := e.db.AddIssue(t, storetest.Issue{ProjectID: p.ID, Title: "TypeError: x", Type: "error", FirstSeen: at, LastSeen: at, Count: 1})
	if code, b := e.do(t, http.MethodPost, "/api/0/issues/"+itoa(id)+"/linear", ""); code != http.StatusConflict {
		t.Errorf("sin Linear = %d: %s", code, b)
	}

	// Con Linear caído, un 502 sin el detalle de Linear.
	e2, _, _ := setupChannels(t, true)
	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "detalle interno de Linear", http.StatusInternalServerError)
	}))
	t.Cleanup(down.Close)
	alerts, err := alert.New(e2.db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), alert.Options{
		LinearAPIKey: fakeLinearKey, LinearAPIURL: down.URL, LinearTeamID: fakes.LinearTeamID})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux, e2.db.Store, api.Options{Channels: alerts}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e2.srv = srv
	p2 := e2.db.AddProject(t, "web")
	id2 := e2.db.AddIssue(t, storetest.Issue{ProjectID: p2.ID, Title: "TypeError: x", Type: "error", FirstSeen: at, LastSeen: at, Count: 1})
	code, b := e2.do(t, http.MethodPost, "/api/0/issues/"+itoa(id2)+"/linear", "")
	if code != http.StatusBadGateway || strings.Contains(string(b), "interno") {
		t.Errorf("Linear caído = %d: %s", code, b)
	}
	if links, _ := e2.db.IssueLinks(context.Background(), id2); len(links) != 0 {
		t.Errorf("enlaces tras fallar = %+v", links)
	}
}
