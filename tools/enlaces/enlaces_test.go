package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// Los Markdown del repositorio no tienen enlaces rotos.
func TestRepoLinks(t *testing.T) {
	files, broken, err := check(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if files < 10 {
		t.Fatalf("solo %d ficheros Markdown: ¿se ha movido la documentación?", files)
	}
	if len(broken) > 0 {
		t.Errorf("enlaces rotos:\n%s", strings.Join(broken, "\n"))
	}
}

func TestSlugify(t *testing.T) {
	for title, want := range map[string]string{
		"Quick start":                         "quick-start",
		"1. Create a token":                   "1-create-a-token",
		"`mothlog people revoke`":             "mothlog-people-revoke",
		"Filters, cap and sampling":           "filters-cap-and-sampling",
		"Linear and GitHub both ways":         "linear-and-github-both-ways",
		"Con SQLite, sin servidor (de datos)": "con-sqlite-sin-servidor-de-datos",
		"Búsqueda":                            "búsqueda",
		"See [the API](api.md)":               "see-the-api",
	} {
		if got := slugify(title); got != want {
			t.Errorf("slugify(%q) = %q, want %q", title, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("README.md", "# Top\n\n## Same\n\n## Same\n\n"+
		"[ok](docs/a.md) [ok](docs/a.md#part-two) [ok](#same-1) [ok](docs/img.png) [ok](https://example.com/x#y)\n"+
		"[bad](docs/missing.md) [bad](#nope) [bad](docs/a.md#nope) [bad](docs/img.png#x)\n"+
		"`[code](not-checked.md)`\n```\n[code](not-checked.md)\n```\n")
	write("docs/a.md", "# A\n\n## Part two\n\n[ok](../README.md#top) ![ok](img.png \"title\")\n")
	write("docs/img.png", "")

	files, broken, err := check(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"README.md:8: docs/missing.md: no existe",
		"README.md:8: #nope: no hay ningún título con esa ancla",
		"README.md:8: docs/a.md#nope: no hay ningún título con esa ancla",
		"README.md:8: docs/img.png#x: ancla en un fichero que no es Markdown",
	}
	if files != 2 || !slices.Equal(broken, want) {
		t.Errorf("files = %d, broken:\n%s", files, strings.Join(broken, "\n"))
	}
}
