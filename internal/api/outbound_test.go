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

const bodyMarker = "SERVICIO-INTERNO marca=9c2f"

// internalService es un servicio que solo se ve desde dentro: apunta lo que
// le llega y responde con un error que lleva bodyMarker.
type internalService struct {
	mu   sync.Mutex
	hits []string
	auth []string
}

func (s *internalService) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.hits = append(s.hits, r.Method+" "+r.URL.Path)
	s.auth = append(s.auth, r.Header.Get("Authorization"))
	s.mu.Unlock()
	http.Error(w, bodyMarker, http.StatusForbidden)
}

func (s *internalService) snapshot() ([]string, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.hits...), append([]string(nil), s.auth...)
}

// setupOutbound monta la API como el binario: alert.New sin cliente propio.
func setupOutbound(t *testing.T, allowPrivate bool) (*env, store.Project) {
	t.Helper()
	db := storetest.NewDB(t)
	alerts, err := alert.New(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)),
		alert.Options{PublicURL: "https://errors.example.com", AllowPrivate: allowPrivate})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com",
		Auth: api.RequireAuth(api.BearerToken(db.Store)), Alerts: alerts, Digests: alerts}); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	_, token, err := db.CreateToken(context.Background(), "outbound")
	if err != nil {
		t.Fatal(err)
	}
	return &env{db: db, srv: srv, token: token}, db.AddProject(t, "outbound")
}

