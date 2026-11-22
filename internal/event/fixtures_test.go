package event_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
)

const fixturesDir = "../../testdata/envelopes"

const (
	keyAPI = "5b6d3a35fd6eb6e0f9d410fc5c0f6cbc"
	keyWeb = "ce01335f125960aa6957a133d15b5f3a"
	// El de la prueba con sentry-cli de tools/sentry-cli.
	keyWebCLI = "7438a4393be53fabe513ce19bec380e9"
)

type want struct {
	key        string
	noEvent    bool
	sdk        string
	sdkVersion string
	excType    string
	message    string
	logger     string
	inApp      bool
	release    string
	request    string
	// debugIDs es cuántas imágenes sourcemap trae debug_meta.
	debugIDs int
}

var (
	python  = want{key: keyAPI, sdk: "sentry.python.fastapi", sdkVersion: "2.75.0", release: "fixture-release-1"}
	browser = want{key: keyWeb, sdk: "sentry.javascript.browser", sdkVersion: "11.15.0", release: "fixture-release-1"}
)

func with(base want, f func(*want)) want {
	f(&base)
	return base
}

// Una fila por fichero de testdata/envelopes, según la tabla de tools/fixtures/README.md.
var fixtures = map[string]want{
	"api-excepcion-1": with(python, func(w *want) {
		w.excType, w.inApp, w.request = "ZeroDivisionError", true, "GET http://testserver/pedidos/p_1042/media"
	}),
	"api-excepcion-2":  {key: keyAPI, noEvent: true},
	"api-descartado-1": {key: keyAPI, noEvent: true},
	"api-descartado-2": {key: keyAPI, noEvent: true},
	"api-validacion-1": with(python, func(w *want) {
		w.excType, w.inApp, w.request = "ValidationError", true, "GET http://testserver/pedidos/importar"
	}),
	"api-validacion-2": {key: keyAPI, noEvent: true},
	"api-log-exception-1": with(python, func(w *want) {
		w.excType, w.inApp, w.logger, w.message = "KeyError", true, "tienda.stock", "stock.sincronizacion_fallida"
	}),
	"api-log-error-1": with(python, func(w *want) {
		w.logger, w.message = "tienda.stock", "pagos.reembolso_rechazado"
	}),
	"api-tarea-a-1": with(python, func(w *want) {
		w.logger = "worker"
		w.message = `task failed id=88dfc065281046dfa0098d0fa91bbdf4 name='enviar_factura' worker=2fb81653 try=4 duration_ms=146906 error=TimeoutError('timed out')`
	}),
	"api-tarea-b-1": with(python, func(w *want) {
		w.logger = "worker"
		w.message = `task failed id=fceb1a3e2ad94b5f8244e34b6d0f4d35 name='enviar_factura' worker=8e00938f try=4 duration_ms=157682 error=TimeoutError('timed out')`
	}),
	"web-typeerror-1": with(browser, func(w *want) {
		w.excType, w.inApp, w.request = "TypeError", true, " https://app.example.com/"
	}),
	"web-cli-upload-1": with(browser, func(w *want) {
		w.key, w.release, w.excType, w.inApp, w.request = keyWebCLI, "prueba-upload", "TypeError", true, " https://app.example.com/"
	}),
	"web-cli-inject-1": with(browser, func(w *want) {
		w.key, w.release, w.excType, w.inApp, w.request = keyWebCLI, "prueba-inject", "TypeError", true, " https://app.example.com/"
		w.debugIDs = 2
	}),
	"web-sesion-1": {key: keyWeb, noEvent: true},
	"web-sesion-2": {key: keyWeb, noEvent: true},
	// Check-ins de crons: los lee internal/monitor.
	"api-cron-ok-1":            {key: keyAPI, noEvent: true},
	"api-cron-ok-2":            {key: keyAPI, noEvent: true},
	"api-cron-error-1":         {key: keyAPI, noEvent: true},
	"api-cron-error-2":         {key: keyAPI, noEvent: true},
	"cli-monitors-run-1":       {key: keyAPI, noEvent: true},
	"cli-monitors-run-2":       {key: keyAPI, noEvent: true},
	"cli-monitors-run-error-1": {key: keyAPI, noEvent: true},
	"cli-monitors-run-error-2": {key: keyAPI, noEvent: true},
}

type meta struct {
	Query   string            `json:"query"`
	Headers map[string]string `json:"headers"`
}

