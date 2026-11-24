package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/cron"
	"github.com/intake-es/mothlog/internal/db"
)

type changes struct {
	mu   sync.Mutex
	list []MonitorChange
}

func (c *changes) MonitorChanged(_ context.Context, _ db.Tx, ch MonitorChange) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.list = append(c.list, ch)
	return nil
}

func (c *changes) kinds() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, ch := range c.list {
		out = append(out, ch.Monitor.Slug+":"+ch.Kind)
	}
	return out
}

func every(n int, unit string) *MonitorConfig {
	return &MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeInterval, Every: n, Unit: unit}}
}

func checkIn(t *testing.T, s *Store, projectID int64, in CheckInInput, at time.Time, n MonitorNotifier) CheckInResult {
	t.Helper()
	res, err := s.ProcessCheckIn(context.Background(), projectID, in, at, n)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var t0 = time.Date(2026, 11, 22, 10, 0, 30, 0, time.UTC)

func TestCheckInCreatesAndUpdatesMonitor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))

	// Sin monitor_config, un monitor que no existe no se crea.
	res := checkIn(t, s, p.ID, CheckInInput{Slug: "copia", Status: "ok", Environment: "production"}, t0, nil)
	if res.Outcome != CheckInIgnored {
		t.Fatalf("sin monitor_config = %+v", res)
	}
	margin, runtime := 5, 10
	cfg := &MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "0  3 * * *"}, Timezone: "Europe/Madrid",
		CheckinMargin: &margin, MaxRuntime: &runtime}
	res = checkIn(t, s, p.ID, CheckInInput{Slug: "Copia", Status: "in_progress", Environment: "production", Config: cfg}, t0, nil)
	if !res.Created || res.Outcome != CheckInCreated || res.Monitor.Slug != "copia" {
		t.Fatalf("crear = %+v", res)
	}
	m, err := s.Monitor(ctx, p.ID, "copia")
	if err != nil {
		t.Fatal(err)
	}
	if m.Schedule.Crontab != "0 3 * * *" || m.Timezone != "Europe/Madrid" || m.CheckinMargin != 5 || m.MaxRuntime != 10 ||
		m.Environment != "" || m.Status != MonitorPending {
		t.Errorf("monitor = %+v", m)
	}
	if want := time.Date(2026, 11, 23, 2, 0, 0, 0, time.UTC); !m.NextCheckinAt.Equal(want) {
		t.Errorf("siguiente = %v; quería %v", m.NextCheckinAt, want)
	}

	// Otro monitor_config lo cambia; lo que no trae vuelve a su valor por defecto.
	res = checkIn(t, s, p.ID, CheckInInput{Slug: "copia", Status: "ok", Environment: "production",
		CheckInID: res.CheckIn.CheckInID, Config: every(2, "hour")}, t0.Add(time.Minute), nil)
	if res.Outcome != CheckInUpdated || res.CheckIn.Status != CheckInOK || *res.CheckIn.DurationMs != 60_000 {
		t.Fatalf("cerrar = %+v", res.CheckIn)
	}
	m, _ = s.Monitor(ctx, p.ID, "copia")
	if m.Schedule.Type != cron.TypeInterval || m.Schedule.Every != 2 || m.Timezone != "UTC" || m.CheckinMargin != 1 ||
		m.MaxRuntime != 30 || m.Status != CheckInOK {
		t.Errorf("monitor = %+v", m)
	}
	// Con otro horario, la siguiente se cuenta desde el último check-in.
	if want := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC); !m.NextCheckinAt.Equal(want) {
		t.Errorf("siguiente = %v; quería %v", m.NextCheckinAt, want)
	}

	// Un monitor_config no válido no cambia nada, pero el check-in vale.
	res = checkIn(t, s, p.ID, CheckInInput{Slug: "copia", Status: "ok", Environment: "production",
		Config: &MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "61 * * * *"}}}, t0.Add(2*time.Minute), nil)
	if res.Outcome != CheckInCreated || res.Reason == "" {
		t.Errorf("config mala = %+v", res)
	}
	// Creado por un check-in vigila cualquier entorno: una prueba en local no ata el monitor.
	res = checkIn(t, s, p.ID, CheckInInput{Slug: "copia", Status: "error", Environment: "development"}, t0.Add(3*time.Minute), nil)
	if res.Outcome != CheckInCreated {
		t.Errorf("development = %+v", res)
	}
	// Con el entorno fijado por la API, los de otro se ignoran.
	prod := "production"
	if _, err := s.UpdateMonitor(ctx, p.ID, "copia", MonitorInput{Environment: &prod}); err != nil {
		t.Fatal(err)
	}
	res = checkIn(t, s, p.ID, CheckInInput{Slug: "copia", Status: "ok", Environment: "staging"}, t0.Add(4*time.Minute), nil)
	if res.Outcome != CheckInIgnored {
		t.Errorf("staging = %+v", res)
	}
	m, _ = s.Monitor(ctx, p.ID, "copia")
	if m.Schedule.Every != 2 || m.Status != CheckInError {
		t.Errorf("monitor = %+v", m)
	}
}

