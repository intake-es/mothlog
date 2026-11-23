package api_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/api"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

type hookServer struct {
	mu     sync.Mutex
	bodies [][]byte
	sigs   []string
	fail   bool
}

func (h *hookServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b, _ := io.ReadAll(r.Body)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.fail {
		http.Error(w, "caído", http.StatusServiceUnavailable)
		return
	}
	h.bodies = append(h.bodies, b)
	h.sigs = append(h.sigs, r.Header.Get(alert.HeaderSignature))
}

func setupRules(t *testing.T) *env {
	t.Helper()
	db := storetest.NewDB(t)
	alerts, err := alert.New(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), alert.Options{PublicURL: "https://errors.example.com", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com", Alerts: alerts}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(context.Background(), "tests")
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, srv: srv, token: token}
}

func TestRulesAPI(t *testing.T) {
	e := setupRules(t)
	p := e.db.AddProject(t, "web")
	hook := &hookServer{}
	hookSrv := httptest.NewServer(hook)
	t.Cleanup(hookSrv.Close)
	base := "/api/0/projects/" + itoa(p.ID) + "/rules"

	var list struct{ Data []store.Rule }
	e.get(t, base+"/", &list)
	if list.Data == nil || len(list.Data) != 0 {
		t.Fatalf("sin reglas = %+v", list)
	}

	code, b := e.do(t, http.MethodPost, base, `{"name": "Errores nuevos", "throttle_minutes": 30,
		"conditions": {"new": true, "regressed": true, "environments": ["production"]},
		"actions": [{"type": "webhook", "url": "`+hookSrv.URL+`/hook"},
			{"id": "slack", "type": "slack", "url": "https://hooks.slack.test/services/T000/B000/secretísimo"}]}`)
	if code != http.StatusCreated {
		t.Fatalf("POST = %d: %s", code, b)
	}
	var created store.Rule
	if err := json.Unmarshal(b, &created); err != nil {
		t.Fatal(err)
	}
	hookAct := created.Actions[0]
	if !created.Enabled || created.ID == 0 || len(hookAct.Secret) != 64 || strings.Contains(string(b), "secretísimo") {
		t.Fatalf("creada = %s", b)
	}

	// Al leerla, el secreto del webhook ya va tapado.
	var got store.Rule
	e.get(t, base+"/"+itoa(created.ID)+"/", &got)
	if got.Actions[0].Secret == hookAct.Secret || !strings.HasPrefix(got.Actions[0].Secret, "…") {
		t.Errorf("leída = %+v", got.Actions[0])
	}

	// Editar devolviendo lo leído conserva los secretos.
	got.Name = "Errores nuevos y regresiones"
	body, _ := json.Marshal(got)
	code, b = e.do(t, http.MethodPut, base+"/"+itoa(created.ID), string(body))
	if code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", code, b)
	}
	stored, err := e.db.Rule(context.Background(), p.ID, created.ID)
	if err != nil || stored.Name != "Errores nuevos y regresiones" || stored.Actions[0].Secret != hookAct.Secret ||
		!strings.HasSuffix(stored.Actions[1].URL, "secretísimo") {
		t.Fatalf("guardada = %+v, %v", stored, err)
	}

	// Probar manda un aviso firmado con el secreto de la regla.
	code, b = e.do(t, http.MethodPost, base+"/"+itoa(created.ID)+"/test", `{"action_id": "`+hookAct.ID+`"}`)
	if code != http.StatusOK || !strings.Contains(string(b), `"ok":true`) {
		t.Fatalf("test = %d: %s", code, b)
	}
	hook.mu.Lock()
	if len(hook.bodies) != 1 || !strings.Contains(string(hook.bodies[0]), `"type":"test"`) || !strings.HasPrefix(hook.sigs[0], "t=") {
		t.Errorf("webhook = %q %q", hook.bodies, hook.sigs)
	}
	hook.fail = true
	hook.mu.Unlock()
	code, b = e.do(t, http.MethodPost, base+"/"+itoa(created.ID)+"/test", `{"action_id": "`+hookAct.ID+`"}`)
	if code != http.StatusBadGateway || !strings.Contains(string(b), "503") || strings.Contains(string(b), hookSrv.URL) {
		t.Errorf("test caído = %d: %s", code, b)
	}
	for _, tc := range []struct {
		body string
		code int
	}{
		{`{"action_id": "no-existe"}`, http.StatusNotFound},
		{`{}`, http.StatusBadRequest},
	} {
		if code, b := e.do(t, http.MethodPost, base+"/"+itoa(created.ID)+"/test", tc.body); code != tc.code {
			t.Errorf("test %s = %d: %s", tc.body, code, b)
		}
	}

	for _, tc := range []struct{ body, want string }{
		{`{"name": "x", "conditions": {}, "actions": [{"type": "ntfy", "url": "https://n.test/t"}]}`, "al menos una condición"},
		{`{"name": "x", "conditions": {"new": true}, "actions": [{"type": "linear"}]}`, "LINEAR_API_KEY"},
		{`{"name": "x", "conditions": {"new": true}, "actions": [{"type": "email", "to": ["a@b.test"]}]}`, "SMTP_URL"},
		{`{"name": "x", "otra": 1}`, "otra"},
	} {
		code, b := e.do(t, http.MethodPost, base, tc.body)
		if code != http.StatusBadRequest || !strings.Contains(string(b), tc.want) {
			t.Errorf("%s = %d %s; quiero 400 con %q", tc.body, code, b, tc.want)
		}
	}

	other := e.db.AddProject(t, "otro")
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/"+itoa(other.ID)+"/rules/"+itoa(created.ID), ""); code != http.StatusNotFound {
		t.Errorf("la regla se ve desde otro proyecto: %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/999999/rules", ""); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe: %d", code)
	}

	e.get(t, base, &list)
	if len(list.Data) != 1 || strings.Contains(list.Data[0].Actions[1].URL, "secretísimo") {
		t.Errorf("lista = %+v", list.Data)
	}
	if code, b := e.do(t, http.MethodDelete, base+"/"+itoa(created.ID)+"/", ""); code != http.StatusNoContent {
		t.Fatalf("DELETE = %d: %s", code, b)
	}
	if code, _ := e.do(t, http.MethodDelete, base+"/"+itoa(created.ID), ""); code != http.StatusNotFound {
		t.Errorf("borrar dos veces = %d", code)
	}
}

func TestRulesAPIWithoutAlerts(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "web")
	r, _, err := e.db.CreateRule(context.Background(), p.ID, store.Rule{Name: "r", Conditions: store.RuleConditions{New: true},
		Actions: []store.RuleAction{{ID: "a", Type: store.ActionNtfy, URL: "https://n.test/t"}}})
	if err != nil {
		t.Fatal(err)
	}
	code, _ := e.do(t, http.MethodPost, "/api/0/projects/"+itoa(p.ID)+"/rules/"+itoa(r.ID)+"/test", `{"action_id": "a"}`)
	if code != http.StatusServiceUnavailable {
		t.Errorf("test sin avisos = %d", code)
	}
}

func TestRulesNeedAuth(t *testing.T) {
	e := setupRules(t)
	p := e.db.AddProject(t, "web")
	res, err := http.Get(e.srv.URL + "/api/0/projects/" + itoa(p.ID) + "/rules")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("sin token = %d", res.StatusCode)
	}
}
