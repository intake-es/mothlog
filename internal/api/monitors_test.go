package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/cron"
	"github.com/intake-es/mothlog/internal/store"
)

type monitorJSON struct {
	store.Monitor
	CheckIns   []store.CheckIn `json:"checkins"`
	NextCursor string          `json:"next_cursor"`
}

func TestMonitorsAPI(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	back := e.db.AddProject(t, "backend")
	path := "/api/0/projects/" + itoa(back.ID) + "/monitors/"

	code, b := e.do(t, http.MethodPost, path, `{"name": "Copia de seguridad", "schedule": {"type": "crontab", "value": "0 4 * * *"},
		"timezone": "Europe/Madrid", "checkin_margin": 10}`)
	if code != http.StatusCreated {
		t.Fatalf("crear = %d: %s", code, b)
	}
	var m store.Monitor
	decode(t, b, &m)
	if m.Slug != "copia-de-seguridad" || m.Schedule.Crontab != "0 4 * * *" || m.CheckinMargin != 10 || m.MaxRuntime != 30 || m.Status != "pending" {
		t.Errorf("creado = %+v", m)
	}
	for _, bad := range []string{
		`{"name": "x", "schedule": {"type": "crontab", "value": "0 4 * *"}}`,
		`{"name": "x", "schedule": {"type": "interval", "value": 0, "unit": "hour"}}`,
		`{"name": "x", "schedule": {"type": "crontab", "value": "* * * * *"}, "timezone": "Luna/Tranquilidad"}`,
		`{"name": "x", "schedule": {"type": "crontab", "value": "* * * * *"}, "otro": 1}`,
		`{"name": "Copia de seguridad", "schedule": {"type": "crontab", "value": "* * * * *"}}`,
	} {
		if code, b := e.do(t, http.MethodPost, path, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d: %s", bad, code, b)
		}
	}

	// Un check-in de un SDK crea otro, que falla.
	if _, err := e.db.ProcessCheckIn(ctx, back.ID, store.CheckInInput{Slug: "facturas", Status: "error", Environment: "production",
		Config: &store.MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeInterval, Every: 1, Unit: "hour"}}}, time.Now(), nil); err != nil {
		t.Fatal(err)
	}
	var list struct{ Data []store.Monitor }
	e.get(t, "/api/0/monitors/", &list)
	if len(list.Data) != 2 || list.Data[0].Slug != "facturas" || list.Data[0].Status != "error" || len(list.Data[0].Recent) != 1 ||
		list.Data[0].NextCheckinAt == nil || list.Data[1].Slug != "copia-de-seguridad" || len(list.Data[1].Recent) != 0 {
		t.Errorf("lista = %+v", list.Data)
	}
	e.get(t, "/api/0/monitors/?project=backend&status=error", &list)
	if len(list.Data) != 1 {
		t.Errorf("filtrada = %+v", list.Data)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/monitors/?project=nadie", ""); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe = %d", code)
	}

	var d monitorJSON
	e.get(t, path+"facturas/", &d)
	if d.Slug != "facturas" || len(d.CheckIns) != 1 || d.CheckIns[0].Status != "error" {
		t.Errorf("detalle = %+v", d)
	}
	var page store.CheckInPage
	e.get(t, path+"facturas/checkins/?limit=5", &page)
	if len(page.CheckIns) != 1 {
		t.Errorf("historial = %+v", page)
	}

	code, b = e.do(t, http.MethodPatch, path+"facturas/", `{"muted": true, "max_runtime": 90}`)
	if code != http.StatusOK {
		t.Fatalf("silenciar = %d: %s", code, b)
	}
	decode(t, b, &m)
	if !m.Muted || m.MaxRuntime != 90 || m.Schedule.Every != 1 {
		t.Errorf("silenciado = %+v", m)
	}
	if code, _ := e.do(t, http.MethodPatch, path+"nadie/", `{"muted": true}`); code != http.StatusNotFound {
		t.Errorf("editar uno que no existe = %d", code)
	}
	if code, _ := e.do(t, http.MethodDelete, path+"facturas/", ""); code != http.StatusNoContent {
		t.Errorf("borrar = %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, path+"facturas/", ""); code != http.StatusNotFound {
		t.Errorf("borrado = %d", code)
	}
}

func TestMCPMonitors(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	back := e.db.AddProject(t, "backend")
	tz := "Europe/Madrid"
	for _, st := range []string{"ok", "error"} {
		if _, err := e.db.ProcessCheckIn(ctx, back.ID, store.CheckInInput{Slug: "copia", Status: st, Environment: "production",
			Config: &store.MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "0 3 * * *"}, Timezone: tz}},
			time.Now(), nil); err != nil {
			t.Fatal(err)
		}
	}
	cs := connect(t, e.srv.URL, e.token)
	out, isErr := call(t, cs, "list_monitors", map[string]any{"project": "backend"})
	var list struct{ Data []store.Monitor }
	if err := json.Unmarshal([]byte(out), &list); err != nil || isErr || len(list.Data) != 1 || list.Data[0].Status != "error" ||
		len(list.Data[0].Recent) != 2 {
		t.Fatalf("list_monitors = %s", out)
	}
	out, isErr = call(t, cs, "get_monitor", map[string]any{"project": "backend", "slug": "copia", "limit": 1})
	var m monitorJSON
	if err := json.Unmarshal([]byte(out), &m); err != nil || isErr || m.Timezone != tz || len(m.CheckIns) != 1 ||
		m.CheckIns[0].Status != "error" || m.NextCursor == "" {
		t.Fatalf("get_monitor = %s", out)
	}
	out, isErr = call(t, cs, "get_monitor", map[string]any{"project": "backend", "slug": "nadie"})
	if !isErr || !strings.Contains(out, "no such monitor") {
		t.Errorf("get_monitor de uno que no existe = %s", out)
	}
}
