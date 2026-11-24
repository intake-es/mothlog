// Package metrics cuenta en memoria lo que pasa en el servicio y lo sirve en
// /metrics con el formato de texto de Prometheus.
package metrics

import (
	"bufio"
	"bytes"
	"context"
	"crypto/subtle"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/store"
)

const (
	// CacheTTL es cuánto vale lo leído de la base antes de volver a leerlo.
	CacheTTL = 10 * time.Second
	// QueryTimeout es lo más que se espera a la base al pedir /metrics.
	QueryTimeout = 2 * time.Second
)

// DropDuplicate es el motivo de un evento con un event_id ya guardado.
const DropDuplicate = "duplicate"

// IngestBuckets son los límites, en segundos, del histograma de ingesta.
var IngestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// Source es de donde salen las métricas que viven en la base.
type Source interface {
	MetricsSnapshot(ctx context.Context) (store.MetricsSnapshot, error)
	Pool() db.DB
}

// Metrics es nil-safe: con un *Metrics nil, contar no hace nada.
type Metrics struct {
	src Source
	log *slog.Logger
	now func() time.Time

	received, saved, dropped *vec
	rejected, checkIns       *vec
	requests                 *vec
	ingest                   *histogram

	snapMu   sync.Mutex
	snap     store.MetricsSnapshot
	snapOK   bool
	snapAt   time.Time
	snapTook time.Duration
}

func New(src Source, log *slog.Logger) *Metrics {
	return &Metrics{
		src: src, log: log, now: time.Now,
		received: newVec("mothlog_events_received_total", "Events received in envelopes with a valid key.", "project"),
		saved:    newVec("mothlog_events_saved_total", "Events saved.", "project"),
		dropped:  newVec("mothlog_events_dropped_total", "Events dropped, by reason.", "project", "reason"),
		rejected: newVec("mothlog_envelopes_rejected_total", "Envelopes rejected by ingestion, by HTTP status.", "code"),
		checkIns: newVec("mothlog_checkins_received_total", "Cron check-ins received, by status.", "status"),
		requests: newVec("mothlog_http_requests_total", "Requests to the API and the MCP, by route and status.", "method", "route", "code"),
		ingest:   newHistogram("mothlog_ingest_duration_seconds", "Ingestion time of each accepted envelope.", IngestBuckets),
	}
}

func projectLabel(id int64) string { return strconv.FormatInt(id, 10) }

func (m *Metrics) Received(project int64, n int) {
	if m != nil && n > 0 {
		m.received.add(float64(n), projectLabel(project))
	}
}

func (m *Metrics) Saved(project int64) {
	if m != nil {
		m.saved.add(1, projectLabel(project))
	}
}

func (m *Metrics) Dropped(project int64, reason string, n int) {
	if m != nil && n > 0 {
		m.dropped.add(float64(n), projectLabel(project), reason)
	}
}

// Envelope apunta cómo acabó un envío: rechazado o cuánto tardó en aceptarse.
func (m *Metrics) Envelope(code int, took time.Duration) {
	if m == nil {
		return
	}
	if code >= 400 {
		m.rejected.add(1, strconv.Itoa(code))
		return
	}
	m.ingest.observe(took.Seconds())
}

func (m *Metrics) CheckIn(status string) {
	if m == nil {
		return
	}
	switch status {
	case "in_progress", "ok", "error", "ignored":
	default:
		status = "other"
	}
	m.checkIns.add(1, status)
}

// Handler sirve /metrics a quien mande token en Authorization: Bearer.
func (m *Metrics) Handler(token string) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			w.Header().Set("WWW-Authenticate", `Bearer realm="metrics"`)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		var buf bytes.Buffer
		m.Write(r.Context(), &buf)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = w.Write(buf.Bytes())
	})
}

// Write escribe todas las métricas en el formato de texto de Prometheus.
func (m *Metrics) Write(ctx context.Context, out *bytes.Buffer) {
	w := bufio.NewWriter(out)
	defer func() { _ = w.Flush() }()
	for _, v := range []*vec{m.received, m.saved, m.dropped, m.rejected} {
		v.write(w)
	}
	m.ingest.write(w)
	m.checkIns.write(w)
	m.requests.write(w)
	m.writeSnapshot(ctx, w)
	m.writeRuntime(w)
}

