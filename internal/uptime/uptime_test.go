package uptime

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/store/storetest"
)

// site es un servidor de prueba que se puede tirar y levantar.
type site struct {
	up   atomic.Bool
	hits atomic.Int64
	srv  *httptest.Server
}

func newSite(t *testing.T) *site {
	s := &site{}
	s.up.Store(true)
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		s.hits.Add(1)
		if !s.up.Load() {
			// Caído: corta la conexión sin responder.
			if hj, ok := w.(http.Hijacker); ok {
				if conn, _, err := hj.Hijack(); err == nil {
					_ = conn.Close()
					return
				}
			}
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(s.srv.Close)
	return s
}

type hook struct {
	mu   sync.Mutex
	reqs []alert.WebhookPayload
}

func (h *hook) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var pl alert.WebhookPayload
	_ = json.Unmarshal(body, &pl)
	h.mu.Lock()
	h.reqs = append(h.reqs, pl)
	h.mu.Unlock()
}

func (h *hook) all() []alert.WebhookPayload {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]alert.WebhookPayload(nil), h.reqs...)
}

type slackMsgs struct {
	mu   sync.Mutex
	text []string
}

func (s *slackMsgs) ServeHTTP(_ http.ResponseWriter, r *http.Request) {
	var m struct{ Text string }
	_ = json.NewDecoder(r.Body).Decode(&m)
	s.mu.Lock()
	s.text = append(s.text, m.Text)
	s.mu.Unlock()
}

