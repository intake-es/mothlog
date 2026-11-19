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
