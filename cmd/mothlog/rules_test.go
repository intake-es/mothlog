package main

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/intake-es/mothlog/internal/alert"
	"github.com/intake-es/mothlog/internal/store/storetest"
)

func TestRulesCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	db.AddProject(t, "web")
	alerts, err := alert.New(db.Store, slog.New(slog.NewTextHandler(io.Discard, nil)), alert.Options{AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	var received atomic.Int32
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(alert.HeaderSignature) != "" {
			received.Add(1)
		}
	}))
	t.Cleanup(hook.Close)
	run := func(stdin string, args ...string) (string, error) {
		var out bytes.Buffer
		err := rulesCommand(ctx, db.Store, alerts, append([]string{"web"}, args...), strings.NewReader(stdin), &out)
		return out.String(), err
	}

	out, err := run("")
	if err != nil || !strings.Contains(out, "has no rules") {
		t.Fatalf("lista vacía = %q, %v", out, err)
	}
	rule := `{"name": "Al agente", "conditions": {"new": true, "frequency": {"events": 50, "window_minutes": 10}, "levels": ["error"]},
		"actions": [{"id": "agente", "type": "webhook", "url": "` + hook.URL + `"}]}`
	out, err = run(rule, "create", "-")
	if err != nil || !strings.Contains(out, `"name": "Al agente"`) || !strings.Contains(out, "Save the webhook secret") {
		t.Fatalf("create = %q, %v", out, err)
	}
	out, err = run("")
	if err != nil || !strings.Contains(out, "Al agente") || !strings.Contains(out, "new or >50 events/10 min; level error") ||
		!strings.Contains(out, "webhook:agente") {
		t.Fatalf("lista = %q, %v", out, err)
	}
	id := "1"
	if out, err = run("", "show", id); err != nil || !strings.Contains(out, `"secret": "…`) {
		t.Fatalf("show = %q, %v", out, err)
	}
	if out, err = run("", "test", id); err != nil || !strings.Contains(out, "agente (webhook): sent") || received.Load() != 1 {
		t.Fatalf("test = %q, %v, recibidos %d", out, err, received.Load())
	}

	file := filepath.Join(t.TempDir(), "regla.json")
	if err := os.WriteFile(file, []byte(strings.Replace(rule, "Al agente", "Al agente de guardia", 1)), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err = run("", "edit", id, file); err != nil || strings.Contains(out, "Save the webhook secret") {
		t.Fatalf("edit = %q, %v: editar sin secreto conserva el que había", out, err)
	}
	if out, _ = run(""); !strings.Contains(out, "Al agente de guardia") {
		t.Errorf("tras editar = %q", out)
	}
	if _, err = run(`{"name": "x", "conditions": {"new": true}, "actions": [{"type": "email", "to": ["a@b.test"]}]}`, "create", "-"); err == nil ||
		!strings.Contains(err.Error(), "SMTP_URL") {
		t.Errorf("correo sin SMTP_URL = %v", err)
	}
	if _, err = run("", "delete", id); err != nil {
		t.Fatal(err)
	}
	if _, err = run("", "delete", id); err == nil || !strings.Contains(err.Error(), "has no rule 1") {
		t.Errorf("borrar dos veces = %v", err)
	}
	if _, err = run("", "otra"); err == nil || !strings.Contains(err.Error(), "usage of projects rules") {
		t.Errorf("orden desconocida = %v", err)
	}
}
