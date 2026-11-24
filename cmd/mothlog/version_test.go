package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPrintVersion(t *testing.T) {
	defer func(v, c, d string) { version, commit, date = v, c, d }(version, commit, date)
	version, commit, date = "1.2.3", "abc1234", "2026-11-23T10:00:00Z"
	var out bytes.Buffer
	if err := printVersion(&out); err != nil {
		t.Fatal(err)
	}
	want := "mothlog 1.2.3\ncommit: abc1234\ndate: 2026-11-23T10:00:00Z\n"
	if out.String() != want {
		t.Fatalf("salida = %q, quiero %q", out.String(), want)
	}
}

func TestVersionSinDatabaseURL(t *testing.T) {
	t.Setenv("DATABASE_URL", "")
	if err := run([]string{"version"}); err != nil {
		t.Fatalf("version sin DATABASE_URL: %v", err)
	}
}

func TestVersionEnLaAyuda(t *testing.T) {
	if !strings.Contains(usage, "mothlog version") {
		t.Fatal("la ayuda no menciona version")
	}
}