func (e *env) createRule(t *testing.T, project int64, action string) store.Rule {
	t.Helper()
	code, b := e.do(t, http.MethodPost, "/api/0/projects/"+itoa(project)+"/rules",
		`{"name":"r","conditions":{"new":true},"actions":[`+action+`]}`)
	if code != http.StatusCreated {
		t.Fatalf("crear regla con %s = %d: %s", action, code, b)
	}
	var r store.Rule
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

func (e *env) testAction(t *testing.T, r store.Rule) (int, string) {
	t.Helper()
	code, b := e.do(t, http.MethodPost, "/api/0/projects/"+itoa(r.ProjectID)+"/rules/"+itoa(r.ID)+"/test",
		`{"action_id":"`+r.Actions[0].ID+`"}`)
	return code, string(b)
}

// metadataRule es una regla guardada antes de que se rechazaran estas URL al
// guardar: se escribe en la base sin pasar por la API.
func metadataRule(t *testing.T, e *env, project int64) store.Rule {
	t.Helper()
	r := e.createRule(t, project, `{"id":"m","type":"discord","url":"https://discord.example.com/api/webhooks/1/x"}`)
	r.Actions = []store.RuleAction{{ID: "m", Type: store.ActionDiscord, URL: "http://169.254.169.254/latest/meta-data/"}}
	if _, err := e.db.Pool().Exec(context.Background(), `UPDATE alert_rules SET actions = $2 WHERE id = $1`, r.ID, r.Actions); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAlertDestinationsBlocked(t *testing.T) {
	e, p := setupOutbound(t, false)
	svc := &internalService{}
	internal := httptest.NewServer(svc)
	t.Cleanup(internal.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/redirigido", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	rules := map[string]store.Rule{
		"webhook a loopback":      e.createRule(t, p.ID, `{"id":"w","type":"webhook","url":"`+internal.URL+`/admin"}`),
		"slack a una redirección": e.createRule(t, p.ID, `{"id":"s","type":"slack","url":"`+redirect.URL+`/hook"}`),
		"ntfy a loopback":         e.createRule(t, p.ID, `{"id":"n","type":"ntfy","url":"`+internal.URL+`/tema","token":"tk_prueba_123456789"}`),
		"discord a metadatos":     metadataRule(t, e, p.ID),
	}
	for name, r := range rules {
		code, b := e.testAction(t, r)
		if code != http.StatusBadGateway || !strings.Contains(b, "dirección bloqueada") || strings.Contains(b, bodyMarker) {
			t.Errorf("%s: %d %s", name, code, b)
		}
		if name != "discord a metadatos" && !strings.Contains(b, alert.AllowPrivateEnv+"=1") {
			t.Errorf("%s: el error no nombra %s: %s", name, alert.AllowPrivateEnv, b)
		}
		if strings.Contains(b, "127.0.0.1") {
			t.Errorf("%s: el error dice la dirección: %s", name, b)
		}
	}

	dbase := "/api/0/projects/" + itoa(p.ID) + "/digest"
	if code, b := e.do(t, http.MethodPut, dbase, `{"enabled":true,"frequency":"daily","hour":9,"timezone":"UTC","language":"es",
		"destinations":[{"id":"d","type":"webhook","url":"`+internal.URL+`/resumen"}]}`); code != http.StatusOK {
		t.Fatalf("PUT digest = %d: %s", code, b)
	}
	code, b := e.do(t, http.MethodPost, dbase+"/send", "")
	if code != http.StatusOK || !strings.Contains(string(b), "dirección bloqueada") || strings.Contains(string(b), `"ok":true`) {
		t.Errorf("mandar el resumen: %d %s", code, b)
	}

	if hits, _ := svc.snapshot(); len(hits) != 0 {
		t.Fatalf("el servicio interno ha recibido %q", hits)
	}
}

func TestAlertDestinationsRejectedOnSave(t *testing.T) {
	e, p := setupOutbound(t, false)
	for _, u := range []string{"http://169.254.169.254/latest/meta-data/", "http://metadata.google.internal/computeMetadata/v1/",
		"http://[::ffff:169.254.169.254]/", "http://[fe80::1]/", "http://100.100.100.200/"} {
		for _, typ := range []string{"slack", "discord", "ntfy", "webhook"} {
			code, b := e.do(t, http.MethodPost, "/api/0/projects/"+itoa(p.ID)+"/rules",
				`{"name":"r","conditions":{"new":true},"actions":[{"type":"`+typ+`","url":"`+u+`"}]}`)
			if code != http.StatusBadRequest {
				t.Errorf("regla %s a %s = %d: %s", typ, u, code, b)
			}
		}
		code, b := e.do(t, http.MethodPut, "/api/0/projects/"+itoa(p.ID)+"/digest", `{"enabled":true,"frequency":"daily","hour":9,
			"timezone":"UTC","language":"es","destinations":[{"type":"webhook","url":"`+u+`"}]}`)
		if code != http.StatusBadRequest {
			t.Errorf("resumen a %s = %d: %s", u, code, b)
		}
	}
}

func TestAlertDestinationsAllowPrivate(t *testing.T) {
	e, p := setupOutbound(t, true)
	svc := &internalService{}
	internal := httptest.NewServer(svc)
	t.Cleanup(internal.Close)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, internal.URL+"/redirigido", http.StatusFound)
	}))
	t.Cleanup(redirect.Close)

	// Loopback va; del cuerpo de la respuesta no vuelve nada.
	code, b := e.testAction(t, e.createRule(t, p.ID, `{"id":"w","type":"webhook","url":"`+internal.URL+`/admin"}`))
	if code != http.StatusBadGateway || !strings.Contains(b, "403 Forbidden") || strings.Contains(b, bodyMarker) {
		t.Errorf("webhook a loopback: %d %s", code, b)
	}
	// Una redirección no se sigue.
	code, b = e.testAction(t, e.createRule(t, p.ID, `{"id":"s","type":"slack","url":"`+redirect.URL+`/hook"}`))
	if code != http.StatusBadGateway || !strings.Contains(b, "302 Found") {
		t.Errorf("slack a una redirección: %d %s", code, b)
	}
	// Metadatos, nunca.
	code, b = e.testAction(t, metadataRule(t, e, p.ID))
	if code != http.StatusBadGateway || !strings.Contains(b, "dirección bloqueada") || strings.Contains(b, alert.AllowPrivateEnv) {
		t.Errorf("discord a metadatos: %d %s", code, b)
	}
	if hits, _ := svc.snapshot(); len(hits) != 1 || hits[0] != "POST /admin" {
		t.Fatalf("el servicio interno ha recibido %q; quiero solo POST /admin", hits)
	}
}