func (m *Metrics) snapshot(ctx context.Context) (store.MetricsSnapshot, bool, time.Duration) {
	m.snapMu.Lock()
	defer m.snapMu.Unlock()
	if !m.snapAt.IsZero() && m.now().Sub(m.snapAt) < CacheTTL {
		return m.snap, m.snapOK, m.snapTook
	}
	ctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	start := m.now()
	snap, err := m.src.MetricsSnapshot(ctx)
	m.snapTook = m.now().Sub(start)
	m.snapAt = m.now()
	m.snap, m.snapOK = snap, err == nil
	if err != nil && m.log != nil {
		m.log.WarnContext(ctx, "metrics: could not read the database", "err", err)
	}
	return m.snap, m.snapOK, m.snapTook
}

var (
	cronStatuses   = []string{"pending", "ok", "error", "missed", "timeout"}
	uptimeStatuses = []string{"pending", "up", "down", "paused"}
)

func (m *Metrics) writeSnapshot(ctx context.Context, w *bufio.Writer) {
	snap, ok, took := m.snapshot(ctx)
	family(w, "mothlog_metrics_db_up", "1 if the last database read for /metrics succeeded.", "gauge")
	sample(w, "mothlog_metrics_db_up", nil, boolFloat(ok))
	family(w, "mothlog_metrics_db_duration_seconds", "How long the last database read for /metrics took.", "gauge")
	sample(w, "mothlog_metrics_db_duration_seconds", nil, took.Seconds())
	if ok {
		family(w, "mothlog_alert_outbox", "Alerts in the outbox, by channel and status.", "gauge")
		for _, o := range snap.Outbox {
			sample(w, "mothlog_alert_outbox", []string{"channel", o.Channel, "status", "pending"}, float64(o.Pending))
			sample(w, "mothlog_alert_outbox", []string{"channel", o.Channel, "status", "sent"}, float64(o.Sent))
			sample(w, "mothlog_alert_outbox", []string{"channel", o.Channel, "status", "failed"}, float64(o.Failed))
		}
		family(w, "mothlog_alert_outbox_oldest_pending_seconds", "Age of the oldest pending alert of each channel; 0 if there is none.", "gauge")
		for _, o := range snap.Outbox {
			sample(w, "mothlog_alert_outbox_oldest_pending_seconds", []string{"channel", o.Channel}, o.OldestPendingSeconds)
		}
		family(w, "mothlog_digests_sent_total", "Digests sent, by channel.", "counter")
		for _, o := range snap.Outbox {
			sample(w, "mothlog_digests_sent_total", []string{"channel", o.Channel}, float64(o.DigestsSent))
		}
		statuses(w, "mothlog_cron_monitors", "Cron monitors, by status.", cronStatuses, snap.CronMonitors)
		statuses(w, "mothlog_uptime_monitors", "Uptime monitors, by status.", uptimeStatuses, snap.UptimeMonitors)
		family(w, "mothlog_project_info", "Name of each project, to join with the project label.", "gauge")
		ids := make([]int64, 0, len(snap.Projects))
		for id := range snap.Projects {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			sample(w, "mothlog_project_info", []string{"project", projectLabel(id), "name", snap.Projects[id]}, 1)
		}
	}

	st := m.src.Pool().Stats()
	family(w, "mothlog_db_pool_connections", "Database pool connections, by state.", "gauge")
	sample(w, "mothlog_db_pool_connections", []string{"state", "in_use"}, float64(st.InUse))
	sample(w, "mothlog_db_pool_connections", []string{"state", "idle"}, float64(st.Idle))
	sample(w, "mothlog_db_pool_connections", []string{"state", "constructing"}, float64(st.Constructing))
	family(w, "mothlog_db_pool_max_connections", "Pool connection limit.", "gauge")
	sample(w, "mothlog_db_pool_max_connections", nil, float64(st.Max))
	family(w, "mothlog_db_pool_waits_total", "Times a connection request had to wait because none was free.", "counter")
	sample(w, "mothlog_db_pool_waits_total", nil, float64(st.Waits))
	family(w, "mothlog_db_pool_wait_seconds_total", "Total time waiting for a free connection.", "counter")
	sample(w, "mothlog_db_pool_wait_seconds_total", nil, st.WaitTime.Seconds())
}

func statuses(w *bufio.Writer, name, help string, known []string, got map[string]int64) {
	family(w, name, help, "gauge")
	all := slices.Clone(known)
	for s := range got {
		if !slices.Contains(all, s) {
			all = append(all, s)
		}
	}
	for _, s := range all {
		sample(w, name, []string{"status", s}, float64(got[s]))
	}
}

