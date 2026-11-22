package main

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
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

func TestFiltersCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "web")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := filtersCommand(ctx, db.Store, args, &out)
		return out.String(), err
	}

	out, err := run("web")
	if err != nil || !strings.Contains(out, "events_per_issue_per_hour  100 (por defecto)") ||
		!strings.Contains(out, "rate_limit_per_minute      RATE_LIMIT_PER_MINUTE") || !strings.Contains(out, "sample_rate                1") {
		t.Fatalf("por defecto: %q, %v", out, err)
	}
	for _, args := range [][]string{
		{"web", "set", "messages", "^ResizeObserver loop", `Script error\.?`},
		{"web", "set", "exception_types", "KeyError"},
		{"web", "set", "releases", "*-beta.*"},
		{"web", "set", "environments", "development", "staging"},
		{"web", "set", "browser_extensions", "on"},
		{"web", "set", "web_crawlers", "on"},
		{"web", "set", "localhost", "on"},
		{"web", "set", "events_per_issue_per_hour", "25"},
		{"web", "set", "sample_rate", "0.1"},
		{"web", "set", "rate_limit_per_minute", "500"},
		{"web", "set", "environments"},
		{"web", "set", "localhost", "off"},
	} {
		if _, err := run(args...); err != nil {
			t.Fatalf("%v: %v", args, err)
		}
	}
	f, err := db.Filters(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.Messages, []string{"^ResizeObserver loop", `Script error\.?`}) || !slices.Equal(f.ExceptionTypes, []string{"KeyError"}) ||
		!slices.Equal(f.Releases, []string{"*-beta.*"}) || len(f.Environments) != 0 || !f.BrowserExtensions || !f.WebCrawlers || f.Localhost ||
		f.PerIssuePerHour() != 25 || f.SampleRate != 0.1 || *f.RateLimitPerMinute != 500 {
		t.Fatalf("guardado = %+v", f)
	}
	out, err = run("web", "set", "rate_limit_per_minute", "default")
	if err != nil || !strings.Contains(out, "RATE_LIMIT_PER_MINUTE") || !strings.Contains(out, "events_per_issue_per_hour  25\n") {
		t.Errorf("volver al límite por defecto: %q, %v", out, err)
	}

	for _, args := range [][]string{
		{"web", "set", "otra", "x"},
		{"web", "set", "localhost", "sí"},
		{"web", "set", "localhost", "on", "off"},
		{"web", "set", "sample_rate", "2"},
		{"web", "set", "sample_rate", "mitad"},
		{"web", "set", "events_per_issue_per_hour", "0"},
		{"web", "set", "messages", "(roto"},
		{"nadie"},
	} {
		if _, err := run(args...); err == nil {
			t.Errorf("%v sin error", args)
		}
	}
	if again, _ := db.Filters(ctx, p.ID); again.SampleRate != 0.1 || again.PerIssuePerHour() != 25 {
		t.Errorf("un cambio no válido ha tocado lo guardado: %+v", again)
	}
}

func TestDroppedCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "web")
	if err := db.AddDropped(ctx, []store.Dropped{
		{ProjectID: p.ID, Reason: store.DropWebCrawler, Day: time.Now(), Count: 12},
		{ProjectID: p.ID, Reason: store.DropSampled, Day: time.Now().AddDate(0, 0, -5), Count: 3},
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := droppedCommand(ctx, db.Store, []string{"web"}, &out); err != nil {
		t.Fatal(err)
	}
	if s := out.String(); !strings.Contains(s, "web_crawler        12\n") || !strings.Contains(s, "sampled            3\n") || !strings.Contains(s, "total              15\n") {
		t.Errorf("salida = %q", s)
	}
	out.Reset()
	if err := droppedCommand(ctx, db.Store, []string{"web", "1"}, &out); err != nil || !strings.Contains(out.String(), "total              12\n") {
		t.Errorf("un día: %q, %v", out.String(), err)
	}
	if err := droppedCommand(ctx, db.Store, []string{"web", "x"}, &out); err == nil {
		t.Error("días no numéricos sin error")
	}
}

func TestReleasesCommand(t *testing.T) {
	db := storetest.NewDB(t)
	ctx := context.Background()
	p := db.AddProject(t, "web")
	run := func(args ...string) (string, error) {
		var out bytes.Buffer
		err := releasesCommand(ctx, db.Store, args, &out)
		return out.String(), err
	}

	out, err := run("new", "web", "web@1.0.0")
	if err != nil || !strings.Contains(out, "creada") {
		t.Fatalf("new = %q, %v", out, err)
	}
	out, err = run("new", "web", "web@1.0.0", "--env", "production")
	if err != nil || !strings.Contains(out, "ya existía") || !strings.Contains(out, "desplegada en production") {
		t.Fatalf("new --env = %q, %v", out, err)
	}
	if _, err := run("new", "web", "web@1.1.0", "--env=staging"); err != nil {
		t.Fatal(err)
	}
	r, err := db.Release(ctx, p.ID, "web@1.0.0", "")
	if err != nil || len(r.Deploys) != 1 || r.Deploys[0].Environment != "production" {
		t.Errorf("web@1.0.0 = %+v, %v", r, err)
	}
	r, err = db.Release(ctx, p.ID, "web@1.1.0", "")
	if err != nil || len(r.Deploys) != 1 || r.Deploys[0].Environment != "staging" {
		t.Errorf("web@1.1.0 = %+v, %v", r, err)
	}
	for _, bad := range [][]string{{"new", "web"}, {"new", "otro", "v1"}, {"list"}, {"new", "web", "v1", "--env"}, {"new", "web", " "}} {
		if _, err := run(bad...); err == nil {
			t.Errorf("%v sin error", bad)
		}
	}
}