// Un check-in solo crea monitores hasta MaxAutoMonitors por proyecto; la API, más.
func TestAutoMonitorsCap(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	if _, err := s.pool.Exec(ctx, `
		WITH RECURSIVE g(n) AS (SELECT 1 UNION ALL SELECT n + 1 FROM g WHERE n < $2)
		INSERT INTO monitors (project_id, slug, name, schedule_type, schedule)
		SELECT $1, 'm' || n, 'm' || n, 'crontab', '* * * * *' FROM g`, p.ID, MaxAutoMonitors); err != nil {
		t.Fatal(err)
	}
	res := checkIn(t, s, p.ID, CheckInInput{Slug: "uno-mas", Status: "ok", Config: every(1, "hour")}, t0, nil)
	if res.Outcome != CheckInIgnored || res.Created {
		t.Errorf("por encima del tope = %+v", res)
	}
	// Los que ya existen siguen recibiendo check-ins.
	if res := checkIn(t, s, p.ID, CheckInInput{Slug: "m1", Status: "ok"}, t0, nil); res.Outcome != CheckInCreated {
		t.Errorf("existente = %+v", res)
	}
	name := "uno-mas"
	sched := cron.Schedule{Type: cron.TypeInterval, Every: 1, Unit: "hour"}
	if _, err := s.CreateMonitor(ctx, p.ID, MonitorInput{Name: &name, Schedule: &sched}); err != nil {
		t.Errorf("por la API: %v", err)
	}
}

