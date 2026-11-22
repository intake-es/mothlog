package ui

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
)

func newTestServer() http.Handler {
	app := fstest.MapFS{
		"index.html":                       {Data: []byte("<app-root></app-root>")},
		"main-AB12CD34.js":                 {Data: []byte("console.log(1)")},
		"styles-ZZ99YY88.css":              {Data: []byte("body{}")},
		"media/inter-latin-AAAA1111.woff2": {Data: []byte("font")},
		"favicon.svg":                      {Data: []byte("<svg/>")},
	}
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Next", "1")
		w.WriteHeader(http.StatusTeapot)
	})
	return New(app, next)
}

func get(t *testing.T, h http.Handler, method, path string) *http.Response {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
	return rec.Result()
}

func TestServeApp(t *testing.T) {
	h := newTestServer()
	for _, tc := range []struct {
		path, body, cache string
	}{
		{"/", "<app-root>", "no-cache"},
		{"/index.html", "<app-root>", "no-cache"},
		{"/issues/42", "<app-root>", "no-cache"},
		{"/login?error=org", "<app-root>", "no-cache"},
		{"/main-AB12CD34.js", "console.log", "public, max-age=31536000, immutable"},
		{"/styles-ZZ99YY88.css", "body{}", "public, max-age=31536000, immutable"},
		{"/media/inter-latin-AAAA1111.woff2", "font", "public, max-age=31536000, immutable"},
		{"/favicon.svg", "<svg/>", "no-cache"},
	} {
		res := get(t, h, http.MethodGet, tc.path)
		b, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || !strings.Contains(string(b), tc.body) || res.Header.Get("Cache-Control") != tc.cache {
			t.Errorf("GET %s = %d %q cache %q", tc.path, res.StatusCode, b, res.Header.Get("Cache-Control"))
		}
		if res.Header.Get("X-Next") != "" {
			t.Errorf("GET %s pasó al siguiente", tc.path)
		}
	}

	res := get(t, h, http.MethodGet, "/issues/42")
	if !strings.Contains(res.Header.Get("Content-Type"), "text/html") || !strings.Contains(res.Header.Get("Content-Security-Policy"), "default-src 'self'") {
		t.Errorf("cabeceras del index = %v", res.Header)
	}
	if res := get(t, h, http.MethodGet, "/main-AB12CD34.js"); !strings.Contains(res.Header.Get("Content-Type"), "javascript") {
		t.Errorf("Content-Type del js = %q", res.Header.Get("Content-Type"))
	}
}

func TestDoesNotCoverOtherRoutes(t *testing.T) {
	h := newTestServer()
	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/0/issues/"},
		{http.MethodGet, "/api/0/no-existe"},
		{http.MethodGet, "/api"},
		{http.MethodPost, "/api/1/envelope/"},
		{http.MethodGet, "/mcp"},
		{http.MethodPost, "/mcp"},
		{http.MethodGet, "/healthz"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/llms.txt"},
		{http.MethodGet, "/auth/session"},
		{http.MethodGet, "/auth/github/callback"},
		{http.MethodPost, "/auth/logout"},
		{http.MethodPost, "/"},
		{http.MethodDelete, "/issues/1"},
	} {
		res := get(t, h, tc.method, tc.path)
		if res.StatusCode != http.StatusTeapot || res.Header.Get("X-Next") != "1" {
			t.Errorf("%s %s = %d, no pasó al siguiente", tc.method, tc.path, res.StatusCode)
		}
	}
}

func TestMissingAsset(t *testing.T) {
	h := newTestServer()
	for _, p := range []string{"/main-OLD00000.js", "/media/nada.woff2", "/robots.txt"} {
		if res := get(t, h, http.MethodGet, p); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s = %d; quiero 404", p, res.StatusCode)
		}
	}
}

func TestAppRoutesWithDots(t *testing.T) {
	h := newTestServer()
	for _, p := range []string{"/releases/1/web@1.2.3", "/releases/2/2026.11.21-1", "/releases/1/v1.0"} {
		res := get(t, h, http.MethodGet, p)
		if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %d %s; quiero la app", p, res.StatusCode, res.Header.Get("Content-Type"))
		}
	}
}

func TestETag(t *testing.T) {
	h := newTestServer()
	res := get(t, h, http.MethodGet, "/")
	etag := res.Header.Get("ETag")
	if etag == "" {
		t.Fatal("sin ETag")
	}
	req := httptest.NewRequest(http.MethodGet, "/issues/1", nil)
	req.Header.Set("If-None-Match", etag)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotModified {
		t.Errorf("con If-None-Match = %d", rec.Code)
	}
}

func TestPlaceholder(t *testing.T) {
	h := New(fstest.MapFS{}, http.NotFoundHandler())
	res := get(t, h, http.MethodGet, "/issues/3")
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), "npm run build") {
		t.Errorf("= %d %s", res.StatusCode, b)
	}
}

// El binario se compila sin la web: Handler funciona con el dist de este checkout.
func TestEmbedded(t *testing.T) {
	h := Handler(http.NotFoundHandler())
	res := get(t, h, http.MethodGet, "/")
	b, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusOK || !strings.Contains(string(b), "<html") {
		t.Errorf("= %d %s", res.StatusCode, b)
	}
}
