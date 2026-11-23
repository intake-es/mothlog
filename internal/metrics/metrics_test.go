package metrics

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/store"
)

type fakeSource struct {
	pool  db.DB
	snap  store.MetricsSnapshot
	err   error
	calls int
}

func (f *fakeSource) MetricsSnapshot(ctx context.Context) (store.MetricsSnapshot, error) {
	f.calls++
	return f.snap, f.err
}

func (f *fakeSource) Pool() db.DB { return f.pool }

func newFake(t *testing.T) *fakeSource {
	t.Helper()
	// OpenPostgres no conecta hasta que se le pide una conexión.
	pool, err := db.OpenPostgres(context.Background(), "postgres://x:x@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	return &fakeSource{pool: pool, snap: store.MetricsSnapshot{
		Outbox:         []store.OutboxStats{{Channel: "slack", Pending: 2, Sent: 5, Failed: 1, DigestsSent: 3, OldestPendingSeconds: 42.5}},
		CronMonitors:   map[string]int64{"ok": 2, "missed": 1},
		UptimeMonitors: map[string]int64{"down": 1},
		Projects:       map[int64]string{1: `api "uno"`, 2: "web\\dos"},
	}}
}

func output(t *testing.T, m *Metrics) string {
	t.Helper()
	var buf bytes.Buffer
	m.Write(context.Background(), &buf)
	return buf.String()
}

func filled(t *testing.T) *Metrics {
	t.Helper()
	m := New(newFake(t), nil)
	m.Received(1, 3)
	m.Saved(1)
	m.Dropped(1, store.DropExceptionType, 2)
	m.Envelope(http.StatusOK, 30*time.Millisecond)
	m.Envelope(http.StatusOK, 3*time.Second)
	m.Envelope(http.StatusUnauthorized, time.Millisecond)
	m.CheckIn("ok")
	m.CheckIn("raro")
	return m
}

var (
	metricName = regexp.MustCompile(`^[a-zA-Z_:][a-zA-Z0-9_:]*$`)
	sampleLine = regexp.MustCompile(`^([a-zA-Z_:][a-zA-Z0-9_:]*)(\{([a-zA-Z_][a-zA-Z0-9_]*="(\\.|[^"\\])*",?)*\})? (\S+)$`)
)

// checkFormat comprueba lo que exige el formato de texto: HELP y TYPE una vez
// por familia y antes de sus muestras, nombres válidos, valores numéricos y
// los histogramas con buckets acumulados y +Inf igual a _count.
func checkFormat(t *testing.T, text string) {
	t.Helper()
	types := map[string]string{}
	seen := map[string]bool{}
	var current string
	buckets := map[string][]float64{}
	counts := map[string]float64{}
	for line := range strings.Lines(text) {
		line = strings.TrimSuffix(line, "\n")
		if rest, ok := strings.CutPrefix(line, "# HELP "); ok {
			name, _, _ := strings.Cut(rest, " ")
			if !metricName.MatchString(name) || seen[name] {
				t.Errorf("HELP repetido o con nombre no válido: %q", line)
			}
			seen[name], current = true, name
			continue
		}
		if rest, ok := strings.CutPrefix(line, "# TYPE "); ok {
			name, typ, _ := strings.Cut(rest, " ")
			if name != current || types[name] != "" {
				t.Errorf("TYPE fuera de su sitio: %q", line)
			}
			switch typ {
			case "counter", "gauge", "histogram":
			default:
				t.Errorf("tipo %q", typ)
			}
			types[name] = typ
			continue
		}
		mm := sampleLine.FindStringSubmatch(line)
		if mm == nil {
			t.Errorf("línea no válida: %q", line)
			continue
		}
		name := mm[1]
		v, err := strconv.ParseFloat(mm[5], 64)
		if err != nil && mm[5] != "+Inf" {
			t.Errorf("valor no válido: %q", line)
		}
		base := name
		if types[current] == "histogram" {
			base = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, "_bucket"), "_sum"), "_count")
		}
		if base != current {
			t.Errorf("muestra %s fuera de su familia %s", name, current)
		}
		if types[current] == "counter" && !strings.HasSuffix(name, "_total") {
			t.Errorf("contador sin _total: %s", name)
		}
		switch {
		case strings.HasSuffix(name, "_bucket"):
			if b := buckets[base]; len(b) > 0 && v < b[len(b)-1] {
				t.Errorf("buckets no acumulados: %q", line)
			}
			buckets[base] = append(buckets[base], v)
			if strings.Contains(line, `le="+Inf"`) {
				counts[base+"+Inf"] = v
			}
		case strings.HasSuffix(name, "_count") && types[current] == "histogram":
			if counts[base+"+Inf"] != v {
				t.Errorf("%s_count %v distinto del bucket +Inf %v", base, v, counts[base+"+Inf"])
			}
		}
	}
}