func TestMonitorTransitions(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	n := &changes{}
	at := t0
	send := func(status string) {
		at = at.Add(time.Minute)
		checkIn(t, s, p.ID, CheckInInput{Slug: "tarea", Status: status, Config: every(1, "minute")}, at, n)
	}
	for _, st := range []string{"ok", "error", "error", "ok", "ok", "error"} {
		send(st)
	}
	// Cada minuto, cada uno es el que tocaba; uno a mano, antes de su hora, no.
	m, _ := s.Monitor(ctx, p.ID, "tarea")
	page, _ := s.CheckIns(ctx, m.ID, "", 1)
	if c := page.CheckIns[0]; c.ExpectedAt == nil || !c.ExpectedAt.Equal(t0.Add(6*time.Minute).Truncate(time.Minute)) ||
		len(c.CheckInID) != 32 {
		t.Errorf("check-in = %+v", c)
	}
	hourly := &MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "0 * * * *"}}
	checkIn(t, s, p.ID, CheckInInput{Slug: "horaria", Status: "ok", Config: hourly}, t0, nil)
	early := checkIn(t, s, p.ID, CheckInInput{Slug: "horaria", Status: "ok"}, t0.Add(5*time.Minute), nil)
	onTime := checkIn(t, s, p.ID, CheckInInput{Slug: "horaria", Status: "ok"}, t0.Add(59*time.Minute), nil)
	if early.CheckIn.ExpectedAt != nil || onTime.CheckIn.ExpectedAt == nil ||
		!onTime.CheckIn.ExpectedAt.Equal(time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("a mano = %v; a su hora = %v", early.CheckIn.ExpectedAt, onTime.CheckIn.ExpectedAt)
	}
	want := []string{"tarea:cron_failed", "tarea:cron_recovered", "tarea:cron_failed"}
	if got := n.kinds(); !slices.Equal(got, want) {
		t.Errorf("avisos = %v; quería %v", got, want)
	}
	// Silenciado no avisa, pero el estado cambia.
	muted := true
	if _, err := s.UpdateMonitor(ctx, p.ID, "tarea", MonitorInput{Muted: &muted}); err != nil {
		t.Fatal(err)
	}
	send("ok")
	if got := n.kinds(); len(got) != 3 {
		t.Errorf("silenciado avisa: %v", got)
	}
	if m, _ := s.Monitor(ctx, p.ID, "tarea"); m.Status != CheckInOK || !m.Muted {
		t.Errorf("monitor = %+v", m)
	}
}

func TestSweepMissedAndTimeout(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	n := &changes{}
	margin, runtime := 2, 5
	cfg := &MonitorConfig{Schedule: cron.Schedule{Type: cron.TypeCrontab, Crontab: "*/10 * * * *"}, CheckinMargin: &margin, MaxRuntime: &runtime}
	// 10:00:30: empieza y acaba. Toca a las 10:10, con margen hasta las 10:12.
	checkIn(t, s, p.ID, CheckInInput{Slug: "deja-de-ir", Status: "ok", Config: cfg}, t0, n)
	// 10:00:30: empieza y se queda colgado; puede durar hasta las 10:05:30.
	hung := checkIn(t, s, p.ID, CheckInInput{Slug: "colgado", Status: "in_progress", Config: cfg}, t0, n)

	sweep := func(at time.Time) (int, int) {
		t.Helper()
		missed, timeouts, err := s.SweepMonitors(ctx, at, n)
		if err != nil {
			t.Fatal(err)
		}
		return missed, timeouts
	}
	if m, to := sweep(t0.Add(5 * time.Minute)); m != 0 || to != 0 {
		t.Fatalf("10:05:30: %d missed, %d timeout", m, to)
	}
	if m, to := sweep(t0.Add(5*time.Minute + time.Second)); m != 0 || to != 1 {
		t.Fatalf("10:05:31: %d missed, %d timeout", m, to)
	}
	if got := n.kinds(); !slices.Equal(got, []string{"colgado:cron_timeout"}) {
		t.Fatalf("avisos = %v", got)
	}
	// El ok que llega tarde guarda la duración, pero sigue en timeout.
	res := checkIn(t, s, p.ID, CheckInInput{Slug: "colgado", Status: "ok", CheckInID: hung.CheckIn.CheckInID}, t0.Add(8*time.Minute), n)
	if res.CheckIn.Status != CheckInTimeout || *res.CheckIn.DurationMs != 8*60_000 {
		t.Errorf("ok tarde = %+v", res.CheckIn)
	}
	// El colgado también toca a las 10:10: a las 10:12 faltan los dos.
	if m, _ := sweep(time.Date(2026, 11, 22, 10, 11, 59, 0, time.UTC)); m != 0 {
		t.Fatalf("10:11:59: %d missed", m)
	}
	if m, _ := sweep(time.Date(2026, 11, 22, 10, 12, 0, 0, time.UTC)); m != 2 {
		t.Fatalf("10:12: %d missed", m)
	}
	mon, _ := s.Monitor(ctx, p.ID, "deja-de-ir")
	if mon.Status != CheckInMissed || !mon.NextCheckinAt.Equal(time.Date(2026, 11, 22, 10, 20, 0, 0, time.UTC)) {
		t.Errorf("monitor = %+v", mon)
	}
	page, _ := s.CheckIns(ctx, mon.ID, "", 10)
	if c := page.CheckIns[0]; c.Status != CheckInMissed || !c.ExpectedAt.Equal(time.Date(2026, 11, 22, 10, 10, 0, 0, time.UTC)) {
		t.Errorf("missed = %+v", c)
	}
	// Tras horas sin nada, un solo missed más y la siguiente es la que aún puede llegar.
	if m, _ := sweep(time.Date(2026, 11, 22, 15, 1, 0, 0, time.UTC)); m != 2 {
		t.Fatalf("15:01: %d missed", m)
	}
	mon, _ = s.Monitor(ctx, p.ID, "deja-de-ir")
	// A las 15:01, la de las 15:00 aún está dentro del margen.
	if !mon.NextCheckinAt.Equal(time.Date(2026, 11, 22, 15, 0, 0, 0, time.UTC)) {
		t.Errorf("siguiente = %v", mon.NextCheckinAt)
	}
	// missed→missed no vuelve a avisar; timeout→missed sí.
	want := []string{"colgado:cron_timeout", "deja-de-ir:cron_missed", "colgado:cron_missed"}
	got := n.kinds()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("avisos = %v; quería %v", got, want)
	}
	// Vuelve a ir: se recupera.
	checkIn(t, s, p.ID, CheckInInput{Slug: "deja-de-ir", Status: "ok"}, time.Date(2026, 11, 22, 15, 1, 5, 0, time.UTC), n)
	if got := n.kinds(); got[len(got)-1] != "deja-de-ir:cron_recovered" {
		t.Errorf("avisos = %v", got)
	}
}

