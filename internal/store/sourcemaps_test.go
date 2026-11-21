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
	got, err := s.SourceMap(ctx, p.ID, "r1", "", "chunk-A.js")
	if err != nil || string(got) != `{"v":2}` {
		t.Fatalf("tras resubir = %q, %v", got, err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sourcemaps`).Scan(&n); err != nil || n != 1 {
		t.Errorf("filas = %d, %v", n, err)
	}

	if _, err := s.SourceMap(ctx, p.ID, "r2", "", "chunk-A.js"); !errors.Is(err, ErrNotFound) {
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
	if _, err := s.SourceMap(ctx, p.ID, "r0", "", "chunk-A.js"); !errors.Is(err, ErrNotFound) {
		t.Errorf("el viejo sigue: err = %v", err)
	}
	if _, err := s.SourceMap(ctx, p.ID, "r1", "", "chunk-A.js"); err != nil {
		t.Errorf("el reciente se borró: %v", err)
	}
}

func TestSourceMapsDistAndDebugID(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "front", "clave-front")
	if err != nil {
		t.Fatal(err)
	}
	const id1, id2 = "174ab4b5-75a1-56fb-be73-185871cd4267", "86c80c38-f83a-593e-aa45-9e741c94fda4"
	put := func(f SourceMapFile) {
		t.Helper()
		if err := s.PutSourceMaps(ctx, p.ID, "r1", []SourceMapFile{f}); err != nil {
			t.Fatal(err)
		}
	}
	get := func(dist string) string {
		t.Helper()
		b, err := s.SourceMap(ctx, p.ID, "r1", dist, "a.js")
		if err != nil {
			t.Fatalf("dist %q: %v", dist, err)
		}
		return string(b)
	}

	put(SourceMapFile{File: "a.js", Map: []byte("sin-dist")})
	put(SourceMapFile{File: "a.js", Dist: "d1", URL: "~/a.js", Map: []byte("d1")})
	if got := get("d1"); got != "d1" {
		t.Errorf("dist d1 = %q", got)
	}
	if got := get("d2"); got != "sin-dist" {
		t.Errorf("otra dist = %q; quiero el que no tiene dist", got)
	}

	// Los de debug ID no sustituyen a los de nombre, pero por nombre gana el más nuevo.
	put(SourceMapFile{File: "a.js", DebugID: id1, Map: []byte("id1")})
	put(SourceMapFile{File: "a.js", DebugID: id2, Map: []byte("id2")})
	put(SourceMapFile{File: "a.js", DebugID: id1, Map: []byte("id1-otra-vez")})
	if got := get(""); got != "id1-otra-vez" {
		t.Errorf("por nombre = %q; quiero el más nuevo", got)
	}
	for id, want := range map[string]string{id1: "id1-otra-vez", id2: "id2"} {
		b, err := s.SourceMapByDebugID(ctx, p.ID, id)
		if err != nil || string(b) != want {
			t.Errorf("debug ID %s = %q, %v", id, b, err)
		}
	}
	if _, err := s.SourceMapByDebugID(ctx, p.ID+1000, id1); !errors.Is(err, ErrNotFound) {
		t.Errorf("otro proyecto: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM sourcemaps`).Scan(&n); err != nil || n != 4 {
		t.Errorf("filas = %d, %v", n, err)
	}
}

func TestUploadChunksAndBundles(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	p, err := s.CreateProject(ctx, "front", "clave-front")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.PutUploadChunks(ctx, map[string][]byte{"aa": []byte("a"), "bb": []byte("b")}); err != nil {
		t.Fatal(err)
	}
	missing, err := s.MissingUploadChunks(ctx, []string{"aa", "cc", "bb", "cc"})
	if err != nil || len(missing) != 1 || missing[0] != "cc" {
		t.Errorf("faltan = %v, %v", missing, err)
	}
	if b, err := s.UploadChunk(ctx, "bb"); err != nil || string(b) != "b" {
		t.Errorf("trozo bb = %q, %v", b, err)
	}
	if _, err := s.UploadChunk(ctx, "cc"); !errors.Is(err, ErrNotFound) {
		t.Errorf("trozo que no está: %v", err)
	}

	b := Bundle{Checksum: "ff", ProjectIDs: []int64{p.ID}, Release: "r1", Files: []SourceMapFile{{File: "a.js", Map: []byte("m")}}}
	if done, err := s.BundleAssembled(ctx, "ff", []int64{p.ID}); err != nil || done {
		t.Fatalf("antes: %v, %v", done, err)
	}
	if err := s.PutBundle(ctx, b); err != nil {
		t.Fatal(err)
	}
	if done, err := s.BundleAssembled(ctx, "ff", []int64{p.ID}); err != nil || !done {
		t.Errorf("después: %v, %v", done, err)
	}
	b.ProjectIDs = []int64{p.ID, p.ID + 1000}
	if err := s.PutBundle(ctx, b); !errors.Is(err, ErrNotFound) {
		t.Errorf("proyecto que no existe: %v", err)
	}
}
