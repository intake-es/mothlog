package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type uptimeChanges struct {
	mu   sync.Mutex
	list []UptimeChange
}

func (c *uptimeChanges) UptimeChanged(_ context.Context, _ pgx.Tx, ch UptimeChange) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, ch)
	return nil
}

func ptr[T any](v T) *T { return &v }

func code(n int) *int { return &n }

func TestParseStatusCodes(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"2xx", "200-299"},
		{" 200-299 , 301 ", "200-299,301"},
		{"2XX,404", "200-299,404"},
	} {
		if _, got, err := ParseStatusCodes(c.in); err != nil || got != c.want {
			t.Errorf("ParseStatusCodes(%q) = %q, %v; quería %q", c.in, got, err, c.want)
		}
	}
	for _, in := range []string{"", "abc", "99", "600", "300-200", "6xx"} {
		if _, _, err := ParseStatusCodes(in); !errors.Is(err, ErrInvalid) {
			t.Errorf("ParseStatusCodes(%q) = %v; quería ErrInvalid", in, err)
		}
	}
	if !StatusMatches("200-299,404", 404) || StatusMatches("200-299", 500) || !StatusMatches("2xx", 204) {
		t.Error("StatusMatches")
	}
}

func TestCheckUptimeURL(t *testing.T) {
	for _, bad := range []string{
		"http://169.254.169.254/latest/meta-data/",
		"http://[fe80::1]/",
		"http://[::ffff:169.254.169.254]/",
		"http://metadata.google.internal/computeMetadata/v1/",
		"http://100.100.100.200/",
		"ftp://example.com/",
		"https://usuario:clave@example.com/",
		"example.com",
	} {
		if _, err := CheckUptimeURL(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CheckUptimeURL(%q) = %v; quería ErrInvalid", bad, err)
		}
	}
	got, err := CheckUptimeURL(" https://example.com/salud#arriba ")
	if err != nil || got != "https://example.com/salud" {
		t.Errorf("CheckUptimeURL = %q, %v", got, err)
	}
	// Las privadas se deciden al pedir, con UPTIME_ALLOW_PRIVATE.
	if _, err := CheckUptimeURL("http://10.0.0.5:8080/"); err != nil {
		t.Errorf("privada: %v", err)
	}
}

func TestUptimeMonitorCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "web", randomHex(t, 16))

	m, err := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Name: ptr("Web principal"), URL: ptr("https://example.com/")})
	if err != nil {
		t.Fatal(err)
	}
	if m.Slug != "web-principal" {
		t.Errorf("slug = %q", m.Slug)
	}
	if m.Method != "GET" || m.IntervalSeconds != 60 || m.TimeoutSeconds != 10 || m.ExpectedStatus != "200-299" ||
		m.FailureThreshold != 2 || m.Status != UptimePending || m.Paused {
		t.Errorf("por defecto = %+v", m)
	}
	if _, err := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Slug: ptr(m.Slug), URL: ptr("https://example.com/")}); !errors.Is(err, ErrInvalid) {
		t.Errorf("repetido: %v", err)
	}
	for _, in := range []UptimeInput{
		{Slug: ptr("a"), URL: ptr("http://169.254.169.254/")},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), Method: ptr("POST")},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), IntervalSeconds: ptr(10)},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), IntervalSeconds: ptr(7200)},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), IntervalSeconds: ptr(30), TimeoutSeconds: ptr(45)},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), FailureThreshold: ptr(0)},
		{Slug: ptr("a"), URL: ptr("https://example.com/"), ExpectedStatus: ptr("abc")},
	} {
		if _, err := s.CreateUptimeMonitor(ctx, p.ID, in); !errors.Is(err, ErrInvalid) {
			t.Errorf("CreateUptimeMonitor(%+v) = %v; quería ErrInvalid", in, err)
		}
	}

	now := time.Now()
	m, err = s.UpdateUptimeMonitor(ctx, p.ID, m.Slug, UptimeInput{Paused: ptr(true), Method: ptr("head"),
		ExpectedStatus: ptr("2xx,301")}, now)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Paused || m.Method != "HEAD" || m.ExpectedStatus != "200-299,301" {
		t.Errorf("editado = %+v", m)
	}
	list, err := s.ListUptimeMonitors(ctx, UptimeFilter{Project: "web", Status: "paused"}, now)
	if err != nil || len(list) != 1 || list[0].Recent == nil {
		t.Fatalf("pausados = %+v, %v", list, err)
	}
	if err := s.DeleteUptimeMonitor(ctx, p.ID, m.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.UptimeMonitor(ctx, p.ID, m.Slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("borrado: %v", err)
	}
}

