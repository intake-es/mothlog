package sourcemap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/intake-es/intake-errors/internal/store"
)

// pgStore abre un Store en un esquema propio de la base de pruebas.
func pgStore(t *testing.T) (*store.Store, *pgx.Conn, string) {
	t.Helper()
	base := os.Getenv("INTAKE_ERRORS_TEST_DATABASE_URL")
	if base == "" {
		t.Skip("sin INTAKE_ERRORS_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	schema := "test_" + hex.EncodeToString(b)

	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = admin.Close(context.Background())
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	s, err := store.Open(ctx, u.String(), quiet())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s, admin, schema
}

func TestPostgresUploadEnrichPurge(t *testing.T) {
	st, admin, schema := pgStore(t)
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "front", "0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f")
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, quiet(), Options{UploadToken: token})

	body, ct := multipartBody(t, mapFile(t, "login/chunk-A1B2C3D4.js.map"))
	if rec := post(t, s, uploadURL(p.ID+1, release1), "Bearer "+token, body, ct); rec.Code != http.StatusNotFound {
		t.Errorf("proyecto inexistente: %d %s", rec.Code, rec.Body)
	}

	uploadOK(t, s, p.ID, release1, mapFile(t, "login/chunk-A1B2C3D4.js.map"), mapFile(t, "login/chunk-E5F6G7H8.js.map"))
	uploadOK(t, s, p.ID, release1, mapFile(t, "login/chunk-A1B2C3D4.js.map"))
	var n int
	var size int64
	if err := admin.QueryRow(ctx, `SELECT count(*), sum(length(map_gzip)) FROM `+schema+`.sourcemaps`).Scan(&n, &size); err != nil || n != 2 {
		t.Fatalf("filas = %d, %v", n, err)
	}
	if size > 1<<20 {
		t.Errorf("guardados %d bytes: no parecen comprimidos", size)
	}

	ev := readEvent(t, "frontend-login-typeerror-1")
	if err := s.Enrich(ctx, p.ID, ev); err != nil {
		t.Fatal(err)
	}
	frames := ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename != loginPage+".ts" || top.Line != 268 || top.ContextLine != "    return data.cuenta!.id;" {
		t.Fatalf("primer fotograma = %s:%d %q", top.Filename, top.Line, top.ContextLine)
	}

	if _, err := admin.Exec(ctx, `UPDATE `+schema+`.sourcemaps SET uploaded_at = now() - interval '91 days' WHERE file = 'chunk-E5F6G7H8.js'`); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Purge(ctx, 90*24*time.Hour)
	if err != nil || deleted != 1 {
		t.Fatalf("borrados = %d, %v", deleted, err)
	}
	if _, err := st.SourceMap(ctx, p.ID, release1, "chunk-A1B2C3D4.js"); err != nil {
		t.Errorf("el reciente se borró: %v", err)
	}
}