// Con dos réplicas (dos Store con su propio pool, como dos procesos)
// repasando a la vez, cada missed y cada timeout se marcan una vez.
func TestSweepConcurrentNoDuplicates(t *testing.T) {
	ctx := context.Background()
	u := testSchemaURL(t, "dos réplicas sobre la misma base se reparten el trabajo con FOR UPDATE SKIP LOCKED; con SQLite corre una sola instancia")
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	replicas := make([]*Store, 2)
	for i := range replicas {
		r, err := Open(ctx, u, log)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(r.Close)
		replicas[i] = r
	}
	s := replicas[0]
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	const monitors = 20
	runtime := 1
	for i := range monitors {
		slug := "m" + string(rune('a'+i))
		cfg := every(1, "minute")
		cfg.MaxRuntime = &runtime
		checkIn(t, s, p.ID, CheckInInput{Slug: slug, Status: "ok", Config: cfg}, t0, nil)
		checkIn(t, s, p.ID, CheckInInput{Slug: slug, Status: "in_progress"}, t0.Add(time.Second), nil)
	}
	n := &changes{}
	at := t0.Add(10 * time.Minute)
	const workers = 4
	var wg sync.WaitGroup
	totals := make([][2]int, workers)
	errs := make([]error, workers)
	for w := range workers {
		wg.Go(func() {
			m, to, err := replicas[w%len(replicas)].SweepMonitors(ctx, at, n)
			totals[w], errs[w] = [2]int{m, to}, err
		})
	}
	wg.Wait()
	missed, timeouts := 0, 0
	for w := range workers {
		if errs[w] != nil {
			t.Fatal(errs[w])
		}
		missed += totals[w][0]
		timeouts += totals[w][1]
	}
	if missed != monitors || timeouts != monitors {
		t.Errorf("%d missed y %d timeout entre todos; quería %d de cada", missed, timeouts, monitors)
	}
	var rows int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM monitor_checkins WHERE status IN ('missed', 'timeout')`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2*monitors {
		t.Errorf("%d filas missed o timeout; quería %d", rows, 2*monitors)
	}
	// Cada monitor avisa una vez de timeout y otra de missed.
	if got := len(n.kinds()); got != 2*monitors {
		t.Errorf("%d avisos; quería %d", got, 2*monitors)
	}
}

func TestMonitorCRUD(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	name := "Copia de seguridad"
	sched := cron.Schedule{Type: cron.TypeCrontab, Crontab: "0 4 * * *"}
	tz := "Europe/Madrid"
	m, err := s.CreateMonitor(ctx, p.ID, MonitorInput{Name: &name, Schedule: &sched, Timezone: &tz})
	if err != nil {
		t.Fatal(err)
	}
	if m.Slug != "copia-de-seguridad" || m.CheckinMargin != DefaultCheckinMargin || m.MaxRuntime != DefaultMaxRuntime ||
		m.Status != MonitorPending || m.NextCheckinAt != nil {
		t.Errorf("creado = %+v", m)
	}
	if _, err := s.CreateMonitor(ctx, p.ID, MonitorInput{Name: &name, Schedule: &sched}); !errors.Is(err, ErrInvalid) {
		t.Errorf("repetido: %v", err)
	}
	bad := "Mars/Olympus"
	if _, err := s.UpdateMonitor(ctx, p.ID, m.Slug, MonitorInput{Timezone: &bad}); !errors.Is(err, ErrInvalid) {
		t.Errorf("zona mala: %v", err)
	}
	if _, err := s.CreateMonitor(ctx, p.ID, MonitorInput{Name: &name}); !errors.Is(err, ErrInvalid) {
		t.Errorf("sin horario: %v", err)
	}
	margin := 15
	m, err = s.UpdateMonitor(ctx, p.ID, m.Slug, MonitorInput{CheckinMargin: &margin})
	if err != nil || m.CheckinMargin != 15 || m.Timezone != "Europe/Madrid" {
		t.Fatalf("editar = %+v %v", m, err)
	}
	list, err := s.ListMonitors(ctx, MonitorFilter{Project: "api"})
	if err != nil || len(list) != 1 || list[0].Recent == nil {
		t.Fatalf("lista = %+v %v", list, err)
	}
	if _, err := s.ListMonitors(ctx, MonitorFilter{Status: "raro"}); !errors.Is(err, ErrInvalid) {
		t.Errorf("estado raro: %v", err)
	}
	if err := s.DeleteMonitor(ctx, p.ID, m.Slug); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Monitor(ctx, p.ID, m.Slug); !errors.Is(err, ErrNotFound) {
		t.Errorf("borrado: %v", err)
	}
}

func TestCheckInsPagesAndPurge(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, _ := s.CreateProject(ctx, "api", randomHex(t, 16))
	for i := range 5 {
		checkIn(t, s, p.ID, CheckInInput{Slug: "tarea", Status: "ok", Config: every(1, "minute")}, t0.Add(time.Duration(i)*time.Minute), nil)
	}
	m, _ := s.Monitor(ctx, p.ID, "tarea")
	first, err := s.CheckIns(ctx, m.ID, "", 3)
	if err != nil || len(first.CheckIns) != 3 || first.NextCursor == "" {
		t.Fatalf("primera = %+v %v", first, err)
	}
	second, err := s.CheckIns(ctx, m.ID, first.NextCursor, 3)
	if err != nil || len(second.CheckIns) != 2 || second.NextCursor != "" || !second.CheckIns[0].StartedAt.Before(first.CheckIns[2].StartedAt) {
		t.Fatalf("segunda = %+v %v", second, err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE monitor_checkins SET received_at = `+s.pool.Dialect().SubSeconds("received_at", "$1"),
		100*24*3600); err != nil {
		t.Fatal(err)
	}
	n, err := s.PurgeCheckIns(ctx, time.Now().Add(-90*24*time.Hour))
	if err != nil || n != 4 {
		t.Fatalf("purgados = %d %v", n, err)
	}
}