// Cae al llegar a failure_threshold fallos seguidos, el incidente empieza en
// el primer fallo y la primera comprobación buena lo cierra con su duración.
func TestRecordUptimeCheckIncident(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "web", randomHex(t, 16))
	m, err := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Slug: ptr("home"), URL: ptr("https://example.com/"),
		FailureThreshold: ptr(3)})
	if err != nil {
		t.Fatal(err)
	}
	n := &uptimeChanges{}
	record := func(at time.Time, ok bool, err string) string {
		t.Helper()
		c := UptimeCheck{CheckedAt: at, OK: ok, DurationMs: 120, Error: err}
		if ok {
			c.StatusCode = code(200)
		} else {
			c.StatusCode = code(503)
		}
		kind, e := s.RecordUptimeCheck(ctx, m.ID, c, n)
		if e != nil {
			t.Fatal(e)
		}
		return kind
	}
	if k := record(t0, true, ""); k != "" {
		t.Errorf("primera buena = %q", k)
	}
	// Un fallo suelto no abre nada.
	record(t0.Add(time.Minute), false, "código 503")
	record(t0.Add(2*time.Minute), true, "")
	if k := record(t0.Add(3*time.Minute), false, "código 503"); k != "" {
		t.Errorf("1 fallo = %q", k)
	}
	if k := record(t0.Add(4*time.Minute), false, "conexión rechazada"); k != "" {
		t.Errorf("2 fallos = %q", k)
	}
	if k := record(t0.Add(5*time.Minute), false, "conexión rechazada"); k != KindUptimeDown {
		t.Errorf("3 fallos = %q", k)
	}
	if k := record(t0.Add(6*time.Minute), false, "conexión rechazada"); k != "" {
		t.Errorf("sigue caído = %q", k)
	}
	got, _ := s.UptimeMonitor(ctx, p.ID, "home")
	if got.Status != UptimeDown || got.DownSince == nil || !got.DownSince.Equal(t0.Add(3*time.Minute)) || got.ConsecutiveFailures != 4 {
		t.Errorf("caído = %+v", got)
	}
	if k := record(t0.Add(10*time.Minute), true, ""); k != KindUptimeUp {
		t.Errorf("vuelve = %q", k)
	}
	if len(n.list) != 2 || n.list[0].Kind != KindUptimeDown || n.list[1].Kind != KindUptimeUp {
		t.Fatalf("avisos = %+v", n.list)
	}
	down, up := n.list[0].Incident, n.list[1].Incident
	if down.ID != up.ID || !down.StartedAt.Equal(t0.Add(3*time.Minute)) || down.FirstError != "código 503" || down.EndedAt != nil {
		t.Errorf("incidente al caer = %+v", down)
	}
	if up.EndedAt == nil || !up.EndedAt.Equal(t0.Add(10*time.Minute)) || up.DurationMs == nil || *up.DurationMs != 7*60*1000 {
		t.Errorf("incidente al volver = %+v", up)
	}

	got, _ = s.UptimeMonitor(ctx, p.ID, "home")
	d, err := s.UptimeDetail(ctx, got, t0.Add(11*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if d.Status != UptimeUp || d.DownSince != nil || len(d.Incidents) != 1 || len(d.Checks) != 8 || d.Checks[0].StatusCode == nil {
		t.Errorf("detalle = %+v", d)
	}
	// created_at es la hora real y las comprobaciones van con t0: aquí solo cabe ver que hay porcentaje.
	if d.Uptime24h == nil || *d.Uptime24h > 100 || len(d.ResponseTimes) == 0 {
		t.Errorf("uptime = %v, tiempos = %+v", d.Uptime24h, d.ResponseTimes)
	}
	var fails int
	for _, b := range d.ResponseTimes {
		fails += b.Failures
	}
	if fails != 5 {
		t.Errorf("fallos en la gráfica = %d", fails)
	}
}

func TestAvailability(t *testing.T) {
	now := t0
	created := now.Add(-48 * time.Hour)
	inc := []span{
		{from: now.Add(-36 * time.Hour), to: now.Add(-35 * time.Hour)},
		{from: now.Add(-90 * time.Minute), to: now.Add(-30 * time.Minute)},
		{from: now.Add(-6 * time.Minute), to: now},
	}
	if v := *availability(created, inc, now, 24*time.Hour); v < 95.41 || v > 95.42 {
		t.Errorf("24 h = %v; quería 100·(1 − 66/1440)", v)
	}
	// En 30 días cuenta desde que existe: 48 h.
	if v := *availability(created, inc, now, 30*24*time.Hour); v < 95.6 || v > 95.7 {
		t.Errorf("30 d = %v; quería 100·(1 − 126/2880)", v)
	}
	if v := *availability(now, nil, now, time.Hour); v != 100 {
		t.Errorf("recién creado = %v", v)
	}
}

// Dos réplicas que reservan a la vez nunca cogen el mismo monitor.
func TestClaimUptimeChecksNoDuplicates(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "web", randomHex(t, 16))
	for i := range 40 {
		if _, err := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Slug: ptr("m" + string(rune('a'+i%26)) + string(rune('a'+i/26))),
			URL: ptr("https://example.com/")}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().Add(time.Second)
	seen := map[int64]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for {
				got, err := s.ClaimUptimeChecks(ctx, now, 3)
				if err != nil {
					t.Error(err)
					return
				}
				if len(got) == 0 {
					return
				}
				mu.Lock()
				for _, g := range got {
					seen[g.ID]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if len(seen) != 40 {
		t.Errorf("reservados = %d; quería 40", len(seen))
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("monitor %d reservado %d veces", id, n)
		}
	}
	// Hasta dentro de un intervalo no toca otra vez.
	if got, _ := s.ClaimUptimeChecks(ctx, now.Add(59*time.Second), 100); len(got) != 0 {
		t.Errorf("antes de tiempo = %d", len(got))
	}
	if got, _ := s.ClaimUptimeChecks(ctx, now.Add(60*time.Second), 100); len(got) != 40 {
		t.Errorf("al minuto = %d", len(got))
	}
}

func TestPurgeUptimeChecks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "web", randomHex(t, 16))
	m, _ := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Slug: ptr("home"), URL: ptr("https://example.com/"), FailureThreshold: ptr(1)})
	now := time.Now().UTC()
	for _, age := range []time.Duration{10 * 24 * time.Hour, 8 * 24 * time.Hour, 6 * 24 * time.Hour, time.Hour} {
		if _, err := s.RecordUptimeCheck(ctx, m.ID, UptimeCheck{CheckedAt: now.Add(-age), OK: age > 7*24*time.Hour, DurationMs: 50}, nil); err != nil {
			t.Fatal(err)
		}
	}
	n, err := s.PurgeUptimeChecks(ctx, now.Add(-7*24*time.Hour))
	if err != nil || n != 2 {
		t.Fatalf("borradas = %d, %v", n, err)
	}
	d, _ := s.UptimeDetail(ctx, m, now)
	if len(d.Checks) != 2 || len(d.Incidents) != 1 {
		t.Errorf("quedan %d comprobaciones y %d incidentes", len(d.Checks), len(d.Incidents))
	}
}

