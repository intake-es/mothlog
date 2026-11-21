package main

import (
	"bytes"
	"context"
	"slices"
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

func TestLabelsCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "backend")
	stored := func() []string {
		var ids []string
		if err := db.Pool().QueryRow(ctx, `SELECT linear_label_ids FROM projects WHERE id = $1`, p.ID).Scan(&ids); err != nil {
			t.Fatal(err)
		}
		return ids
	}
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := labelsCommand(ctx, db.Store, args, &out)
		return out.String(), err
	}

	if ids := stored(); len(ids) != 0 {
		t.Fatalf("etiquetas por defecto = %v", ids)
	}
	if out, err := run("backend"); err != nil || !strings.Contains(out, "no llevan etiquetas propias") {
		t.Fatalf("enseñar sin etiquetas: %q, %v", out, err)
	}

	if out, err := run("backend", "label-equipo, label-prioridad,label-equipo"); err != nil || !strings.Contains(out, "label-equipo, label-prioridad") {
		t.Fatalf("sustituir: %q, %v", out, err)
	}
	if ids := stored(); !slices.Equal(ids, []string{"label-equipo", "label-prioridad"}) {
		t.Fatalf("guardadas = %v", ids)
	}
	if out, err := run("backend"); err != nil || out != "label-equipo\nlabel-prioridad\n" {
		t.Fatalf("enseñar: %q, %v", out, err)
	}
	if _, err := run("backend", "label-otra"); err != nil || !slices.Equal(stored(), []string{"label-otra"}) {
		t.Fatalf("sustituir otra vez: %v, %v", err, stored())
	}

	for _, bad := range []string{"", ",", "label-a,", "label-a,,label-b", " ", "--clean", "label a"} {
		if _, err := run("backend", bad); err == nil {
			t.Errorf("%q sin error", bad)
		}
		if ids := stored(); !slices.Equal(ids, []string{"label-otra"}) {
			t.Fatalf("tras %q, guardadas = %v", bad, ids)
		}
	}

	for _, args := range [][]string{{"nadie"}, {"nadie", "label-a"}, {"nadie", "--clear"}} {
		if _, err := run(args...); err == nil || !strings.Contains(err.Error(), `no hay ningún proyecto llamado "nadie"`) {
			t.Errorf("%v: err = %v", args, err)
		}
	}

	if out, err := run("backend", "--clear"); err != nil || !strings.Contains(out, "ya no llevan") {
		t.Fatalf("quitar: %q, %v", out, err)
	}
	if ids := stored(); ids == nil || len(ids) != 0 {
		t.Fatalf("tras quitar = %#v", ids)
	}

	db.AddProject(t, "backend")
	if _, err := run("backend", "label-a"); err == nil || len(stored()) != 0 {
		t.Errorf("con dos proyectos del mismo nombre: %v", err)
	}
}
