package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseImportArgs(t *testing.T) {
	a, err := parseImportArgs([]string{"sentry", "--url", "https://sentry.example.com", "--org", "ejemplo",
		"--project", "api", "--project=web,front", "--since", "2026-10-01", "--events", "3", "--dry-run"})
	if err != nil {
		t.Fatal(err)
	}
	if a.url != "https://sentry.example.com" || a.org != "ejemplo" || strings.Join(a.projects, ",") != "api,web,front" ||
		!a.since.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || a.events != 3 || !a.dryRun {
		t.Errorf("= %+v", a)
	}
	if a, err := parseImportArgs([]string{"sentry", "--url", "u", "--org", "o"}); err != nil || a.events != 5 || a.dryRun {
		t.Errorf("por defecto = %+v, %v", a, err)
	}
	for _, args := range [][]string{
		{},
		{"otro"},
		{"sentry", "--org", "o"},
		{"sentry", "--url", "u"},
		{"sentry", "--url", "u", "--org", "o", "--events", "0"},
		{"sentry", "--url", "u", "--org", "o", "--since", "ayer"},
		{"sentry", "--url", "u", "--org", "o", "sobra"},
	} {
		if _, err := parseImportArgs(args); err == nil {
			t.Errorf("%q debería fallar", args)
		}
	}
	const secret = "sntrys_no_va_aqui"
	for _, args := range [][]string{
		{"sentry", "--url", "u", "--org", "o", "--token", secret},
		{"sentry", "--url", "u", "--org", "o", "-token=" + secret},
	} {
		_, err := parseImportArgs(args)
		if err == nil || !strings.Contains(err.Error(), "SENTRY_IMPORT_TOKEN") || strings.Contains(err.Error(), secret) {
			t.Errorf("%q = %v; quiero que pida SENTRY_IMPORT_TOKEN sin repetir el token", args, err)
		}
	}
}