func TestFormat(t *testing.T) {
	text := output(t, filled(t))
	checkFormat(t, text)
	for _, want := range []string{
		`intake_events_received_total{project="1"} 3`,
		`intake_events_dropped_total{project="1",reason="exception_type"} 2`,
		`intake_envelopes_rejected_total{code="401"} 1`,
		`intake_ingest_duration_seconds_bucket{le="0.025"} 0`,
		`intake_ingest_duration_seconds_bucket{le="0.05"} 1`,
		`intake_ingest_duration_seconds_bucket{le="2.5"} 1`,
		`intake_ingest_duration_seconds_bucket{le="5"} 2`,
		`intake_ingest_duration_seconds_bucket{le="+Inf"} 2`,
		`intake_ingest_duration_seconds_count 2`,
		`intake_checkins_received_total{status="other"} 1`,
		`intake_alert_outbox{channel="slack",status="pending"} 2`,
		`intake_alert_outbox_oldest_pending_seconds{channel="slack"} 42.5`,
		`intake_digests_sent_total{channel="slack"} 3`,
		`intake_cron_monitors{status="missed"} 1`,
		`intake_cron_monitors{status="timeout"} 0`,
		`intake_uptime_monitors{status="down"} 1`,
		`intake_project_info{project="1",name="api \"uno\""} 1`,
		`intake_project_info{project="2",name="web\\dos"} 1`,
		`intake_db_pool_connections{state="in_use"} 0`,
		`intake_metrics_db_up 1`,
		"go_goroutines ",
		"go_memstats_heap_alloc_bytes ",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("falta %q", want)
		}
	}
}

// promtoolModule es el promtool que se compila con go run si no hay uno instalado.
const promtoolModule = "github.com/prometheus/prometheus/cmd/promtool@v0.316.0"

// promtool devuelve cómo lanzar promtool: el de PROMTOOL, el del PATH o, si no
// hay, go run del módulo oficial (la primera vez lo descarga y compila).
func promtool(t *testing.T) []string {
	t.Helper()
	if bin := os.Getenv("PROMTOOL"); bin != "" {
		return []string{bin}
	}
	if bin, err := exec.LookPath("promtool"); err == nil {
		return []string{bin}
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatal("sin promtool ni go para compilarlo")
	}
	return []string{goBin, "run", promtoolModule}
}

func checkMetrics(t *testing.T, cmdline []string, text string) ([]byte, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "metrics.txt")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	cmd := exec.Command(cmdline[0], append(cmdline[1:], "check", "metrics")...)
	// Fuera del módulo, para que go run no toque go.mod.
	cmd.Dir = t.TempDir()
	cmd.Stdin = f
	return cmd.CombinedOutput()
}

// La salida tiene que pasar el validador oficial, y el validador tiene que
// rechazar una salida rota, para que el test no pase por no comprobar nada.
func TestPromtool(t *testing.T) {
	cmdline := promtool(t)
	if out, err := checkMetrics(t, cmdline, output(t, filled(t))); err != nil {
		t.Fatalf("promtool: %v\n%s", err, out)
	}
	broken := "# TYPE intake_x counter\nintake_x{a=\"1\" 1\n"
	if out, err := checkMetrics(t, cmdline, broken); err == nil {
		t.Fatalf("promtool acepta una salida rota:\n%s", out)
	}
}

func TestSnapshotCacheAndErrors(t *testing.T) {
	src := newFake(t)
	m := New(src, nil)
	now := time.Now()
	m.now = func() time.Time { return now }
	output(t, m)
	output(t, m)
	if src.calls != 1 {
		t.Fatalf("%d lecturas en caché, quiero 1", src.calls)
	}
	now = now.Add(CacheTTL)
	src.err = errors.New("caída")
	text := output(t, m)
	if src.calls != 2 {
		t.Fatalf("%d lecturas pasada la caché, quiero 2", src.calls)
	}
	checkFormat(t, text)
	if !strings.Contains(text, "intake_metrics_db_up 0\n") || strings.Contains(text, "intake_alert_outbox{") {
		t.Errorf("sin base, quiero db_up 0 y sin avisos:\n%s", text)
	}
	if !strings.Contains(text, "intake_db_pool_connections") || !strings.Contains(text, "go_goroutines") {
		t.Error("sin base faltan las métricas que no dependen de ella")
	}
}