func (m *Metrics) writeRuntime(w *bufio.Writer) {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	family(w, "go_goroutines", "Running goroutines.", "gauge")
	sample(w, "go_goroutines", nil, float64(runtime.NumGoroutine()))
	family(w, "go_memstats_heap_alloc_bytes", "Heap bytes in use by live objects.", "gauge")
	sample(w, "go_memstats_heap_alloc_bytes", nil, float64(ms.HeapAlloc))
	family(w, "go_memstats_sys_bytes", "Bytes obtained from the system by the Go runtime.", "gauge")
	sample(w, "go_memstats_sys_bytes", nil, float64(ms.Sys))
	family(w, "go_gc_cycles_total", "Garbage collector cycles.", "counter")
	sample(w, "go_gc_cycles_total", nil, float64(ms.NumGC))
	if rss, ok := residentBytes(); ok {
		family(w, "process_resident_memory_bytes", "Resident memory of the process.", "gauge")
		sample(w, "process_resident_memory_bytes", nil, rss)
	}
}

// residentBytes lee la memoria residente de /proc; fuera de Linux no hay.
func residentBytes() (float64, bool) {
	b, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0, false
	}
	f := strings.Fields(string(b))
	if len(f) < 2 {
		return 0, false
	}
	pages, err := strconv.ParseFloat(f[1], 64)
	if err != nil {
		return 0, false
	}
	return pages * float64(os.Getpagesize()), true
}

func boolFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

type vec struct {
	name, help string
	labels     []string

	mu   sync.Mutex
	vals map[string]*point
}

type point struct {
	labels []string
	n      float64
}

func newVec(name, help string, labels ...string) *vec {
	return &vec{name: name, help: help, labels: labels, vals: map[string]*point{}}
}

func (v *vec) add(n float64, values ...string) {
	key := strings.Join(values, "\xff")
	v.mu.Lock()
	p := v.vals[key]
	if p == nil {
		p = &point{labels: make([]string, 0, 2*len(values))}
		for i, val := range values {
			p.labels = append(p.labels, v.labels[i], val)
		}
		v.vals[key] = p
	}
	p.n += n
	v.mu.Unlock()
}

func (v *vec) write(w *bufio.Writer) {
	v.mu.Lock()
	keys := make([]string, 0, len(v.vals))
	for k := range v.vals {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	pts := make([]point, len(keys))
	for i, k := range keys {
		pts[i] = *v.vals[k]
	}
	v.mu.Unlock()
	family(w, v.name, v.help, "counter")
	for _, p := range pts {
		sample(w, v.name, p.labels, p.n)
	}
}

type histogram struct {
	name, help string
	bounds     []float64

	mu     sync.Mutex
	counts []uint64
	sum    float64
	total  uint64
}

func newHistogram(name, help string, bounds []float64) *histogram {
	return &histogram{name: name, help: help, bounds: bounds, counts: make([]uint64, len(bounds))}
}

func (h *histogram) observe(v float64) {
	i, _ := slices.BinarySearch(h.bounds, v)
	h.mu.Lock()
	if i < len(h.counts) {
		h.counts[i]++
	}
	h.sum += v
	h.total++
	h.mu.Unlock()
}

func (h *histogram) write(w *bufio.Writer) {
	h.mu.Lock()
	counts := slices.Clone(h.counts)
	sum, total := h.sum, h.total
	h.mu.Unlock()
	family(w, h.name, h.help, "histogram")
	var acc uint64
	for i, b := range h.bounds {
		acc += counts[i]
		sample(w, h.name+"_bucket", []string{"le", formatFloat(b)}, float64(acc))
	}
	sample(w, h.name+"_bucket", []string{"le", "+Inf"}, float64(total))
	sample(w, h.name+"_sum", nil, sum)
	sample(w, h.name+"_count", nil, float64(total))
}

func family(w *bufio.Writer, name, help, typ string) {
	_, _ = fmt.Fprintf(w, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, typ)
}

// sample escribe una línea; labels va por pares de nombre y valor.
func sample(w *bufio.Writer, name string, labels []string, v float64) {
	_, _ = w.WriteString(name)
	if len(labels) > 0 {
		_ = w.WriteByte('{')
		for i := 0; i < len(labels); i += 2 {
			if i > 0 {
				_ = w.WriteByte(',')
			}
			_, _ = w.WriteString(labels[i])
			_, _ = w.WriteString(`="`)
			_, _ = w.WriteString(labelEscaper.Replace(labels[i+1]))
			_ = w.WriteByte('"')
		}
		_ = w.WriteByte('}')
	}
	_ = w.WriteByte(' ')
	_, _ = w.WriteString(formatFloat(v))
	_ = w.WriteByte('\n')
}

var labelEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`)

func formatFloat(v float64) string {
	switch {
	case math.IsInf(v, 1):
		return "+Inf"
	case math.IsInf(v, -1):
		return "-Inf"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
