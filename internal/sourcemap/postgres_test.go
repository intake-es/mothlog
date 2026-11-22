package sourcemap

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
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

// TestPostgresSentryCLIReleases repite lo que mandaron releases new, releases
// finalize y deploys new de sentry-cli 3.8.2 (testdata/sentry-cli/releases):
// las fechas de las respuestas cambian, así que solo se comparan sus campos.
func TestPostgresSentryCLIReleases(t *testing.T) {
	st, admin, schema := pgStore(t)
	ctx := context.Background()
	p, err := st.CreateProject(ctx, "front", "7438a4393be53fabe513ce19bec380e9")
	if err != nil {
		t.Fatal(err)
	}
	// Un error de la release anterior, marcado para la siguiente.
	var issue int64
	if err := admin.QueryRow(ctx, `INSERT INTO `+schema+`.issues (project_id, fingerprint, title, first_seen, last_seen, event_count, first_release)
		VALUES ($1, 'fp', 'TypeError', now(), now(), 1, 'prueba-upload') RETURNING id`, p.ID).Scan(&issue); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, `INSERT INTO `+schema+`.events (issue_id, project_id, event_id, occurred_at, release, environment, data)
		VALUES ($1, $2, gen_random_uuid(), now(), 'prueba-upload', 'production', '{}')`, issue, p.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.EnsureRelease(ctx, st.Pool(), p.ID, "prueba-upload", time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetIssueStatus(ctx, issue, "resolved_next"); err != nil {
		t.Fatal(err)
	}
	s := New(st, quiet(), Options{UploadToken: token})

	metas, err := filepath.Glob(filepath.Join(testdata, "sentry-cli", "releases", "*.meta.json"))
	if err != nil || len(metas) != 3 {
		t.Fatalf("grabaciones = %v, %v", metas, err)
	}
	for _, m := range metas {
		var r recorded
		b, err := os.ReadFile(m)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		body, err := os.ReadFile(strings.TrimSuffix(m, ".meta.json") + ".body")
		if err != nil {
			t.Fatal(err)
		}
		rec := serve(t, s, r.Method, r.Path, "Bearer "+token, r.Headers["content-type"], body)
		var got, want map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatalf("%s: %s", filepath.Base(m), rec.Body)
		}
		if err := json.Unmarshal([]byte(r.Response), &want); err != nil {
			t.Fatal(err)
		}
		if rec.Code != r.Status || len(got) != len(want) {
			t.Fatalf("%s %s %s = %d %s; grabado %d %s", filepath.Base(m), r.Method, r.Path, rec.Code, rec.Body, r.Status, r.Response)
		}
		for k, w := range want {
			g, ok := got[k]
			switch {
			case !ok:
				t.Errorf("%s: falta %s", filepath.Base(m), k)
			case strings.HasPrefix(k, "date") || k == "id":
				if (g == nil) != (w == nil) {
					t.Errorf("%s: %s = %v; grabado %v", filepath.Base(m), k, g, w)
				}
			case !reflect.DeepEqual(g, w):
				t.Errorf("%s: %s = %v; grabado %v", filepath.Base(m), k, g, w)
			}
		}
	}

	rel, err := st.Release(ctx, p.ID, "prueba-releases", "")
	if err != nil {
		t.Fatal(err)
	}
	if rel.FinalizedAt == nil || len(rel.Deploys) != 1 || rel.Deploys[0].Environment != "production" {
		t.Errorf("release = %+v", rel)
	}
	got, err := st.Issue(ctx, issue)
	if err != nil || got.Status != "resolved" || got.FixedInRelease == nil || *got.FixedInRelease != "prueba-releases" {
		t.Errorf("el error marcado para la siguiente = %+v, %v", got, err)
	}

	// Un despliegue de una release que no existe en ningún proyecto.
	rec := serve(t, s, http.MethodPost, "/api/0/organizations/o/releases/nada/deploys/", "Bearer "+token, "application/json", []byte(`{"environment":"production"}`))
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "detail") {
		t.Errorf("release que no existe: %d %s", rec.Code, rec.Body)
	}
	rec = serve(t, s, http.MethodPost, "/api/0/organizations/o/releases/prueba-releases/deploys/", "Bearer no-vale", "application/json", []byte(`{"environment":"production"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("token malo: %d", rec.Code)
	}
}
