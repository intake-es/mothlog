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
	keyBackend      = "1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a1a"
	keyIntegrations = "2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b2b"
	keyFront        = "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f"
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
}

var (
	python257 = want{key: keyBackend, sdk: "sentry.python.fastapi", sdkVersion: "2.57.0", release: "fixture-release-1"}
	python266 = want{key: keyIntegrations, sdk: "sentry.python.fastapi", sdkVersion: "2.66.1", release: "fixture-release-1"}
	angular   = want{key: keyFront, sdk: "sentry.javascript.angular", sdkVersion: "11.0.0"}
)

func with(base want, f func(*want)) want {
	f(&base)
	return base
}

// Una fila por fichero de testdata/envelopes, según la tabla de tools/fixtures/README.md.
var fixtures = map[string]want{
	"backend-ardiq-task-failed-a-1": with(python257, func(w *want) {
		w.logger = "ardiq"
		w.message = `task failed id=43ed20e1ee514256b5fd6db53d2ad712 name='enviar_factura' worker=723be757 try=4 duration_ms=190918 error=AttributeError("'NoneType' object has no attribute '__dict__'")`
	}),
	"backend-ardiq-task-failed-b-1": with(python257, func(w *want) {
		w.logger = "ardiq"
		w.message = `task failed id=193d9a4704ab46469ffec6b691642810 name='enviar_factura' worker=f94152a2 try=4 duration_ms=190918 error=AttributeError("'NoneType' object has no attribute '__dict__'")`
	}),
	"backend-fastapi-zerodivision-1": with(python257, func(w *want) {
		w.excType, w.inApp, w.request = "ZeroDivisionError", true, "GET http://testserver/__prueba/division"
	}),
	"backend-fastapi-zerodivision-2": {key: keyBackend, noEvent: true},
	"backend-fastapi-zerodivision-3": {key: keyBackend, noEvent: true},
	"backend-intakeerror-404-1":      {key: keyBackend, noEvent: true},
	"backend-intakeerror-404-2":      {key: keyBackend, noEvent: true},
	"backend-structlog-error-1": with(python257, func(w *want) {
		w.message = "ai.action_reported_failure"
	}),
	// Lanzado desde el __main__ del script de grabación: ningún fotograma es de la app.
	"backend-structlog-exception-1": with(python257, func(w *want) {
		w.excType = "KeyError"
	}),
	"backend-validationerror-1": with(python257, func(w *want) {
		w.excType, w.inApp, w.request = "ValidationError", true, "GET http://testserver/__prueba/validacion"
	}),
	"backend-validationerror-2": {key: keyBackend, noEvent: true},
	"backend-validationerror-3": {key: keyBackend, noEvent: true},
	"frontend-angular-typeerror-1": with(angular, func(w *want) {
		w.excType, w.inApp, w.request = "TypeError", true, " https://app.example.com/auth/login"
	}),
	"integrations-fastapi-typeerror-1": with(python266, func(w *want) {
		w.excType, w.inApp, w.request = "TypeError", true, "GET http://testserver/__prueba/twilio"
	}),
	"integrations-fastapi-typeerror-2": {key: keyIntegrations, noEvent: true},
	"integrations-fastapi-typeerror-3": {key: keyIntegrations, noEvent: true},
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
					if it.Header.Type != envelope.TypeSessions && it.Header.Type != envelope.TypeClientReport {
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
				if f.Filename == "" || f.Function == "" || f.Line == 0 {
					t.Errorf("fotograma incompleto: %+v", f)
				}
			}
		})
	}
}

func TestFixturesPythonFrameContext(t *testing.T) {
	env, _ := readFixture(t, "backend-fastapi-zerodivision-1")
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
	if e.ServerName == "" || e.Transaction != "/__prueba/division" || e.Tags["component"] != "api" {
		t.Errorf("server_name %q, transaction %q, tags %v", e.ServerName, e.Transaction, e.Tags)
	}
}

func TestFixturesFrontColumns(t *testing.T) {
	env, _ := readFixture(t, "frontend-angular-typeerror-1")
	e, err := event.Parse(env.Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	if e.Platform != "javascript" || e.Environment != "production" {
		t.Errorf("plataforma %q, entorno %q", e.Platform, e.Environment)
	}
	for _, f := range e.Exceptions[0].Frames {
		if f.Column == 0 {
			t.Errorf("fotograma sin columna: %+v", f)
		}
	}
}