func readFixture(t *testing.T, name string) (*envelope.Envelope, string) {
	t.Helper()
	base := filepath.Join(fixturesDir, name)
	var m meta
	b, err := os.ReadFile(base + ".meta.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	h := http.Header{}
	for k, v := range m.Headers {
		h.Set(k, v)
	}
	q, err := url.ParseQuery(m.Query)
	if err != nil {
		t.Fatal(err)
	}
	key, err := envelope.PublicKey(h, q)
	if err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(base + ".envelope")
	if err != nil {
		t.Fatal(err)
	}
	data, err := envelope.Decompress(bytes.NewReader(body), h.Get("Content-Encoding"), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return env, key
}

func TestFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join(fixturesDir, "*.envelope"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != len(fixtures) {
		t.Fatalf("hay %d envelopes en %s y %d filas en la tabla", len(files), fixturesDir, len(fixtures))
	}

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".envelope")
		w, ok := fixtures[name]
		if !ok {
			t.Errorf("%s no está en la tabla", name)
			continue
		}
		t.Run(name, func(t *testing.T) {
			env, key := readFixture(t, name)
			if key != w.key {
				t.Errorf("clave = %q, quiero %q", key, w.key)
			}
			if len(env.Items) == 0 {
				t.Fatal("sin items")
			}
			items := env.Events()
			if w.noEvent {
				if len(items) != 0 {
					t.Fatalf("%d eventos, quiero ninguno", len(items))
				}
				for _, it := range env.Items {
					switch it.Header.Type {
					case envelope.TypeSession, envelope.TypeSessions, envelope.TypeClientReport, envelope.TypeCheckIn:
					default:
						t.Errorf("item inesperado %q", it.Header.Type)
					}
				}
				return
			}
			if len(items) != 1 {
				t.Fatalf("%d eventos, quiero 1", len(items))
			}

			e, err := event.Parse(items[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			if e.ID == "" || e.ID != env.Header.EventID {
				t.Errorf("id = %q, cabecera %q", e.ID, env.Header.EventID)
			}
			if e.Timestamp.IsZero() || e.Timestamp.Year() != 2026 {
				t.Errorf("fecha = %v", e.Timestamp)
			}
			if e.Level != "error" {
				t.Errorf("nivel = %q", e.Level)
			}
			if e.SDK.Name != w.sdk || e.SDK.Version != w.sdkVersion {
				t.Errorf("sdk = %s/%s, quiero %s/%s", e.SDK.Name, e.SDK.Version, w.sdk, w.sdkVersion)
			}
			if e.Logger != w.logger {
				t.Errorf("logger = %q, quiero %q", e.Logger, w.logger)
			}
			if e.Release != w.release {
				t.Errorf("release = %q, quiero %q", e.Release, w.release)
			}
			if e.Message != w.message {
				t.Errorf("mensaje = %q, quiero %q", e.Message, w.message)
			}
			if len(e.Contexts) == 0 || len(e.Breadcrumbs) == 0 {
				t.Error("faltan contexts o breadcrumbs")
			}
			var dm struct {
				Images []struct {
					Type     string `json:"type"`
					CodeFile string `json:"code_file"`
					DebugID  string `json:"debug_id"`
				} `json:"images"`
			}
			if len(e.DebugMeta) > 0 {
				if err := json.Unmarshal(e.DebugMeta, &dm); err != nil {
					t.Fatal(err)
				}
			}
			if len(dm.Images) != w.debugIDs {
				t.Errorf("%d imágenes en debug_meta, quiero %d", len(dm.Images), w.debugIDs)
			}
			for _, img := range dm.Images {
				if img.Type != "sourcemap" || !strings.HasPrefix(img.CodeFile, "https://app.example.com/") || len(img.DebugID) != 36 {
					t.Errorf("imagen = %+v", img)
				}
			}

			var req string
			if e.Request != nil {
				req = e.Request.Method + " " + e.Request.URL
			}
			if req != w.request {
				t.Errorf("request = %q, quiero %q", req, w.request)
			}

			if w.excType == "" {
				if len(e.Exceptions) != 0 {
					t.Errorf("%d excepciones, quiero ninguna", len(e.Exceptions))
				}
				return
			}
			if len(e.Exceptions) == 0 {
				t.Fatal("sin excepciones")
			}
			last := e.Exceptions[len(e.Exceptions)-1]
			if last.Type != w.excType {
				t.Errorf("excepción = %q, quiero %q", last.Type, w.excType)
			}
			if last.Value == "" || last.Mechanism == nil || last.Mechanism.Type == "" {
				t.Errorf("faltan valor o mecanismo: %+v", last)
			}
			if len(last.Frames) == 0 {
				t.Fatal("sin fotogramas")
			}
			inApp := slices.ContainsFunc(last.Frames, func(f event.Frame) bool { return f.InApp != nil && *f.InApp })
			if inApp != w.inApp {
				t.Errorf("hay fotogramas in_app = %v, quiero %v", inApp, w.inApp)
			}
			for _, f := range last.Frames {
				// Los fotogramas nativos del navegador (Array.map) no tienen línea.
				if f.Filename == "" || f.Function == "" || (f.Line == 0 && f.Filename != "<anonymous>") {
					t.Errorf("fotograma incompleto: %+v", f)
				}
			}
		})
	}
}

func TestFixturesPythonFrameContext(t *testing.T) {
	env, _ := readFixture(t, "api-excepcion-1")
	e, err := event.Parse(env.Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	frames := e.Exceptions[0].Frames
	var app *event.Frame
	for i := range frames {
		if frames[i].InApp != nil && *frames[i].InApp {
			app = &frames[i]
		}
	}
	if app == nil {
		t.Fatal("sin fotograma in_app")
	}
	if app.AbsPath == "" || app.Module == "" || app.ContextLine == "" || len(app.PreContext) == 0 || len(app.PostContext) == 0 {
		t.Errorf("fotograma sin contexto: %+v", app)
	}
	if e.ServerName == "" || e.Transaction != "/pedidos/{pedido_id}/media" || e.Tags["component"] != "api" {
		t.Errorf("server_name %q, transaction %q, tags %v", e.ServerName, e.Transaction, e.Tags)
	}
}

func TestFixturesFrontColumns(t *testing.T) {
	env, _ := readFixture(t, "web-typeerror-1")
	e, err := event.Parse(env.Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if e.Platform != "javascript" || e.Environment != "production" {
		t.Errorf("plataforma %q, entorno %q", e.Platform, e.Environment)
	}
	for _, f := range e.Exceptions[0].Frames {
		if f.Column == 0 && f.Filename != "<anonymous>" {
			t.Errorf("fotograma sin columna: %+v", f)
		}
	}
}
