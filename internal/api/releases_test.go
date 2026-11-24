package api_test

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

func TestEnvironmentFilter(t *testing.T) {
	e := setup(t)
	back := e.db.AddProject(t, "backend")
	front := e.db.AddProject(t, "front")
	now := time.Now().UTC().Truncate(time.Second)
	ev := storetest.Fixture(t, "api-excepcion-1")
	event := func(project, issue int64, env string, at time.Time) {
		ev.Environment = env
		e.db.AddEvent(t, project, issue, ev, at)
	}

	// prod sale en los dos entornos; staging, solo en preproducción; front, en producción.
	prod := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "En los dos", FirstSeen: now.Add(-5 * time.Hour), LastSeen: now, Count: 4})
	event(back.ID, prod, "production", now.Add(-5*time.Hour))
	event(back.ID, prod, "production", now.Add(-4*time.Hour))
	event(back.ID, prod, "staging", now.Add(-3*time.Hour))
	event(back.ID, prod, "staging", now)
	staging := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "Solo staging", FirstSeen: now.Add(-2 * time.Hour), LastSeen: now.Add(-time.Hour), Count: 1})
	event(back.ID, staging, "staging", now.Add(-time.Hour))
	web := e.db.AddIssue(t, storetest.Issue{ProjectID: front.ID, Title: "Front", FirstSeen: now.Add(-time.Hour), LastSeen: now.Add(-time.Hour), Count: 1})
	event(front.ID, web, "production", now.Add(-time.Hour))

	for _, tc := range []struct {
		query string
		want  []int64
	}{
		{"", []int64{prod, web, staging}},
		{"environment=production", []int64{web, prod}},
		{"environment=staging", []int64{prod, staging}},
		{"environment=staging&sort=first_seen", []int64{staging, prod}},
		{"environment=production&project=backend", []int64{prod}},
		{"environment=otro", []int64{}},
	} {
		var page store.IssuePage
		e.get(t, "/api/0/issues/?"+tc.query, &page)
		if got := ids(page.Issues); !slices.Equal(got, tc.want) {
			t.Errorf("%s: %v; quiero %v", tc.query, got, tc.want)
		}
	}

	// Los recuentos y la gráfica son los del entorno.
	var page struct {
		Data []struct {
			store.Issue
			Daily []store.DayCount `json:"daily"`
		} `json:"data"`
	}
	e.get(t, "/api/0/issues/?daily=1&environment=production&project=backend", &page)
	if len(page.Data) != 1 {
		t.Fatalf("= %+v", page.Data)
	}
	got := page.Data[0]
	total := int64(0)
	for _, d := range got.Daily {
		total += d.Count
	}
	if got.Count != 2 || !got.LastSeen.Equal(now.Add(-4*time.Hour)) || total != 2 {
		t.Errorf("en production: %d eventos, última vez %v, %d en la gráfica", got.Count, got.LastSeen, total)
	}

	var d store.IssueDetail
	e.get(t, "/api/0/issues/"+itoa(prod)+"/?environment=staging", &d)
	if d.Count != 2 || d.LatestEvent == nil || d.LatestEvent.Event.Environment != "staging" ||
		len(d.TopValues["environment"]) != 1 || d.TopValues["environment"][0].Value != "staging" {
		t.Errorf("detalle en staging: %d eventos, último %+v, entornos %+v", d.Count, d.LatestEvent, d.TopValues["environment"])
	}
	e.get(t, "/api/0/issues/"+itoa(prod)+"/", &d)
	if d.Count != 4 || len(d.TopValues["environment"]) != 2 {
		t.Errorf("detalle sin entorno: %d eventos, entornos %+v", d.Count, d.TopValues["environment"])
	}

	var events store.EventPage
	e.get(t, "/api/0/issues/"+itoa(prod)+"/events/?environment=production", &events)
	if len(events.Events) != 2 || events.Events[0].Environment != "production" {
		t.Errorf("eventos en production = %+v", events.Events)
	}

	var envs struct {
		Data []store.Environment `json:"data"`
	}
	e.get(t, "/api/0/environments/", &envs)
	if len(envs.Data) != 2 || envs.Data[0].Name != "staging" || envs.Data[0].Issues != 2 || envs.Data[1].Name != "production" || envs.Data[1].Events != 3 {
		t.Errorf("entornos = %+v", envs.Data)
	}
	e.get(t, "/api/0/projects/"+itoa(front.ID)+"/environments/", &envs)
	if len(envs.Data) != 1 || envs.Data[0].Name != "production" {
		t.Errorf("entornos de front = %+v", envs.Data)
	}
}

