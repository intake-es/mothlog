package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

type uptimeJSON struct {
	Type string `json:"type"`
	store.UptimeDetail
}

func TestUptimeAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	web := e.db.AddProject(t, "web")
	path := "/api/0/projects/" + itoa(web.ID) + "/uptime-monitors/"

	code, b := e.do(t, http.MethodPost, path, `{"name": "Web principal", "url": "https://example.com/", "method": "HEAD",
		"interval_seconds": 30, "expected_status": "2xx,301"}`)
	if code != http.StatusCreated {
		t.Fatalf("crear = %d: %s", code, b)
	}
	var m store.UptimeMonitor
	decode(t, b, &m)
	if m.Slug != "web-principal" || m.Method != "HEAD" || m.IntervalSeconds != 30 || m.ExpectedStatus != "200-299,301" ||
		m.FailureThreshold != 2 || m.Status != "pending" {
		t.Errorf("creado = %+v", m)
	}
	for _, bad := range []string{
		`{"name": "meta", "url": "http://169.254.169.254/latest/meta-data/"}`,
		`{"name": "meta", "url": "http://[fe80::a9fe:a9fe]/"}`,
		`{"name": "x", "url": "https://example.com/", "method": "POST"}`,
		`{"name": "x", "url": "https://example.com/", "interval_seconds": 5}`,
		`{"name": "x", "url": "https://example.com/", "otro": 1}`,
		`{"name": "Web principal", "url": "https://example.com/"}`,
	} {
		if code, b := e.do(t, http.MethodPost, path, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d: %s", bad, code, b)
		}
	}
	code, b = e.do(t, http.MethodPost, path, `{"name": "meta", "url": "http://169.254.169.254/latest/meta-data/"}`)
	if !strings.Contains(string(b), "169.254.169.254") || !strings.Contains(string(b), "metadata") {
		t.Errorf("metadatos = %d: %s", code, b)
	}

	code, b = e.do(t, http.MethodPost, path, `{"name": "API", "url": "https://api.example.com/salud", "failure_threshold": 1}`)
	if code != http.StatusCreated {
		t.Fatalf("crear api = %d: %s", code, b)
	}
	decode(t, b, &m)
	sc := 503
	if _, err := e.db.RecordUptimeCheck(ctx, m.ID, store.UptimeCheck{CheckedAt: time.Now().Add(-time.Minute), StatusCode: &sc,
		DurationMs: 210, Error: "código 503"}, nil); err != nil {
		t.Fatal(err)
	}

	var list struct{ Data []store.UptimeMonitor }
	e.get(t, "/api/0/uptime-monitors/", &list)
	if len(list.Data) != 2 || list.Data[0].Slug != "api" || list.Data[0].Status != "down" || list.Data[0].DownSince == nil ||
		len(list.Data[0].Recent) != 1 || list.Data[0].Uptime24h == nil || *list.Data[0].Uptime24h >= 100 ||
		list.Data[1].Slug != "web-principal" || list.Data[1].Uptime24h != nil {
		t.Errorf("lista = %+v", list.Data)
	}
	e.get(t, "/api/0/uptime-monitors/?project=web&status=down", &list)
	if len(list.Data) != 1 {
		t.Errorf("filtrada = %+v", list.Data)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/uptime-monitors/?status=raro", ""); code != http.StatusBadRequest {
		t.Errorf("estado raro = %d", code)
	}

	var d store.UptimeDetail
	e.get(t, path+"api/", &d)
	if d.Slug != "api" || len(d.Incidents) != 1 || d.Incidents[0].EndedAt != nil || len(d.Checks) != 1 || len(d.ResponseTimes) != 1 ||
		d.ResponseTimes[0].Failures != 1 {
		t.Errorf("detalle = %+v", d)
	}

	code, b = e.do(t, http.MethodPatch, path+"api/", `{"paused": true, "timeout_seconds": 20}`)
	if code != http.StatusOK {
		t.Fatalf("pausar = %d: %s", code, b)
	}
	decode(t, b, &m)
	if !m.Paused || m.TimeoutSeconds != 20 {
		t.Errorf("pausado = %+v", m)
	}
	if code, _ := e.do(t, http.MethodPatch, path+"api/", `{"url": "http://169.254.169.254/"}`); code != http.StatusBadRequest {
		t.Errorf("editar a metadatos = %d", code)
	}
	if code, _ := e.do(t, http.MethodPatch, path+"nadie/", `{"paused": true}`); code != http.StatusNotFound {
		t.Errorf("editar uno que no existe = %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, path+"api/", ""); code != http.StatusNoContent {
		t.Errorf("borrar = %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, path+"api/", ""); code != http.StatusNotFound {
		t.Errorf("borrado = %d", code)
	}
}

func TestMCPUptimeMonitors(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	web := e.db.AddProject(t, "web")
	slug, u := "home", "https://example.com/"
	m, err := e.db.CreateUptimeMonitor(ctx, web.ID, store.UptimeInput{Slug: &slug, URL: &u})
	if err != nil {
		t.Fatal(err)
	}
	sc := 200
	if _, err := e.db.RecordUptimeCheck(ctx, m.ID, store.UptimeCheck{CheckedAt: time.Now(), OK: true, StatusCode: &sc, DurationMs: 90}, nil); err != nil {
		t.Fatal(err)
	}
	cs := connect(t, e.srv.URL, e.token)
	out, isErr := call(t, cs, "list_monitors", map[string]any{"project": "web"})
	var list struct {
		Data   []store.Monitor
		Uptime []store.UptimeMonitor
	}
	if err := json.Unmarshal([]byte(out), &list); err != nil || isErr || len(list.Data) != 0 || len(list.Uptime) != 1 ||
		list.Uptime[0].Status != "up" || list.Uptime[0].Uptime30d == nil || *list.Uptime[0].Uptime30d != 100 {
		t.Fatalf("list_monitors = %s", out)
	}
	if out, isErr := call(t, cs, "list_monitors", map[string]any{"status": "down"}); isErr || !strings.Contains(out, `"uptime": []`) {
		t.Errorf("list_monitors down = %s", out)
	}
	out, isErr = call(t, cs, "get_monitor", map[string]any{"project": "web", "slug": "home"})
	var d uptimeJSON
	if err := json.Unmarshal([]byte(out), &d); err != nil || isErr || d.Type != "uptime" || d.URL != u || len(d.Checks) != 1 {
		t.Fatalf("get_monitor = %s", out)
	}
	if out, isErr := call(t, cs, "get_monitor", map[string]any{"project": "web", "slug": "home", "type": "cron"}); !isErr ||
		!strings.Contains(out, "no such monitor") {
		t.Errorf("get_monitor cron = %s", out)
	}
}
