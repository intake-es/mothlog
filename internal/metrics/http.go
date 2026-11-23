package metrics

import (
	"net/http"
	"strconv"
	"strings"
)

// Routes cuenta las peticiones a la API (/api/0/) y al MCP (/mcp) que sirve
// mux, con la ruta del patrón que casó, sin los ids. Con m nil, devuelve mux.
func (m *Metrics) Routes(mux http.Handler) http.Handler {
	if m == nil {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if !strings.HasPrefix(p, "/api/0/") && p != "/mcp" && !strings.HasPrefix(p, "/mcp/") {
			mux.ServeHTTP(w, r)
			return
		}
		cw := &codeWriter{ResponseWriter: w}
		mux.ServeHTTP(cw, r)
		// ServeMux deja en r.Pattern el patrón que casó.
		method, route := r.Method, r.Pattern
		if meth, rest, ok := strings.Cut(route, " "); ok {
			method, route = meth, rest
		}
		if route == "" {
			route = "unmatched"
		}
		if !knownMethods[method] {
			method = "other"
		}
		if cw.code == 0 {
			cw.code = http.StatusOK
		}
		m.requests.add(1, method, route, strconv.Itoa(cw.code))
	})
}

// knownMethods son los métodos que van tal cual en la etiqueta; el resto,
// que cualquiera puede inventarse, va como other.
var knownMethods = map[string]bool{
	http.MethodGet: true, http.MethodHead: true, http.MethodPost: true, http.MethodPut: true,
	http.MethodPatch: true, http.MethodDelete: true, http.MethodOptions: true,
}

// Register añade GET /metrics a mux, protegido con token.
func (m *Metrics) Register(token string) func(*http.ServeMux) {
	return func(mux *http.ServeMux) { mux.Handle("GET /metrics", m.Handler(token)) }
}

type codeWriter struct {
	http.ResponseWriter
	code int
}

func (w *codeWriter) WriteHeader(code int) {
	if w.code == 0 {
		w.code = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *codeWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.ResponseWriter.Write(b)
}

// Flush deja que el MCP mande sus eventos por streaming.
func (w *codeWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (w *codeWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// CodeWriter envuelve w para saber con qué código respondió.
func CodeWriter(w http.ResponseWriter) (http.ResponseWriter, func() int) {
	cw := &codeWriter{ResponseWriter: w}
	return cw, func() int {
		if cw.code == 0 {
			return http.StatusOK
		}
		return cw.code
	}
}
