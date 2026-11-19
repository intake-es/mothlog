package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
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
			routes(fakeDB{tc.err}).ServeHTTP(rec, httptest.NewRequest(tc.method, "/healthz", nil))
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
