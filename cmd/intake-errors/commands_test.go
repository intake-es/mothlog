package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/intake-es/intake-errors/internal/store/storetest"
)

func TestTokensCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := tokensCommand(ctx, db.Store, args, &out)
		return out.String(), err
	}

	out, err := run("create", "claude")
	if err != nil {
		t.Fatal(err)
	}
	secret, _, _ := strings.Cut(out, "\n")
	if !strings.HasPrefix(secret, "ie_") || len(secret) < 40 {
		t.Fatalf("token = %q", secret)
	}
	if _, err := db.Authenticate(ctx, secret); err != nil {
		t.Fatalf("el token creado no autentica: %v", err)
	}
	var stored []byte
	if err := db.Pool().QueryRow(ctx, `SELECT token_sha256 FROM api_tokens`).Scan(&stored); err != nil || bytes.Contains(stored, []byte(secret)) || len(stored) != 32 {
		t.Errorf("guardado = %x, %v", stored, err)
	}
	if _, err := run("create", "claude"); err == nil {
		t.Error("dos tokens activos con el mismo nombre")
	}

	out, err = run("list")
	if err != nil || !strings.Contains(out, "claude") || strings.Contains(out, secret) {
		t.Errorf("list = %q, %v", out, err)
	}

	if _, err := run("revoke", "claude"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Authenticate(ctx, secret); err == nil {
		t.Error("el token revocado autentica")
	}
	if _, err := run("revoke", "claude"); err == nil {
		t.Error("revocar dos veces no falla")
	}
	if _, err := run("create", "claude"); err != nil {
		t.Errorf("tras revocar, el nombre debería quedar libre: %v", err)
	}
	if _, err := run("borrar"); err == nil {
		t.Error("orden desconocida sin error")
	}
}

func TestExtraLabelCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "backend")
	extraOn := func() bool {
		var on bool
		if err := db.Pool().QueryRow(ctx, `SELECT linear_extra_label FROM projects WHERE id = $1`, p.ID).Scan(&on); err != nil {
			t.Fatal(err)
		}
		return on
	}
	if extraOn() {
		t.Fatal("la etiqueta extra viene activada por defecto")
	}
	var out bytes.Buffer
	if err := extraLabelCommand(ctx, db.Store, "backend", "on", &out); err != nil || !extraOn() {
		t.Fatalf("on: %v, %q", err, out.String())
	}
	if err := extraLabelCommand(ctx, db.Store, "backend", "off", &out); err != nil || extraOn() {
		t.Fatalf("off: %v", err)
	}
	if err := extraLabelCommand(ctx, db.Store, "backend", "quizá", &out); err == nil {
		t.Error("estado desconocido sin error")
	}
	if err := extraLabelCommand(ctx, db.Store, "nadie", "on", &out); err == nil {
		t.Error("proyecto inexistente sin error")
	}
	db.AddProject(t, "backend")
	if err := extraLabelCommand(ctx, db.Store, "backend", "on", &out); err == nil || extraOn() {
		t.Errorf("con dos proyectos del mismo nombre: %v", err)
	}
}