func TestReleasesAPI(t *testing.T) {
	e := setup(t)
	back := e.db.AddProject(t, "backend")
	base := "/api/0/projects/" + itoa(back.ID) + "/releases/"

	code, body := e.do(t, http.MethodPost, base, `{"version": "api@1.0.0"}`)
	if code != http.StatusCreated {
		t.Fatalf("crear = %d %s", code, body)
	}
	if code, body = e.do(t, http.MethodPost, base, `{"version": "api@1.0.0"}`); code != http.StatusOK {
		t.Errorf("crear otra vez = %d %s", code, body)
	}
	for _, bad := range []string{`{}`, `{"version": "  "}`, `{"version": "a\nb"}`, `no es json`} {
		if code, body := e.do(t, http.MethodPost, base, bad); code != http.StatusBadRequest {
			t.Errorf("%s = %d %s", bad, code, body)
		}
	}
	if code, _ := e.do(t, http.MethodPost, "/api/0/projects/999/releases/", `{"version": "x"}`); code != http.StatusNotFound {
		t.Errorf("proyecto que no existe = %d", code)
	}

	// Un error que sale con 1.0.0, se marca para la siguiente y se arregla en 1.1.0.
	ev := storetest.Fixture(t, "api-excepcion-1")
	ev.Release, ev.Environment = "api@1.0.0", "production"
	v1 := "api@1.0.0"
	fixed := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "Arreglado en 1.1.0", Count: 1, FirstRelease: &v1})
	e.db.AddEvent(t, back.ID, fixed, ev, time.Now().Add(-time.Hour))
	code, body = e.do(t, http.MethodPost, "/api/0/issues/"+itoa(fixed)+"/status", `{"status": "resolved_next"}`)
	if code != http.StatusOK {
		t.Fatalf("resolved_next = %d %s", code, body)
	}

	// Crear y desplegar a la vez, con el entorno en el cuerpo.
	var created struct {
		store.Release
		Deploy   *store.Deploy `json:"deploy"`
		Resolved []int64       `json:"resolved_issues"`
	}
	code, body = e.do(t, http.MethodPost, base, `{"version": "api@1.1.0", "environment": "production"}`)
	if code != http.StatusCreated {
		t.Fatalf("crear y desplegar = %d %s", code, body)
	}
	decode(t, body, &created)
	if created.Deploy == nil || created.Deploy.Environment != "production" || len(created.Deploys) != 1 || !slices.Equal(created.Resolved, []int64{fixed}) {
		t.Errorf("crear y desplegar = %s", body)
	}

	v11 := "api@1.1.0"
	ev.Release = v11
	brought := e.db.AddIssue(t, storetest.Issue{ProjectID: back.ID, Title: "Nuevo en 1.1.0", Count: 1, FirstRelease: &v11})
	e.db.AddEvent(t, back.ID, brought, ev, time.Now())

	var deploy struct {
		store.Deploy
		Resolved []int64 `json:"resolved_issues"`
	}
	code, body = e.do(t, http.MethodPost, base+url.PathEscape(v11)+"/deploys/", `{"environment": "staging"}`)
	if code != http.StatusCreated {
		t.Fatalf("desplegar = %d %s", code, body)
	}
	decode(t, body, &deploy)
	if deploy.Environment != "staging" || len(deploy.Resolved) != 0 {
		t.Errorf("desplegar = %s", body)
	}
	if code, body := e.do(t, http.MethodPost, base+"api@1.1.0/deploys/", `{}`); code != http.StatusBadRequest {
		t.Errorf("sin entorno = %d %s", code, body)
	}

	var deploys struct {
		Data []store.Deploy `json:"data"`
	}
	e.get(t, base+url.PathEscape(v11)+"/deploys/", &deploys)
	if len(deploys.Data) != 2 || deploys.Data[0].Environment != "staging" {
		t.Errorf("despliegues = %+v", deploys.Data)
	}

	var list store.ReleasePage
	e.get(t, base, &list)
	if len(list.Releases) != 2 || list.Releases[0].Version != v11 || list.Releases[0].NewIssues != 1 || len(list.Releases[0].Deploys) != 2 {
		t.Errorf("releases = %+v", list.Releases)
	}
	e.get(t, "/api/0/releases/?project=backend&environment=staging", &list)
	if len(list.Releases) != 1 || list.Releases[0].Version != v11 || len(list.Releases[0].Deploys) != 1 {
		t.Errorf("releases en staging = %+v", list.Releases)
	}

	var d store.ReleaseDetail
	e.get(t, base+url.PathEscape(v11)+"/", &d)
	if len(d.New) != 1 || d.New[0].ID != brought || len(d.Stopped) != 1 || d.Stopped[0].ID != fixed ||
		d.Previous == nil || *d.Previous != v1 {
		t.Errorf("detalle de 1.1.0: nuevos %+v, dejaron de salir %+v, anterior %v", d.New, d.Stopped, d.Previous)
	}
	if code, _ := e.do(t, http.MethodGet, base+"no-existe/", ""); code != http.StatusNotFound {
		t.Errorf("release que no existe = %d", code)
	}

	var issue store.Issue
	e.get(t, "/api/0/issues/"+itoa(fixed)+"/", &issue)
	if issue.Status != "resolved" || issue.FixedInRelease == nil || *issue.FixedInRelease != v11 || issue.FirstRelease == nil || *issue.FirstRelease != v1 {
		t.Errorf("issue arreglado = %+v", issue)
	}
}

func decode(t *testing.T, b []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("%v: %s", err, b)
	}
}
