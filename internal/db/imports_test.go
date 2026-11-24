package db

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// pgxAllowed son los únicos directorios que pueden importar pgx.
var pgxAllowed = []string{"internal/db", "internal/store"}

// pgxImports da, de los .go bajo root, los que importan pgx fuera de pgxAllowed.
func pgxImports(root string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); path != root && (name == "node_modules" || name == "testdata" || strings.HasPrefix(name, ".")) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if slices.ContainsFunc(pgxAllowed, func(dir string) bool { return strings.HasPrefix(rel, dir+"/") }) {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			if p == "github.com/jackc/pgx" || strings.HasPrefix(p, "github.com/jackc/pgx/") {
				out = append(out, rel+" importa "+p)
			}
		}
		return nil
	})
	return out, err
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func TestOnlyDBAndStoreImportPgx(t *testing.T) {
	bad, err := pgxImports(moduleRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range bad {
		t.Errorf("%s: solo internal/db e internal/store hablan con pgx; usa internal/db "+
			"(db.DB, db.Tx, db.Rows, db.Batch, db.BeginFunc, db.ErrNoRows…) y, para el SQL propio de Postgres, su Dialect", b)
	}
}

func TestPgxRuleCatchesImports(t *testing.T) {
	root := t.TempDir()
	write := func(rel, src string) {
		t.Helper()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("internal/alert/alert.go", "package alert\n\nimport \"github.com/jackc/pgx/v5\"\n\nvar _ pgx.Tx\n")
	write("internal/ingest/ingest_test.go", "package ingest\n\nimport (\n\t\"fmt\"\n\tpool \"github.com/jackc/pgx/v5/pgxpool\"\n)\n")
	write("cmd/mothlog/main.go", "package main\n\nimport \"github.com/intake-es/mothlog/internal/db\"\n")
	write("internal/db/postgres.go", "package db\n\nimport \"github.com/jackc/pgx/v5\"\n")
	write("internal/store/storetest/storetest.go", "package storetest\n\nimport \"github.com/jackc/pgx/v5/pgconn\"\n")
	write("internal/dbx/x.go", "package dbx\n\nimport \"github.com/jackc/pgx/v5\"\n")
	write("web/node_modules/x/x.go", "package x\n\nimport \"github.com/jackc/pgx/v5\"\n")

	bad, err := pgxImports(root)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"internal/alert/alert.go importa github.com/jackc/pgx/v5",
		"internal/dbx/x.go importa github.com/jackc/pgx/v5",
		"internal/ingest/ingest_test.go importa github.com/jackc/pgx/v5/pgxpool",
	}
	if !slices.Equal(bad, want) {
		t.Errorf("la regla ve %q; quiero %q", bad, want)
	}
}
