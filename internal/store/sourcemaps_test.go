package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSourceMaps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "front", "clave-front")
	if err != nil {
		t.Fatal(err)
	}

	put := func(release, file, body string) error {
		return s.PutSourceMaps(ctx, p.ID, release, []SourceMapFile{{File: file, Map: []byte(body)}})
	}
	if err := put("r1", "chunk-A.js", `{"v":1}`); err != nil {
		t.Fatal(err)
	}
	if err := put("r1", "chunk-A.js", `{"v":2}`); err != nil {
		t.Fatal(err)
	}
	got, err := s.SourceMap(ctx, p.ID, "r1", "chunk-A.js")
	if err != nil || string(got) != `{"v":2}` {
		t.Fatalf("tras resubir = %q, %v", got, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sourcemaps`).Scan(&n); err != nil || n != 1 {
		t.Errorf("filas = %d, %v", n, err)
	}

	if _, err := s.SourceMap(ctx, p.ID, "r2", "chunk-A.js"); !errors.Is(err, ErrNotFound) {
		t.Errorf("otro release: err = %v", err)
	}
	if err := s.PutSourceMaps(ctx, p.ID+1000, "r1", []SourceMapFile{{File: "x.js", Map: []byte("{}")}}); !errors.Is(err, ErrNotFound) {
		t.Errorf("proyecto inexistente: err = %v", err)
	}

	if err := put("r0", "chunk-A.js", `{}`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE sourcemaps SET uploaded_at = now() - interval '91 days' WHERE release = 'r0'`); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.DeleteSourceMapsBefore(ctx, time.Now().AddDate(0, 0, -90))
	if err != nil || deleted != 1 {
		t.Fatalf("borrados = %d, %v", deleted, err)
	}
	if _, err := s.SourceMap(ctx, p.ID, "r0", "chunk-A.js"); !errors.Is(err, ErrNotFound) {
		t.Errorf("el viejo sigue: err = %v", err)
	}
	if _, err := s.SourceMap(ctx, p.ID, "r1", "chunk-A.js"); err != nil {
		t.Errorf("el reciente se borró: %v", err)
	}
}