// Al cambiar la URL de ntfy, el token guardado no va al servidor nuevo.
func TestNtfyTokenDoesNotFollowNewURL(t *testing.T) {
	e, p := setupOutbound(t, true)
	const token = "tk_prueba_ntfy_ABCDEF987654"
	first, second := &internalService{}, &internalService{}
	firstSrv, secondSrv := httptest.NewServer(first), httptest.NewServer(second)
	t.Cleanup(firstSrv.Close)
	t.Cleanup(secondSrv.Close)
	r := e.createRule(t, p.ID, `{"id":"n","type":"ntfy","url":"`+firstSrv.URL+`/equipo","token":"`+token+`"}`)
	base := "/api/0/projects/" + itoa(p.ID) + "/rules/" + itoa(r.ID)
	put := func(url, tok string) (int, string) {
		code, b := e.do(t, http.MethodPut, base, `{"name":"r","conditions":{"new":true},"actions":[{"id":"n","type":"ntfy","url":"`+
			url+`","token":"`+tok+`"}]}`)
		return code, string(b)
	}

	// Con la misma URL, tapada como la devuelve la API, el token se conserva.
	var saved store.Rule
	e.get(t, base, &saved)
	if code, b := put(saved.Actions[0].URL, saved.Actions[0].Token); code != http.StatusOK {
		t.Fatalf("PUT con la URL de antes = %d: %s", code, b)
	}
	e.testAction(t, r)
	if _, auth := first.snapshot(); len(auth) != 1 || auth[0] != "Bearer "+token {
		t.Fatalf("con la misma URL, Authorization = %q", auth)
	}

	// Con otra URL y el token tapado: 400.
	if code, b := put(secondSrv.URL+"/fuera", saved.Actions[0].Token); code != http.StatusBadRequest {
		t.Fatalf("PUT con otra URL y el token tapado = %d: %s", code, b)
	}
	// Con otra URL y el token vacío: se queda vacío.
	if code, b := put(secondSrv.URL+"/fuera", ""); code != http.StatusOK {
		t.Fatalf("PUT con otra URL y sin token = %d: %s", code, b)
	}
	e.testAction(t, r)
	hits, auth := second.snapshot()
	if len(hits) != 1 || auth[0] != "" {
		t.Fatalf("el servidor nuevo ha recibido %q con Authorization %q", hits, auth)
	}
	stored, err := e.db.Rule(context.Background(), p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Actions[0].Token != "" {
		t.Fatalf("el token guardado sigue ahí tras cambiar la URL")
	}
}

// Igual con la clave de firma del webhook: con otra URL, se estrena una.
func TestWebhookSecretDoesNotFollowNewURL(t *testing.T) {
	e, p := setupOutbound(t, true)
	r := e.createRule(t, p.ID, `{"id":"w","type":"webhook","url":"https://a.example.com/hook"}`)
	old := r.Actions[0].Secret
	base := "/api/0/projects/" + itoa(p.ID) + "/rules/" + itoa(r.ID)
	var saved store.Rule
	e.get(t, base, &saved)
	code, b := e.do(t, http.MethodPut, base, `{"name":"r","conditions":{"new":true},"actions":[{"id":"w","type":"webhook",
		"url":"https://b.example.com/hook","secret":"`+saved.Actions[0].Secret+`"}]}`)
	if code != http.StatusBadRequest {
		t.Fatalf("otra URL con la clave tapada = %d: %s", code, b)
	}
	code, b = e.do(t, http.MethodPut, base, `{"name":"r","conditions":{"new":true},"actions":[{"id":"w","type":"webhook",
		"url":"https://b.example.com/hook","secret":""}]}`)
	if code != http.StatusOK {
		t.Fatalf("otra URL sin clave = %d: %s", code, b)
	}
	var got store.Rule
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Actions[0].Secret == "" || got.Actions[0].Secret == old {
		t.Fatalf("la clave de firma no ha cambiado con la URL")
	}
}
