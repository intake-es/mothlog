package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/intake-es/intake-errors/internal/store"
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
		"http://127.0.0.1:8080":          "http://abc@127.0.0.1:8080/3",
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
