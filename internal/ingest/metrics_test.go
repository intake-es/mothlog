package ingest

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/metrics"
	"github.com/intake-es/intake-errors/internal/monitor"
	"github.com/intake-es/intake-errors/internal/store"
)

// snapshotCounter cuenta las lecturas de la base para /metrics.
type snapshotCounter struct {
	*store.Store
	calls atomic.Int64
}

func (s *snapshotCounter) MetricsSnapshot(ctx context.Context) (store.MetricsSnapshot, error) {
	s.calls.Add(1)
	return s.Store.MetricsSnapshot(ctx)
}

func (e *testEnv) withMetrics(m *metrics.Metrics) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mux := http.NewServeMux()
	mux.Handle(Pattern, NewHandler(e.s, e.ing, NewRateLimiter(1000), e.drops, log).
		WithCheckIns(monitor.New(e.s, nil, log)).WithMetrics(m))
	e.h = mux
}

// scrape devuelve cada muestra de /metrics por su nombre con etiquetas.
func scrape(t *testing.T, m *metrics.Metrics) map[string]float64 {
	t.Helper()
	var buf bytes.Buffer
	m.Write(context.Background(), &buf)
	out := map[string]float64{}
	for line := range strings.Lines(buf.String()) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		i := strings.LastIndexByte(line, ' ')
		v, err := strconv.ParseFloat(line[i+1:], 64)
		if err != nil {
			t.Fatalf("línea %q: %v", line, err)
		}
		out[line[:i]] = v
	}
	return out
}

func badKeyRequest() *http.Request {
	r := rawEnvelope(`{"type":"event"}`, `{"message":"hola"}`)
	r.Header.Set("X-Sentry-Auth", "Sentry sentry_key=00000000000000000000000000000000, sentry_version=7")
	return r
}

