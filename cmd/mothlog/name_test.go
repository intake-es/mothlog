package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// oldName es el nombre de antes de Mothlog, con cualquier separador y mayúsculas.
var oldName = regexp.MustCompile(`(?i)intake[-_ ]?errors`)

// oldNameAllowed son los ficheros que nombran a propósito el nombre viejo, y por qué.
var oldNameAllowed = map[string]string{
	"internal/alert/webhook.go":                        "las cabeceras X-Intake-Errors-* que el webhook manda también, hasta la 2.0.0",
	"internal/alert/rules_test.go":                     "comprueba que van esas cabeceras",
	"docs/alerts.md":                                   "cuenta que van esas cabeceras",
	"migrations/00023_sesiones_e_identidad.sql":        "migración ya aplicada: no se cambia",
	"migrations/sqlite/00023_sesiones_e_identidad.sql": "migración ya aplicada: no se cambia",
	"cmd/mothlog/name_test.go":                         "este test",
}

// Fuera de la lista de compatibilidad, no queda el nombre viejo en el código,
// la configuración, la web ni la documentación.
func TestNoOldName(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	skip := []string{".git", "node_modules", ".angular", "dist"}
	var found []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path != root && slices.Contains(skip, d.Name()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		if _, ok := oldNameAllowed[rel]; ok {
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if oldName.MatchString(line) {
				found = append(found, rel+":"+strconv.Itoa(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		t.Errorf("queda el nombre viejo fuera de oldNameAllowed:\n%s", strings.Join(found, "\n"))
	}
}
