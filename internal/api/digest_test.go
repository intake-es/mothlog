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
	"github.com/intake-es/intake-errors/internal/digest/digesttest"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

func setupDigest(t *testing.T) *env {
	t.Helper()
	db := storetest.NewDB(t)
	alerts, err := alert.New(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), alert.Options{PublicURL: "https://errors.example.com", AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := api.Register(mux, db.Store, api.Options{PublicURL: "https://errors.example.com", Alerts: alerts, Digests: alerts}); err != nil {
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

func TestDigestAPI(t *testing.T) {
	e := setupDigest(t)
	p := e.db.AddProject(t, "tienda")
	hook := &hookServer{}
	hookSrv := httptest.NewServer(hook)
	t.Cleanup(hookSrv.Close)
	base := "/api/0/projects/" + itoa(p.ID) + "/digest"

	var c store.DigestConfig
	e.get(t, base, &c)
	if c.Enabled || c.Frequency != "daily" || c.Hour != 9 || c.Language != "es" || len(c.Destinations) != 0 || c.NextSendAt != nil {
		t.Fatalf("por defecto = %+v", c)
	}

	code, b := e.do(t, http.MethodPut, base, `{"enabled": true, "frequency": "weekly", "weekday": 1, "hour": 8,
		"timezone": "Europe/Madrid", "language": "en", "send_empty": false,
		"destinations": [{"type": "webhook", "url": "`+hookSrv.URL+`/resumen"},
			{"id": "slack", "type": "slack", "url": "https://hooks.slack.test/services/T000/B000/secretísimo"}]}`)
	if code != http.StatusOK {
		t.Fatalf("PUT = %d: %s", code, b)
	}
	var saved store.DigestConfig
	if err := json.Unmarshal(b, &saved); err != nil {
		t.Fatal(err)
	}
	secret := saved.Destinations[0].Secret
	if len(secret) != 64 || saved.Destinations[1].URL != "https://hooks.slack.test/…simo" || saved.NextSendAt == nil ||
		saved.NextSendAt.Weekday() != time.Monday {
		t.Fatalf("guardado = %+v", saved)
	}
	e.get(t, base+"/", &c)
	if c.Destinations[0].Secret != "…"+secret[60:] || c.Language != "en" || c.SendEmpty {
		t.Errorf("leído = %+v", c)
	}

	// Lo que devuelve el GET, con los secretos tapados, se puede mandar tal cual.
	raw, _ := json.Marshal(c)
	if code, b := e.do(t, http.MethodPut, base, strings.Replace(string(raw), `"hour":8`, `"hour":7`, 1)); code != http.StatusOK {
		t.Fatalf("PUT de lo leído = %d: %s", code, b)
	}
	full, err := e.db.Digest(context.Background(), p.ID, time.Now())
	if err != nil || full.Destinations[0].Secret != secret || !strings.HasSuffix(full.Destinations[1].URL, "secretísimo") || full.Hour != 7 {
		t.Errorf("los secretos no se conservan: %+v %v", full, err)
	}

	for _, bad := range []string{
		`{"frequency": "monthly"}`,
		`{"enabled": true}`,
		`{"timezone": "Marte/Base"}`,
		`{"destinations": [{"type": "email", "to": ["equipo@example.com"]}]}`,
		`{"destinations": [{"type": "linear"}]}`,
	} {
		if code, b := e.do(t, http.MethodPut, base, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d: %s", bad, code, b)
		}
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/999999/digest", ""); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe = %d", code)
	}

	digesttest.Seed(t, e.db, p.ID, time.Now().Truncate(time.Hour))
	var prev struct {
		Subject, HTML, Text, Markdown string
		Digest                        struct{ Language, Frequency string }
	}
	e.get(t, base+"/preview?language=es&frequency=daily", &prev)
	if !strings.HasPrefix(prev.Subject, "Resumen diario de tienda: 1 nuevo") || !strings.Contains(prev.HTML, "Errores nuevos") ||
		!strings.Contains(prev.Markdown, "https://errors.example.com/issues/") || prev.Digest.Language != "es" {
		t.Errorf("vista previa = %+v", prev)
	}
	e.get(t, base+"/preview", &prev)
	if !strings.HasPrefix(prev.Subject, "Weekly digest for tienda") || prev.Digest.Frequency != "weekly" {
		t.Errorf("vista previa en inglés = %q", prev.Subject)
	}
	if code, _ := e.do(t, http.MethodGet, base+"/preview?language=fr", ""); code != http.StatusBadRequest {
		t.Errorf("idioma que no existe = %d", code)
	}

	code, b = e.do(t, http.MethodPost, base+"/send", "")
	if code != http.StatusOK {
		t.Fatalf("send = %d: %s", code, b)
	}
	var sent struct{ Data []alert.DigestResult }
	if err := json.Unmarshal(b, &sent); err != nil {
		t.Fatal(err)
	}
	// El webhook llega; el Slack de mentira no existe y lo dice.
	if len(sent.Data) != 2 || !sent.Data[0].OK || sent.Data[1].OK || sent.Data[1].Error == "" || strings.Contains(sent.Data[1].Error, "secretísimo") {
		t.Errorf("send = %+v", sent.Data)
	}
	if len(hook.bodies) != 1 || !strings.Contains(string(hook.bodies[0]), `"type":"digest"`) || hook.sigs[0] == "" {
		t.Errorf("webhook = %d", len(hook.bodies))
	}
}

func TestDigestMCP(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "tienda")
	end := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	digesttest.Seed(t, e.db, p.ID, end)
	cs := connect(t, e.srv.URL, e.token)

	out, isErr := call(t, cs, "get_digest", map[string]any{"project": "tienda", "end": "2026-11-22T09:00:00+01:00"})
	if isErr {
		t.Fatal(out)
	}
	for _, w := range []string{"# Resumen diario de tienda", "## Errores nuevos", "## Han vuelto", "## Los que más crecen",
		"## Crons con problemas", "## [Salud de la release web@1.4.1](", "(https://errors.example.com/issues/"} {
		if !strings.Contains(out, w) {
			t.Errorf("get_digest sin %q:\n%s", w, out)
		}
	}
	out, _ = call(t, cs, "get_digest", map[string]any{"project": itoa(p.ID), "period": "week", "language": "en", "end": "2026-11-22T08:00:00Z"})
	if !strings.HasPrefix(out, "# Weekly digest for tienda") {
		t.Errorf("semanal en inglés:\n%s", out)
	}
	for args, want := range map[string]map[string]any{
		"no existe ese proyecto": {"project": "nadie"},
		"period es day o week":   {"project": "tienda", "period": "month"},
		"RFC 3339":               {"project": "tienda", "end": "ayer"},
	} {
		if out, isErr := call(t, cs, "get_digest", want); !isErr || !strings.Contains(out, args) {
			t.Errorf("%v = %s", want, out)
		}
	}
}