// Al pausar un monitor caído, su incidente se cierra a esa hora: ni la pausa
// cuenta como tiempo caído ni la vuelta tras reanudarlo avisa con una duración falsa.
func TestPauseClosesIncident(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "web", randomHex(t, 16))
	m, err := s.CreateUptimeMonitor(ctx, p.ID, UptimeInput{Slug: ptr("home"), URL: ptr("https://example.com/"),
		FailureThreshold: ptr(1)})
	if err != nil {
		t.Fatal(err)
	}
	n := &uptimeChanges{}
	now := time.Now().UTC().Truncate(time.Second)
	fail := UptimeCheck{CheckedAt: now.Add(-10 * time.Minute), StatusCode: code(503), DurationMs: 40, Error: "código 503"}
	if k, err := s.RecordUptimeCheck(ctx, m.ID, fail, n); err != nil || k != KindUptimeDown {
		t.Fatalf("caída = %q, %v", k, err)
	}
	pausedAt := now.Add(-8 * time.Minute)
	m, err = s.UpdateUptimeMonitor(ctx, p.ID, "home", UptimeInput{Paused: ptr(true)}, pausedAt)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Paused || m.Status != UptimePending || m.DownSince != nil || m.ConsecutiveFailures != 0 {
		t.Errorf("pausado = %+v", m)
	}
	d, err := s.UptimeDetail(ctx, m, now)
	if err != nil {
		t.Fatal(err)
	}
	inc := d.Incidents[0]
	if inc.EndedAt == nil || !inc.EndedAt.Equal(pausedAt) || inc.DurationMs == nil || *inc.DurationMs != 2*60*1000 {
		t.Errorf("incidente = %+v", inc)
	}

	// Reanudado, la primera buena no avisa de ninguna vuelta.
	if _, err := s.UpdateUptimeMonitor(ctx, p.ID, "home", UptimeInput{Paused: ptr(false)}, now); err != nil {
		t.Fatal(err)
	}
	ok := UptimeCheck{CheckedAt: now, OK: true, StatusCode: code(200), DurationMs: 40}
	if k, err := s.RecordUptimeCheck(ctx, m.ID, ok, n); err != nil || k != "" {
		t.Errorf("vuelta tras reanudar = %q, %v", k, err)
	}
	if len(n.list) != 1 {
		t.Errorf("avisos = %+v", n.list)
	}
	got, _ := s.UptimeMonitor(ctx, p.ID, "home")
	if got.Status != UptimeUp {
		t.Errorf("estado = %q", got.Status)
	}
}