func TestHandlerToken(t *testing.T) {
	h := New(newFake(t), nil).Handler("secreto")
	for _, tc := range []struct {
		auth string
		want int
	}{
		{"", http.StatusUnauthorized},
		{"Bearer otro", http.StatusUnauthorized},
		{"secreto", http.StatusUnauthorized},
		{"Bearer secreto", http.StatusOK},
	} {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if tc.auth != "" {
			r.Header.Set("Authorization", tc.auth)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != tc.want {
			t.Errorf("Authorization %q: %d, quiero %d", tc.auth, rec.Code, tc.want)
		}
		if tc.want == http.StatusOK && !strings.HasPrefix(rec.Header().Get("Content-Type"), "text/plain; version=0.0.4") {
			t.Errorf("Content-Type %q", rec.Header().Get("Content-Type"))
		}
		if tc.want != http.StatusOK && strings.Contains(rec.Body.String(), "intake_") {
			t.Error("sin token deja ver las métricas")
		}
	}
}

func TestRoutesWithoutIDs(t *testing.T) {
	m := New(newFake(t), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/0/issues/{id}/{$}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") == "404" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("{}"))
	})
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Error("el MCP perdió http.Flusher")
		}
		w.WriteHeader(http.StatusAccepted)
	})
	mux.HandleFunc("GET /healthz", func(http.ResponseWriter, *http.Request) {})
	h := m.Routes(mux)
	for _, p := range []string{"/api/0/issues/1/", "/api/0/issues/2/", "/api/0/issues/404/", "/api/0/nada/7", "/healthz"} {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, p, nil))
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp", nil))
	text := output(t, m)
	for _, want := range []string{
		`intake_http_requests_total{method="GET",route="/api/0/issues/{id}/{$}",code="200"} 2`,
		`intake_http_requests_total{method="GET",route="/api/0/issues/{id}/{$}",code="404"} 1`,
		`intake_http_requests_total{method="GET",route="unmatched",code="404"} 1`,
		`intake_http_requests_total{method="POST",route="/mcp",code="202"} 1`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("falta %q", want)
		}
	}
	if strings.Contains(text, "healthz") || strings.Contains(text, "/7") {
		t.Errorf("cuenta rutas de más o con ids:\n%s", text)
	}
	var nilM *Metrics
	if nilM.Routes(mux) != http.Handler(mux) {
		t.Error("sin métricas debería devolver el mux tal cual")
	}
	nilM.Received(1, 1)
	nilM.Envelope(200, time.Second)
}

// Un método inventado no abre series nuevas: va como other, case o no la ruta.
func TestRoutesMethodIsBounded(t *testing.T) {
	m := New(newFake(t), nil)
	mux := http.NewServeMux()
	mux.HandleFunc("/mcp", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusMethodNotAllowed) })
	mux.HandleFunc("GET /api/0/issues/{$}", func(http.ResponseWriter, *http.Request) {})
	h := m.Routes(mux)
	for i := range 50 {
		method := "INVENTADO" + strconv.Itoa(i)
		for _, p := range []string{"/mcp", "/api/0/xyz", "/api/0/issues/"} {
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(method, p, nil))
		}
	}
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/mcp", nil))
	text := output(t, m)
	if strings.Contains(text, "INVENTADO") {
		t.Fatalf("la etiqueta method copia métodos inventados:\n%s", text)
	}
	for _, want := range []string{
		`intake_http_requests_total{method="other",route="/mcp",code="405"} 50`,
		`intake_http_requests_total{method="other",route="unmatched",code="404"} 50`,
		`intake_http_requests_total{method="other",route="unmatched",code="405"} 50`,
		`intake_http_requests_total{method="DELETE",route="/mcp",code="405"} 1`,
	} {
		if !strings.Contains(text, want+"\n") {
			t.Errorf("falta %q en:\n%s", want, text)
		}
	}
	if n := strings.Count(text, "intake_http_requests_total{"); n != 4 {
		t.Errorf("%d series de peticiones, quiero 4", n)
	}
}
