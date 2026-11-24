package api_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

func TestReleaseHealthAPIAndMCP(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	web := e.db.AddProject(t, "web")
	for _, v := range []string{"1.0", "2.0"} {
		if _, _, err := e.db.CreateRelease(ctx, web.ID, v); err != nil {
			t.Fatal(err)
		}
	}
	hour := time.Now().UTC().Truncate(time.Hour)
	if _, err := e.db.Pool().Exec(ctx, `
		INSERT INTO release_sessions (project_id, release, environment, hour, total, errored, crashed) VALUES
			($1, '1.0', 'production', $2, 4, 1, 1),
			($1, '1.0', 'staging', $2, 4, 0, 0);
		`, web.ID, hour); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Pool().Exec(ctx, `
		INSERT INTO release_users (project_id, release, environment, user_hash, crashed, last_seen) VALUES
			($1, '1.0', 'production', $2, true, now()), ($1, '1.0', 'production', $3, false, now())`,
		web.ID, []byte{1}, []byte{2}); err != nil {
		t.Fatal(err)
	}

	var page store.ReleasePage
	e.get(t, "/api/0/releases/?project=web", &page)
	if len(page.Releases) != 2 {
		t.Fatalf("releases = %+v", page.Releases)
	}
	h := page.Releases[1].Health
	if page.Releases[1].Version != "1.0" || h.Sessions != 8 || h.SessionsCrashed != 1 || !near(h.CrashFreeSessions, 0.875) ||
		!near(h.CrashFreeUsers, 0.5) || !near(h.Adoption, 1) || len(page.Releases[1].HealthByEnvironment) != 2 {
		t.Errorf("1.0 = %+v", page.Releases[1])
	}

	// Sin sesiones, los porcentajes van a null, no a 1.
	_, body := e.do(t, "GET", "/api/0/releases/?project=web", "")
	var raw struct {
		Data []struct {
			Version string                     `json:"version"`
			Health  map[string]json.RawMessage `json:"health"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		t.Fatal(err)
	}
	if raw.Data[0].Version != "2.0" || string(raw.Data[0].Health["crash_free_sessions"]) != "null" ||
		string(raw.Data[0].Health["crash_free_users"]) != "null" || string(raw.Data[0].Health["sessions"]) != "0" {
		t.Errorf("2.0 = %s", body)
	}

	e.get(t, "/api/0/releases/?project=web&environment=production", &page)
	if len(page.Releases) != 1 || page.Releases[0].Health.Environment != "production" || !near(page.Releases[0].Health.CrashFreeSessions, 0.75) {
		t.Errorf("en producción = %+v", page.Releases)
	}

	var d store.ReleaseDetail
	e.get(t, "/api/0/projects/"+itoa(web.ID)+"/releases/1.0/?environment=staging", &d)
	if d.Health.Sessions != 4 || !near(d.Health.CrashFreeSessions, 1) || d.Health.CrashFreeUsers != nil ||
		len(d.HealthSeries) != store.HealthSeriesHours || d.HealthSeries[len(d.HealthSeries)-1].Sessions != 4 {
		t.Errorf("detalle en staging = %+v", d.Health)
	}

	cs := connect(t, e.srv.URL, e.token)
	out, isErr := call(t, cs, "list_releases", map[string]any{"project": "web"})
	if err := json.Unmarshal([]byte(out), &page); err != nil || isErr || !near(page.Releases[1].Health.CrashFreeSessions, 0.875) {
		t.Errorf("list_releases = %s", out)
	}
	out, isErr = call(t, cs, "get_release", map[string]any{"project": "web", "version": "1.0", "environment": "production"})
	if err := json.Unmarshal([]byte(out), &d); err != nil || isErr || !near(d.Health.CrashFreeUsers, 0.5) || len(d.HealthSeries) == 0 {
		t.Errorf("get_release = %s", out)
	}
	if !strings.Contains(out, `"crash_free_sessions": 0.75`) {
		t.Errorf("get_release sin crash_free_sessions: %s", out[:min(len(out), 400)])
	}
}

func near(got *float64, want float64) bool {
	return got != nil && *got > want-1e-9 && *got < want+1e-9
}
