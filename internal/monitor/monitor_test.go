package monitor

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/store"
)

func fixtureCheckIn(t *testing.T, name string) store.CheckInInput {
	t.Helper()
	base := filepath.Join("..", "..", "testdata", "envelopes", name)
	var meta struct {
		Headers map[string]string `json:"headers"`
	}
	b, err := os.ReadFile(base + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(base + ".envelope")
	if err != nil {
		t.Fatal(err)
	}
	data, err := envelope.Decompress(bytes.NewReader(body), meta.Headers["Content-Encoding"], 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	items := env.CheckIns()
	if len(items) != 1 {
		t.Fatalf("%s: %d check-ins", name, len(items))
	}
	in, err := Parse(items[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return in
}

func TestParseFixtures(t *testing.T) {
	cases := []struct {
		name, slug, status, env, release, schedule, tz string
		margin, runtime                                int
		duration                                       bool
	}{
		{"api-cron-ok-1", "limpieza-nocturna", "in_progress", "production", "fixture-release-1", `{"type":"crontab","value":"0 3 * * *"}`, "Europe/Madrid", 5, 20, false},
		{"api-cron-ok-2", "limpieza-nocturna", "ok", "production", "fixture-release-1", `{"type":"crontab","value":"0 3 * * *"}`, "Europe/Madrid", 5, 20, true},
		{"api-cron-error-1", "enviar-facturas", "in_progress", "production", "fixture-release-1", `{"type":"interval","value":2,"unit":"hour"}`, "", 0, 0, false},
		{"api-cron-error-2", "enviar-facturas", "error", "production", "fixture-release-1", `{"type":"interval","value":2,"unit":"hour"}`, "", 0, 0, true},
		{"cli-monitors-run-1", "copia-seguridad", "in_progress", "production", "", `{"type":"crontab","value":"*/5 * * * *"}`, "Europe/Madrid", 2, 3, false},
		{"cli-monitors-run-2", "copia-seguridad", "ok", "production", "", "", "", 0, 0, true},
		{"cli-monitors-run-error-1", "copia-seguridad", "in_progress", "staging", "", "", "", 0, 0, false},
		{"cli-monitors-run-error-2", "copia-seguridad", "error", "staging", "", "", "", 0, 0, true},
	}
	ids := map[string]string{}
	for _, c := range cases {
		in := fixtureCheckIn(t, c.name)
		if in.Slug != c.slug || in.Status != c.status || in.Environment != c.env || in.Release != c.release ||
			(in.Duration != nil) != c.duration || len(in.CheckInID) != 32 {
			t.Errorf("%s: %+v", c.name, in)
		}
		// El in_progress y el que lo cierra llevan el mismo id.
		if prev, ok := ids[c.slug+c.env]; ok && prev != in.CheckInID {
			t.Errorf("%s: id %s distinto del de su in_progress %s", c.name, in.CheckInID, prev)
		}
		ids[c.slug+c.env] = in.CheckInID
		if c.schedule == "" {
			if in.Config != nil {
				t.Errorf("%s: monitor_config = %+v", c.name, in.Config)
			}
			continue
		}
		if in.Config == nil {
			t.Fatalf("%s: sin monitor_config", c.name)
		}
		sched, _ := json.Marshal(in.Config.Schedule)
		if string(sched) != c.schedule || in.Config.Timezone != c.tz {
			t.Errorf("%s: horario %s %q", c.name, sched, in.Config.Timezone)
		}
		if got := intOr0(in.Config.CheckinMargin); got != c.margin {
			t.Errorf("%s: margen %d", c.name, got)
		}
		if got := intOr0(in.Config.MaxRuntime); got != c.runtime {
			t.Errorf("%s: máximo %d", c.name, got)
		}
	}
}

func intOr0(p *int) int {
	if p == nil {
		return 0
	}
	return *p
}

func TestParse(t *testing.T) {
	if _, err := Parse([]byte(`{"status":"ok"}`)); !errors.Is(err, ErrMalformed) {
		t.Errorf("sin slug: %v", err)
	}
	if _, err := Parse([]byte(`{`)); !errors.Is(err, ErrMalformed) {
		t.Errorf("JSON roto: %v", err)
	}
	in, err := Parse([]byte(`{"monitor_slug":"x","status":"OK","check_in_id":"0F8E1A2B-3C4D-4E5F-8A9B-0C1D2E3F4A5B","duration":1.5,
		"monitor_config":{"schedule":{"type":"interval","value":"10","unit":"minutes"},"checkin_margin":0.5}}`))
	if err != nil {
		t.Fatal(err)
	}
	if in.Status != "ok" || in.CheckInID != "0f8e1a2b3c4d4e5f8a9b0c1d2e3f4a5b" || in.Environment != "production" ||
		*in.Duration != 1.5 || in.Config.Schedule.Every != 10 || *in.Config.CheckinMargin != 1 {
		t.Errorf("check-in = %+v %+v", in, in.Config)
	}
	if in, _ := Parse([]byte(`{"monitor_slug":"x","status":"ok","check_in_id":"no-es-un-id"}`)); in.CheckInID != "" {
		t.Errorf("id raro = %q", in.CheckInID)
	}
}