// Unos guardados, otros descartados por un filtro, por el tope, por el límite
// por minuto y por repetidos, otros rechazados y dos check-ins: cada métrica
// tiene que dar lo enviado.
func TestMetricsMatchFixtures(t *testing.T) {
	e := newTestEnv(t, 1000)
	ctx := context.Background()
	src := &snapshotCounter{Store: e.s}
	m := metrics.New(src, nil)
	e.withMetrics(m)
	now := time.Now().UTC()
	e.ing.now = func() time.Time { return now }

	two := 2
	f := store.DefaultFilters()
	f.ExceptionTypes = []string{"KeyError"}
	f.EventsPerIssuePerHour = &two
	if _, err := e.s.SetFilters(ctx, 1, f); err != nil {
		t.Fatal(err)
	}
	f = store.DefaultFilters()
	f.RateLimitPerMinute = &two
	if _, err := e.s.SetFilters(ctx, 2, f); err != nil {
		t.Fatal(err)
	}

	api := loadFixture(t, "api-excepcion-1")
	var first string
	for i := range 4 {
		id := newHexID()
		if i == 0 {
			first = id
		}
		e.sendOK(t, api, api.withEventID(t, id))
	}
	e.sendOK(t, api, api.withEventID(t, first))
	keyErr := loadFixture(t, "api-log-exception-1")
	e.sendOK(t, keyErr, keyErr.body)
	web := loadFixture(t, "web-typeerror-1")
	for range 2 {
		e.sendOK(t, web, web.withEventID(t, newHexID()))
	}
	for _, name := range []string{"api-cron-ok-1", "api-cron-ok-2"} {
		f := loadFixture(t, name)
		e.sendOK(t, f, f.body)
	}

	rejected := []struct {
		r    *http.Request
		code int
	}{
		{web.request(web.withEventID(t, newHexID())), http.StatusTooManyRequests},
		{badKeyRequest(), http.StatusUnauthorized},
		{rawEnvelope(`{"type":"event"}`, `{roto`), http.StatusBadRequest},
		{rawEnvelope(`{"type":"event"}`, `{"message":"`+strings.Repeat("x", MaxCompressed)+`"}`), http.StatusRequestEntityTooLarge},
	}
	for _, rj := range rejected {
		if rec := e.send(rj.r); rec.Code != rj.code {
			t.Fatalf("código %d, quiero %d: %s", rec.Code, rj.code, rec.Body)
		}
	}
	if n := src.calls.Load(); n != 0 {
		t.Fatalf("contar leyó la base %d veces", n)
	}

	issueID := e.count(t, `SELECT id FROM issues WHERE title LIKE 'ZeroDivisionError%'`)
	if _, err := e.s.Pool().Exec(ctx, `
		INSERT INTO alert_outbox (issue_id, channel, kind, event_at, created_at, sent_at, failed_at) VALUES
			($1, 'slack', 'new', now(), now() - interval '1 hour', NULL, NULL),
			($1, 'slack', 'new', now(), now() - interval '2 hours', NULL, now()),
			($1, 'linear', 'new', now(), now(), now(), NULL)`, issueID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool().Exec(ctx, `
		INSERT INTO alert_outbox (digest_project_id, action_id, channel, kind, event_at, sent_at)
		VALUES (1, 'correo', 'email', 'digest', now(), now())`); err != nil {
		t.Fatal(err)
	}
	if _, err := e.s.Pool().Exec(ctx, `
		INSERT INTO uptime_monitors (project_id, slug, name, url, status, paused) VALUES
			(1, 'web', 'Web', 'https://example.com', 'up', false),
			(1, 'api', 'API', 'https://example.com/api', 'down', false),
			(1, 'vieja', 'Vieja', 'https://example.com/v', 'down', true)`); err != nil {
		t.Fatal(err)
	}

	got := scrape(t, m)
	want := map[string]float64{
		`intake_events_received_total{project="1"}`:                        6,
		`intake_events_received_total{project="2"}`:                        3,
		`intake_events_saved_total{project="1"}`:                           2,
		`intake_events_saved_total{project="2"}`:                           2,
		`intake_events_dropped_total{project="1",reason="issue_limit"}`:    2,
		`intake_events_dropped_total{project="1",reason="duplicate"}`:      1,
		`intake_events_dropped_total{project="1",reason="exception_type"}`: 1,
		`intake_events_dropped_total{project="2",reason="rate_limit"}`:     1,
		`intake_envelopes_rejected_total{code="400"}`:                      1,
		`intake_envelopes_rejected_total{code="401"}`:                      1,
		`intake_envelopes_rejected_total{code="413"}`:                      1,
		`intake_envelopes_rejected_total{code="429"}`:                      1,
		`intake_ingest_duration_seconds_count`:                             10,
		`intake_ingest_duration_seconds_bucket{le="+Inf"}`:                 10,
		`intake_checkins_received_total{status="in_progress"}`:             1,
		`intake_checkins_received_total{status="ok"}`:                      1,
		`intake_metrics_db_up`:                                             1,
		`intake_alert_outbox{channel="slack",status="pending"}`:            1,
		`intake_alert_outbox{channel="slack",status="failed"}`:             1,
		`intake_alert_outbox{channel="slack",status="sent"}`:               0,
		`intake_alert_outbox{channel="linear",status="sent"}`:              1,
		`intake_alert_outbox{channel="email",status="sent"}`:               1,
		`intake_alert_outbox_oldest_pending_seconds{channel="linear"}`:     0,
		`intake_digests_sent_total{channel="email"}`:                       1,
		`intake_digests_sent_total{channel="slack"}`:                       0,
		`intake_cron_monitors{status="ok"}`:                                1,
		`intake_cron_monitors{status="error"}`:                             0,
		`intake_uptime_monitors{status="up"}`:                              1,
		`intake_uptime_monitors{status="down"}`:                            1,
		`intake_uptime_monitors{status="paused"}`:                          1,
		`intake_project_info{project="1",name="api"}`:                      1,
	}
	for k, v := range want {
		if g, ok := got[k]; !ok || g != v {
			t.Errorf("%s = %v (está: %v), quiero %v", k, g, ok, v)
		}
	}
	if age := got[`intake_alert_outbox_oldest_pending_seconds{channel="slack"}`]; age < 3500 || age > 3700 {
		t.Errorf("antigüedad del pendiente de slack = %v, quiero una hora", age)
	}
	for k := range got {
		if strings.HasPrefix(k, "intake_events_dropped_total") && want[k] == 0 {
			t.Errorf("descarte de más: %s = %v", k, got[k])
		}
	}
	if src.calls.Load() != 1 {
		t.Errorf("%d lecturas de la base para /metrics, quiero 1", src.calls.Load())
	}
	// En caché: pedir otra vez no vuelve a leer.
	scrape(t, m)
	if src.calls.Load() != 1 {
		t.Errorf("la segunda petición volvió a leer la base")
	}
}

// countingDB cuenta las idas a la base: cada consulta, cada lote y abrir,
// confirmar o deshacer una transacción.
type countingDB struct {
	db.DB
	n *atomic.Int64
}

func (c countingDB) Exec(ctx context.Context, sql string, args ...any) (db.Result, error) {
	c.n.Add(1)
	return c.DB.Exec(ctx, sql, args...)
}

func (c countingDB) Query(ctx context.Context, sql string, args ...any) (db.Rows, error) {
	c.n.Add(1)
	return c.DB.Query(ctx, sql, args...)
}

func (c countingDB) QueryRow(ctx context.Context, sql string, args ...any) db.Row {
	c.n.Add(1)
	return c.DB.QueryRow(ctx, sql, args...)
}

func (c countingDB) SendBatch(ctx context.Context, b *db.Batch) error {
	c.n.Add(1)
	return c.DB.SendBatch(ctx, b)
}

func (c countingDB) Begin(ctx context.Context) (db.Tx, error) {
	c.n.Add(1)
	tx, err := c.DB.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return countingTx{Tx: tx, n: c.n}, nil
}

type countingTx struct {
	db.Tx
	n *atomic.Int64
}

func (c countingTx) Exec(ctx context.Context, sql string, args ...any) (db.Result, error) {
	c.n.Add(1)
	return c.Tx.Exec(ctx, sql, args...)
}

func (c countingTx) Query(ctx context.Context, sql string, args ...any) (db.Rows, error) {
	c.n.Add(1)
	return c.Tx.Query(ctx, sql, args...)
}

func (c countingTx) QueryRow(ctx context.Context, sql string, args ...any) db.Row {
	c.n.Add(1)
	return c.Tx.QueryRow(ctx, sql, args...)
}

func (c countingTx) SendBatch(ctx context.Context, b *db.Batch) error {
	c.n.Add(1)
	return c.Tx.SendBatch(ctx, b)
}

func (c countingTx) Begin(ctx context.Context) (db.Tx, error) {
	c.n.Add(1)
	tx, err := c.Tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return countingTx{Tx: tx, n: c.n}, nil
}

func (c countingTx) Commit(ctx context.Context) error {
	c.n.Add(1)
	return c.Tx.Commit(ctx)
}

func (c countingTx) Rollback(ctx context.Context) error {
	err := c.Tx.Rollback(ctx)
	if !errors.Is(err, db.ErrTxClosed) {
		c.n.Add(1)
	}
	return err
}

// countingCalls cuenta las llamadas que van a la base fuera del pool del ingester.
type countingCalls struct {
	Projects
	checkIns CheckIns
	n        *atomic.Int64
}

func (c countingCalls) ProjectByID(ctx context.Context, id int64) (store.Project, error) {
	c.n.Add(1)
	return c.Projects.ProjectByID(ctx, id)
}

func (c countingCalls) Filters(ctx context.Context, id int64) (store.Filters, error) {
	c.n.Add(1)
	return c.Projects.Filters(ctx, id)
}

func (c countingCalls) CheckIn(ctx context.Context, id int64, in store.CheckInInput) (store.CheckInResult, error) {
	c.n.Add(1)
	return c.checkIns.CheckIn(ctx, id, in)
}

// queries manda los mismos envíos con o sin métricas y devuelve cuántas
// consultas hizo el ingester más cuántas llamadas a la base hizo el resto.
func queries(t *testing.T, withMetrics bool) (int64, *metrics.Metrics) {
	t.Helper()
	e := newTestEnv(t, 1000)
	var n atomic.Int64
	pool := countingDB{DB: e.s.Pool(), n: &n}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var m *metrics.Metrics
	if withMetrics {
		m = metrics.New(e.s, log)
	}
	calls := countingCalls{Projects: e.s, checkIns: monitor.New(e.s, nil, log), n: &n}
	mux := http.NewServeMux()
	mux.Handle(Pattern, NewHandler(calls, NewIngester(pool, nil, &recorder{}, log), NewRateLimiter(1000), e.drops, log).
		WithCheckIns(calls).WithMetrics(m))

	api := loadFixture(t, "api-excepcion-1")
	keyErr := loadFixture(t, "api-log-exception-1")
	cron := loadFixture(t, "api-cron-ok-1")
	ids := []string{"0123456789abcdef0123456789abcdef", "fedcba9876543210fedcba9876543210"}
	for i := range 20 {
		for _, r := range []*http.Request{
			api.request(api.withEventID(t, ids[i%2])), keyErr.request(keyErr.body), cron.request(cron.body), badKeyRequest(),
		} {
			mux.ServeHTTP(httptest.NewRecorder(), r)
		}
	}
	return n.Load(), m
}

func TestCountingDoesNotQueryTheDatabase(t *testing.T) {
	without, _ := queries(t, false)
	with, m := queries(t, true)
	if without == 0 {
		t.Fatal("no se contó ninguna consulta")
	}
	if with != without {
		t.Errorf("con métricas %d consultas, sin ellas %d", with, without)
	}
	got := scrape(t, m)
	if got[`intake_events_received_total{project="1"}`] != 40 || got[`intake_envelopes_rejected_total{code="401"}`] != 20 {
		t.Errorf("no contó lo enviado: %v", got)
	}
	t.Logf("80 envíos: %d consultas con métricas y %d sin ellas", with, without)
}
