package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/metrics"
	"github.com/intake-es/intake-errors/internal/store"
	"github.com/intake-es/intake-errors/internal/ui"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func TestHealthz(t *testing.T) {
	for _, tc := range []struct {
		name   string
		err    error
		method string
		want   int
	}{
		{"ok", nil, http.MethodGet, http.StatusOK},
		{"sin postgres", errors.New("caído"), http.MethodGet, http.StatusServiceUnavailable},
		{"post", nil, http.MethodPost, http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			routes(fakeDB{tc.err}, http.NotFoundHandler()).ServeHTTP(rec, httptest.NewRequest(tc.method, "/healthz", nil))
			if rec.Code != tc.want {
				t.Fatalf("código = %d, quiero %d", rec.Code, tc.want)
			}
		})
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if _, err := loadConfig(); err == nil {
		t.Error("sin DATABASE_URL debería fallar")
	}
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("LISTEN_ADDR", "")
	t.Setenv("LOG_LEVEL", "debug")
	c, err := loadConfig()
	if err != nil || c.Addr != ":8080" || c.LogLevel.String() != "DEBUG" {
		t.Fatalf("= %+v, %v", c, err)
	}
}

func TestLoadConfigLimits(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("RATE_LIMIT_PER_MINUTE", "")
	t.Setenv("RETENTION_DAYS", "")
	c, err := loadConfig()
	if err != nil || c.RateLimitPerMinute != 60 || c.RetentionDays != 90 {
		t.Fatalf("= %+v, %v", c, err)
	}
	for _, v := range []string{"0", "-1", "mucho"} {
		t.Setenv("RETENTION_DAYS", v)
		if _, err := loadConfig(); err == nil {
			t.Errorf("RETENTION_DAYS=%q debería fallar", v)
		}
	}
}

func TestDSN(t *testing.T) {
	p := store.Project{ID: 3, PublicKey: "abc"}
	for in, want := range map[string]string{
		"https://errors.example.com":       "https://abc@errors.example.com/3",
		"https://errors.example.com/":      "https://abc@errors.example.com/3",
		"http://127.0.0.1:8080":            "http://abc@127.0.0.1:8080/3",
		"https://errors.example.com:8443/": "https://abc@errors.example.com:8443/3",
	} {
		if got, err := dsn(in, p); err != nil || got != want {
			t.Errorf("dsn(%q) = %q, %v; quiero %q", in, got, err, want)
		}
	}
	for _, in := range []string{"", "errors.example.com", "ftp://x"} {
		if _, err := dsn(in, p); err == nil {
			t.Errorf("dsn(%q) debería fallar", in)
		}
	}
}

func TestPublicKeyFormat(t *testing.T) {
	k, err := newPublicKey()
	if err != nil || len(k) != 32 || strings.Trim(k, "0123456789abcdef") != "" {
		t.Fatalf("clave %q, %v", k, err)
	}
}

func TestLoadConfigRetention(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://x")
	t.Setenv("RETENTION_DAYS", "")
	if c, err := loadConfig(); err != nil || c.RetentionDays != 90 {
		t.Fatalf("por defecto = %d, %v", c.RetentionDays, err)
	}
	t.Setenv("RETENTION_DAYS", "30")
	if c, err := loadConfig(); err != nil || c.RetentionDays != 30 {
		t.Fatalf("= %d, %v", c.RetentionDays, err)
	}
	for _, bad := range []string{"0", "-1", "noventa"} {
		t.Setenv("RETENTION_DAYS", bad)
		if _, err := loadConfig(); err == nil {
			t.Errorf("RETENTION_DAYS=%q debería fallar", bad)
		}
	}
}

func TestDevLoginOnlyLocal(t *testing.T) {
	t.Setenv("DATABASE_URL", "postgres://127.0.0.1:1/nada")
	t.Setenv("UI_DEV_LOGIN", "1")
	t.Setenv("LISTEN_ADDR", ":8080")
	err := run(nil)
	if err == nil || !strings.Contains(err.Error(), "UI_DEV_LOGIN") {
		t.Fatalf("con LISTEN_ADDR=:8080 = %v; quiero que se niegue a arrancar", err)
	}
}

type metricsSource struct{ pool db.DB }

func (m metricsSource) MetricsSnapshot(context.Context) (store.MetricsSnapshot, error) {
	return store.MetricsSnapshot{}, nil
}

func (m metricsSource) Pool() db.DB { return m.pool }

func TestMetricsRoute(t *testing.T) {
	get := func(h http.Handler, auth string) int {
		r := httptest.NewRequest(http.MethodGet, "/metrics", nil)
		if auth != "" {
			r.Header.Set("Authorization", auth)
		}
		rec := httptest.NewRecorder()
		ui.Handler(h).ServeHTTP(rec, r)
		return rec.Code
	}
	if code := get(routes(fakeDB{}, http.NotFoundHandler()), ""); code != http.StatusNotFound {
		t.Errorf("sin METRICS_TOKEN, /metrics da %d; quiero 404", code)
	}
	pool, err := db.OpenPostgres(context.Background(), "postgres://x:x@127.0.0.1:1/x")
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	met := metrics.New(metricsSource{pool}, nil)
	mux := met.Routes(routes(fakeDB{}, http.NotFoundHandler(), met.Register("secreto")))
	for auth, want := range map[string]int{
		"":               http.StatusUnauthorized,
		"Bearer malo":    http.StatusUnauthorized,
		"Bearer secreto": http.StatusOK,
	} {
		if code := get(mux, auth); code != want {
			t.Errorf("Authorization %q: %d; quiero %d", auth, code, want)
		}
	}
}
