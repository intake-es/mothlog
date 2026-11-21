package sourcemap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
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
	p, err := st.CreateProject(ctx, "web", "ce01335f125960aa6957a133d15b5f3a")
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, quiet(), Options{UploadToken: token})

	body, ct := multipartBody(t, mapFile(t, chunkJS+".map"))
	if rec := post(t, s, uploadURL(p.ID+1, release1), "Bearer "+token, body, ct); rec.Code != http.StatusNotFound {
		t.Errorf("proyecto inexistente: %d %s", rec.Code, rec.Body)
	}

	uploadOK(t, s, p.ID, release1, mapFile(t, chunkJS+".map"), mapFile(t, mainJS+".map"))
	uploadOK(t, s, p.ID, release1, mapFile(t, chunkJS+".map"))
	var n int
	var size int64
	if err := admin.QueryRow(ctx, `SELECT count(*), sum(length(map_gzip)) FROM `+schema+`.sourcemaps`).Scan(&n, &size); err != nil || n != 2 {
		t.Fatalf("filas = %d, %v", n, err)
	}
	if size > 1<<20 {
		t.Errorf("guardados %d bytes: no parecen comprimidos", size)
	}

	ev := readEvent(t, "web-typeerror-1")
	if err := s.Enrich(ctx, p.ID, ev); err != nil {
		t.Fatal(err)
	}
	frames := ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename != carrito || top.Line != 7 || top.ContextLine != failLine {
		t.Fatalf("primer fotograma = %s:%d %q", top.Filename, top.Line, top.ContextLine)
	}

	if _, err := admin.Exec(ctx, `UPDATE `+schema+`.sourcemaps SET uploaded_at = now() - interval '91 days' WHERE file = $1`, mainJS); err != nil {
		t.Fatal(err)
	}
	deleted, err := s.Purge(ctx, 90*24*time.Hour)
	if err != nil || deleted != 1 {
		t.Fatalf("borrados = %d, %v", deleted, err)
	}
	if _, err := st.SourceMap(ctx, p.ID, release1, "", chunkJS); err != nil {
		t.Errorf("el reciente se borró: %v", err)
	}
}

func TestPostgresSentryCLI(t *testing.T) {
	st, admin, schema := pgStore(t)
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "front", "7438a4393be53fabe513ce19bec380e9")
	if err != nil {
		t.Fatal(err)
	}
	_, apiSecret, err := st.CreateToken(ctx, "sentry-cli")
	if err != nil {
		t.Fatal(err)
	}
	s := New(st, quiet(), Options{UploadToken: token})

	replay(t, s, "upload", "Bearer "+token)
	replay(t, s, "inject", "Bearer "+apiSecret)
	count := func(q string) int {
		t.Helper()
		var n int
		if err := admin.QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.sourcemaps WHERE release = 'prueba-upload' AND debug_id IS NULL AND url LIKE '~/%.js'`); n != 2 {
		t.Errorf("mapas por nombre = %d", n)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.sourcemaps WHERE release = '' AND debug_id IN ('` + injectChunkID + `', '` + injectMainID + `')`); n != 2 {
		t.Errorf("mapas por debug ID = %d", n)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.upload_chunks`); n != 2 {
		t.Errorf("hay %d trozos; quiero los 2, hasta que caduquen", n)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.artifact_bundles`); n != 2 {
		t.Errorf("bundles = %d", n)
	}

	for _, name := range []string{"web-cli-upload-1", "web-cli-inject-1"} {
		ev := readEvent(t, name)
		if err := s.Enrich(ctx, p.ID, ev); err != nil {
			t.Fatal(err)
		}
		if top := topFrame(ev); top.Filename != carrito || top.Line != 7 || top.ContextLine != failLine {
			t.Errorf("%s: primer fotograma = %s:%d %q", name, top.Filename, top.Line, top.ContextLine)
		}
	}

	// Otra subida sustituye el mapa por nombre; repetir la del bundle lo
	// repone y renueva su fecha para la retención.
	if err := st.PutSourceMaps(ctx, p.ID, "prueba-upload", []store.SourceMapFile{{File: chunkJS, Map: []byte(`{"version":3,"sources":[],"mappings":""}`)}}); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `UPDATE `+schema+`.sourcemaps SET uploaded_at = now() - interval '10 days'`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(testdata, "sentry-cli", "upload", "002.body"))
	if err != nil {
		t.Fatal(err)
	}
	rec := serve(t, s, http.MethodPost, "/api/0/organizations/o/artifactbundle/assemble/", "Bearer "+token, "application/json", body)
	if !strings.Contains(rec.Body.String(), `"state":"ok"`) {
		t.Errorf("repetido: %d %s", rec.Code, rec.Body)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.sourcemaps WHERE release = 'prueba-upload' AND uploaded_at > now() - interval '1 minute'`); n != 2 {
		t.Errorf("%d mapas renovados, quiero 2", n)
	}
	ev := readEvent(t, "web-cli-upload-1")
	if err := s.Enrich(ctx, p.ID, ev); err != nil {
		t.Fatal(err)
	}
	if top := topFrame(ev); top.Filename != carrito || top.Line != 7 {
		t.Errorf("tras repetir: primer fotograma = %s:%d", top.Filename, top.Line)
	}

	// Retención: los mapas y bundles de hace más de 90 días y los trozos sin ensamblar de más de un día.
	if err := st.PutUploadChunks(ctx, map[string][]byte{sha1Hex([]byte("viejo")): []byte("viejo"), sha1Hex([]byte("nuevo")): []byte("nuevo")}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`UPDATE ` + schema + `.sourcemaps SET uploaded_at = now() - interval '91 days' WHERE release = 'prueba-upload' OR debug_id = '` + injectChunkID + `'`,
		`UPDATE ` + schema + `.artifact_bundles SET assembled_at = now() - interval '91 days' WHERE release = 'prueba-upload'`,
		`UPDATE ` + schema + `.upload_chunks SET uploaded_at = now() - interval '25 hours' WHERE sha1 <> '` + sha1Hex([]byte("nuevo")) + `'`,
	} {
		if _, err := admin.Exec(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	deleted, err := s.Purge(ctx, 90*24*time.Hour)
	if err != nil || deleted != 3 {
		t.Fatalf("borrados = %d, %v", deleted, err)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.sourcemaps`); n != 1 {
		t.Errorf("quedan %d mapas, quiero el de main por debug ID", n)
	}
	if n := count(`SELECT count(*) FROM ` + schema + `.artifact_bundles`); n != 1 {
		t.Errorf("quedan %d bundles", n)
	}
	missing, err := st.MissingUploadChunks(ctx, []string{sha1Hex([]byte("viejo")), sha1Hex([]byte("nuevo"))})
	if err != nil || len(missing) != 1 || missing[0] != sha1Hex([]byte("viejo")) {
		t.Errorf("trozos que faltan tras la retención = %v, %v", missing, err)
	}
	// Tras la retención, el bundle borrado vuelve a pedir su trozo.
	rec = serve(t, s, http.MethodPost, "/api/0/organizations/o/artifactbundle/assemble/", "Bearer "+token, "application/json", body)
	if !strings.Contains(rec.Body.String(), `"state":"not_found"`) {
		t.Errorf("tras la retención: %d %s", rec.Code, rec.Body)
	}
	ev = readEvent(t, "web-cli-upload-1")
	if err := s.Enrich(ctx, p.ID, ev); err != nil {
		t.Fatal(err)
	}
	if topFrame(ev).Minified != nil {
		t.Error("se tradujo con un mapa caducado")
	}
}
