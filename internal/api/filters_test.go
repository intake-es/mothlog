package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

func TestFiltersAPI(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "web")
	path := "/api/0/projects/" + itoa(p.ID) + "/filters"

	var f store.Filters
	e.get(t, path, &f)
	if f.SampleRate != 1 || f.Messages == nil || len(f.Messages) != 0 || f.EventsPerIssuePerHour != nil || f.RateLimitPerMinute != nil {
		t.Fatalf("por defecto = %+v", f)
	}
	code, b := e.do(t, http.MethodGet, path, "")
	if code != http.StatusOK || !strings.Contains(string(b), `"events_per_issue_per_hour":null`) || !strings.Contains(string(b), `"messages":[]`) {
		t.Errorf("JSON por defecto = %s", b)
	}

	code, b = e.do(t, http.MethodPatch, path+"/", `{"messages": ["^Script error"], "browser_extensions": true,
		"events_per_issue_per_hour": 20, "sample_rate": 0.5, "rate_limit_per_minute": 300, "environments": ["development"]}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", code, b)
	}
	// Lo que no trae el cuerpo se queda como estaba; null vuelve al valor por defecto.
	code, b = e.do(t, http.MethodPatch, path, `{"web_crawlers": true, "rate_limit_per_minute": null}`)
	if code != http.StatusOK {
		t.Fatalf("PATCH = %d: %s", code, b)
	}
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	stored, err := e.db.Filters(context.Background(), p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []store.Filters{f, stored} {
		if !slices.Equal(got.Messages, []string{"^Script error"}) || !got.BrowserExtensions || !got.WebCrawlers ||
			got.PerIssuePerHour() != 20 || got.SampleRate != 0.5 || got.RateLimitPerMinute != nil ||
			!slices.Equal(got.Environments, []string{"development"}) {
			t.Errorf("filtros = %+v", got)
		}
	}

	for _, tc := range []struct{ body, want string }{
		{`{"messages": ["(roto"]}`, "regular expression"},
		{`{"sample_rate": 2}`, "sample_rate"},
		{`{"events_per_issue_per_hour": 0}`, "events_per_issue_per_hour"},
		{`{"otra_cosa": 1}`, "otra_cosa"},
		{`no es json`, "invalid body"},
	} {
		code, b := e.do(t, http.MethodPatch, path, tc.body)
		if code != http.StatusBadRequest || !strings.Contains(string(b), tc.want) {
			t.Errorf("%s = %d: %s", tc.body, code, b)
		}
	}
	if again, _ := e.db.Filters(context.Background(), p.ID); again.SampleRate != 0.5 {
		t.Errorf("un PATCH no válido ha cambiado los filtros: %+v", again)
	}

	for _, m := range []string{http.MethodGet, http.MethodPatch} {
		if code, _ := e.do(t, m, "/api/0/projects/999999/filters", `{}`); code != http.StatusNotFound {
			t.Errorf("%s de un proyecto que no existe = %d", m, code)
		}
	}
	req, _ := http.NewRequest(http.MethodPatch, e.srv.URL+path, strings.NewReader(`{"sample_rate": 0}`))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("PATCH sin token = %d", res.StatusCode)
	}
}

func TestDroppedAPI(t *testing.T) {
	e := setup(t)
	p := e.db.AddProject(t, "web")
	now := time.Now().UTC()
	if err := e.db.AddDropped(context.Background(), []store.Dropped{
		{ProjectID: p.ID, Reason: store.DropLocalhost, Day: now, Count: 4},
		{ProjectID: p.ID, Reason: store.DropIssueLimit, Day: now.AddDate(0, 0, -10), Count: 9900},
	}); err != nil {
		t.Fatal(err)
	}
	var st store.DroppedStats
	e.get(t, "/api/0/projects/"+itoa(p.ID)+"/dropped/", &st)
	if st.Total != 9904 || st.Reasons[store.DropLocalhost] != 4 || st.Reasons[store.DropIssueLimit] != 9900 ||
		st.Reasons[store.DropSampled] != 0 || len(st.Daily) != 2 {
		t.Fatalf("30 días = %+v", st)
	}
	e.get(t, "/api/0/projects/"+itoa(p.ID)+"/dropped?days=7", &st)
	if st.Total != 4 || len(st.Daily) != 1 || st.Daily[0].Reason != store.DropLocalhost {
		t.Fatalf("7 días = %+v", st)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/"+itoa(p.ID)+"/dropped?days=x", ""); code != http.StatusBadRequest {
		t.Errorf("days=x = %d", code)
	}
	if code, _ := e.do(t, http.MethodGet, "/api/0/projects/999999/dropped", ""); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe = %d", code)
	}
}