func (s *slackMsgs) all() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.text...)
}

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type env struct {
	db      *storetest.DB
	alerter *alert.Alerter
	slack   *slackMsgs
	clock   *clock
	project store.Project
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func newEnv(t *testing.T) *env {
	t.Helper()
	db := storetest.NewDB(t)
	e := &env{db: db, slack: &slackMsgs{}, clock: &clock{t: time.Now().UTC().Truncate(time.Second).Add(time.Second)}}
	slackSrv := httptest.NewServer(e.slack)
	t.Cleanup(slackSrv.Close)
	a, err := alert.New(db.Store, quiet(), alert.Options{PublicURL: "https://errores.test", SlackWebhookURL: slackSrv.URL + "/services/T/B/x"})
	if err != nil {
		t.Fatal(err)
	}
	e.alerter = a
	e.project = db.AddProject(t, "web")
	return e
}

func (e *env) service() *Service {
	s := New(e.db.Store, NewChecker(true, nil), e.alerter, quiet())
	s.Now = e.clock.now
	return s
}

func (e *env) monitor(t *testing.T, slug, u string) store.UptimeMonitor {
	t.Helper()
	m, err := e.db.CreateUptimeMonitor(context.Background(), e.project.ID, store.UptimeInput{Slug: &slug, URL: &u})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func (e *env) tick(t *testing.T, svcs ...*Service) {
	t.Helper()
	var wg sync.WaitGroup
	for _, s := range svcs {
		wg.Go(func() {
			if _, err := s.Tick(context.Background()); err != nil {
				t.Error(err)
			}
			s.Wait()
		})
	}
	wg.Wait()
	e.clock.add(time.Minute)
}

func (e *env) send(t *testing.T) {
	t.Helper()
	if _, err := e.alerter.SendPending(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// Sin reglas: cae tras dos fallos seguidos, vuelve, el incidente guarda su
// duración y los canales por defecto avisan de las dos cosas.
func TestDownAndUpDefaultChannels(t *testing.T) {
	e := newEnv(t)
	web := newSite(t)
	e.monitor(t, "home", web.srv.URL+"/")
	svc := e.service()
	t0 := e.clock.now()

	e.tick(t, svc) // bien
	web.up.Store(false)
	e.tick(t, svc) // 1 fallo
	e.send(t)
	if got := e.slack.all(); len(got) != 0 {
		t.Fatalf("avisa con un fallo: %v", got)
	}
	e.tick(t, svc) // 2 fallos: caído
	e.tick(t, svc)
	web.up.Store(true)
	e.tick(t, svc) // vuelve
	e.send(t)

	got := e.slack.all()
	if len(got) != 2 || got[0] != "Monitor caído en web: home" || got[1] != "Monitor recuperado en web: home" {
		t.Fatalf("slack = %q", got)
	}
	m, _ := e.db.UptimeMonitor(context.Background(), e.project.ID, "home")
	d, err := e.db.UptimeDetail(context.Background(), m, e.clock.now())
	if err != nil {
		t.Fatal(err)
	}
	if m.Status != store.UptimeUp || len(d.Incidents) != 1 || len(d.Checks) != 5 {
		t.Fatalf("detalle = %+v", d)
	}
	inc := d.Incidents[0]
	if !inc.StartedAt.Equal(t0.Add(time.Minute)) || inc.EndedAt == nil || !inc.EndedAt.Equal(t0.Add(4*time.Minute)) ||
		inc.DurationMs == nil || *inc.DurationMs != 3*60*1000 || inc.FirstError != "conexión cerrada sin respuesta" {
		t.Errorf("incidente = %+v", inc)
	}
}

// Con una regla de disponibilidad, el webhook recibe la caída y la vuelta con
// el incidente, y los canales por defecto ya no avisan.
func TestWebhookRule(t *testing.T) {
	e := newEnv(t)
	web := newSite(t)
	other := newSite(t)
	h := &hook{}
	hookSrv := httptest.NewServer(h)
	t.Cleanup(hookSrv.Close)
	if _, _, err := e.db.CreateRule(context.Background(), e.project.ID, store.Rule{Name: "web caída", Enabled: true,
		Conditions: store.RuleConditions{Uptime: &store.UptimeCondition{Down: true, Up: true, Monitors: []string{"home"}}},
		Actions:    []store.RuleAction{{Type: store.ActionWebhook, URL: hookSrv.URL + "/hook"}}}); err != nil {
		t.Fatal(err)
	}
	e.monitor(t, "home", web.srv.URL+"/")
	e.monitor(t, "otra", other.srv.URL+"/")
	svc := e.service()
	t0 := e.clock.now()

	web.up.Store(false)
	other.up.Store(false)
	e.tick(t, svc)
	e.tick(t, svc)
	e.tick(t, svc)
	web.up.Store(true)
	e.tick(t, svc)
	e.send(t)

	got := h.all()
	if len(got) != 2 || got[0].Type != store.KindUptimeDown || got[1].Type != store.KindUptimeUp {
		t.Fatalf("webhook = %+v", got)
	}
	down, up := got[0], got[1]
	if down.Uptime == nil || down.Uptime.Slug != "home" || down.Uptime.CheckURL != web.srv.URL+"/" ||
		down.Uptime.URL != "https://errores.test/monitors/"+itoa(e.project.ID)+"/uptime/home" ||
		down.Incident == nil || !down.Incident.StartedAt.Equal(t0) || down.Incident.EndedAt != nil || down.Issue != nil ||
		!strings.HasPrefix(down.Markdown, "# Monitor caído: home") {
		t.Errorf("caída = %+v", down)
	}
	if up.Incident == nil || up.Incident.DurationMs == nil || *up.Incident.DurationMs != 3*60*1000 ||
		!strings.Contains(up.Markdown, "Caído durante: 3 min 0 s") {
		t.Errorf("vuelta = %+v %s", up.Incident, up.Markdown)
	}
	if s := e.slack.all(); len(s) != 0 {
		t.Errorf("slack con regla = %v", s)
	}
}

// Dos réplicas a la vez: cada comprobación se hace una vez y cada aviso sale una vez.
func TestTwoReplicasNoDuplicates(t *testing.T) {
	e := newEnv(t)
	sites := []*site{newSite(t), newSite(t), newSite(t)}
	for i, s := range sites {
		e.monitor(t, "m"+itoa(int64(i)), s.srv.URL+"/")
	}
	a, b := e.service(), e.service()
	const rounds = 6
	for i := range rounds {
		for _, s := range sites {
			s.up.Store(i < 2 || i >= 4)
		}
		e.tick(t, a, b)
	}
	e.send(t)
	for i, s := range sites {
		if n := s.hits.Load(); n != rounds {
			t.Errorf("sitio %d pedido %d veces; quería %d", i, n, rounds)
		}
	}
	var checks, incidents, outbox int
	ctx := context.Background()
	_ = e.db.Pool().QueryRow(ctx, `SELECT count(*) FROM uptime_checks`).Scan(&checks)
	_ = e.db.Pool().QueryRow(ctx, `SELECT count(*) FROM uptime_incidents`).Scan(&incidents)
	_ = e.db.Pool().QueryRow(ctx, `SELECT count(*) FROM alert_outbox`).Scan(&outbox)
	if checks != 3*rounds || incidents != 3 || outbox != 6 || len(e.slack.all()) != 6 {
		t.Errorf("comprobaciones = %d, incidentes = %d, avisos = %d, slack = %d", checks, incidents, outbox, len(e.slack.all()))
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Los monitores lentos no retrasan a los demás: Tick lanza lo que cabe sin
// esperar, el siguiente coge el resto en cuanto hay hueco, y cada comprobación
// lleva la hora en que empezó su petición.
func TestSlowChecksDontHoldOthers(t *testing.T) {
	e := newEnv(t)
	release := make(chan struct{})
	var arrived atomic.Int32
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		arrived.Add(1)
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { slow.Close() })
	fast := newSite(t)
	ctx := context.Background()
	for i := range parallel {
		e.monitor(t, "lento-"+itoa(int64(i)), slow.URL+"/")
	}
	e.monitor(t, "rapido", fast.srv.URL+"/")
	svc := e.service()
	t0 := e.clock.now()

	start := time.Now()
	n, err := svc.Tick(ctx)
	if err != nil || n != parallel || time.Since(start) > time.Second {
		t.Fatalf("primer tick = %d, %v en %s", n, err, time.Since(start))
	}
	// Sin huecos no reserva más: el rápido queda para cuando los haya.
	if n, _ := svc.Tick(ctx); n != 0 {
		t.Errorf("sin huecos = %d", n)
	}
	// Con las peticiones lentas ya en marcha, pasa el tiempo.
	for arrived.Load() < parallel {
		time.Sleep(10 * time.Millisecond)
	}
	e.clock.add(10 * time.Second)
	close(release)
	svc.Wait()
	if n, _ := svc.Tick(ctx); n != 1 {
		t.Errorf("segundo tick = %d", n)
	}
	svc.Wait()

	rows, err := e.db.Pool().Query(ctx, `SELECT m.slug, c.checked_at FROM uptime_checks c JOIN uptime_monitors m ON m.id = c.monitor_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var slug string
		var at time.Time
		if err := rows.Scan(&slug, &at); err != nil {
			t.Fatal(err)
		}
		seen++
		want := t0
		if slug == "rapido" {
			want = t0.Add(10 * time.Second)
		}
		if !at.Equal(want) {
			t.Errorf("%s checked_at = %s; quería %s", slug, at, want)
		}
	}
	if seen != parallel+1 || fast.hits.Load() != 1 {
		t.Errorf("comprobaciones = %d, peticiones al rápido = %d", seen, fast.hits.Load())
	}
}
