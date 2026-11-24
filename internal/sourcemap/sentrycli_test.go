package sourcemap

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/intake-es/mothlog/internal/event"
)

// Debug IDs que `sourcemaps inject` dio a los .js de testdata/sentry-cli/inject.
const (
	injectChunkID = "174ab4b5-75a1-56fb-be73-185871cd4267"
	injectMainID  = "5cb45e20-41a8-5fea-a9ea-8edaf705a1dc"
)

type recorded struct {
	Method   string            `json:"method"`
	Path     string            `json:"path"`
	Headers  map[string]string `json:"headers"`
	Status   int               `json:"status"`
	Response string            `json:"response"`
}

func serve(t *testing.T, s *Service, method, url, auth, ctype string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	req := httptest.NewRequest(method, url, bytes.NewReader(body))
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("respuesta no es JSON: %q", got)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(g, w)
}

// replay manda en orden las peticiones que hizo sentry-cli 3.8.2 en
// testdata/sentry-cli/<dir> y comprueba que las respuestas son las grabadas.
func replay(t *testing.T, s *Service, dir, auth string) {
	t.Helper()
	metas, err := filepath.Glob(filepath.Join(testdata, "sentry-cli", dir, "*.meta.json"))
	if err != nil || len(metas) == 0 {
		t.Fatalf("sin grabaciones en %s: %v", dir, err)
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
		rec := serve(t, s, r.Method, r.Path, auth, r.Headers["content-type"], body)
		if rec.Code != r.Status || !sameJSON(t, rec.Body.String(), r.Response) {
			t.Fatalf("%s %s %s = %d %s; grabado %d %s", filepath.Base(m), r.Method, r.Path, rec.Code, rec.Body, r.Status, r.Response)
		}
	}
}

func topFrame(ev *event.Event) event.Frame {
	frames := ev.Exceptions[len(ev.Exceptions)-1].Frames
	return frames[len(frames)-1]
}

func TestSentryCLIUploadByName(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	replay(t, s, "upload", "Bearer "+token)

	ev := readEvent(t, "web-cli-upload-1")
	if len(ev.DebugMeta) != 0 {
		t.Fatal("este envío no debería traer debug_meta")
	}
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if top := topFrame(ev); top.Filename != carrito || top.Line != 7 || top.ContextLine != failLine {
		t.Fatalf("primer fotograma = %s:%d %q", top.Filename, top.Line, top.ContextLine)
	}
	if f := findFrame(t, ev, "main-L4E5YTBB.js", 31885); f.Filename != "src/main.ts" || f.Line != 11 {
		t.Errorf("main = %s:%d", f.Filename, f.Line)
	}
	if _, ok := st.maps[key{project: webID, release: "prueba-upload", file: chunkJS}]; !ok {
		t.Error("el mapa no quedó guardado por release y nombre")
	}

	// Con otro release no hay mapa por nombre.
	ev = readEvent(t, "web-cli-upload-1")
	ev.Release = "otro"
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if topFrame(ev).Minified != nil {
		t.Error("se tradujo con el mapa de otro release")
	}
}

func TestSentryCLIUploadByDebugID(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	// Con un token de la API también vale.
	replay(t, s, "inject", "Bearer "+apiToken)

	for _, id := range []string{injectChunkID, injectMainID} {
		if _, ok := st.maps[key{project: webID, debugID: id}]; !ok {
			t.Errorf("falta el mapa del debug ID %s", id)
		}
	}

	ev := readEvent(t, "web-cli-inject-1")
	if ids := debugIDs(ev.DebugMeta); ids["https://app.example.com/"+chunkJS] != injectChunkID {
		t.Fatalf("debug IDs del evento = %v", ids)
	}
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	top := topFrame(ev)
	if top.Filename != carrito || top.Line != 7 || top.Column != 25 || top.ContextLine != failLine {
		t.Fatalf("primer fotograma = %s:%d:%d %q", top.Filename, top.Line, top.Column, top.ContextLine)
	}
	// inject añade dos líneas al principio del .js.
	if top.Minified == nil || top.Minified.Line != 3 || top.Minified.Column != 383 {
		t.Errorf("minified = %+v", top.Minified)
	}
	if f := findFrame(t, ev, "main-V7FHULKG.js", 31885); f.Filename != "src/main.ts" || f.Line != 11 {
		t.Errorf("main = %s:%d", f.Filename, f.Line)
	}

	// Subido sin release, solo se encuentra por debug ID: sin debug_meta se queda igual.
	ev = readEvent(t, "web-cli-inject-1")
	ev.DebugMeta = nil
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if topFrame(ev).Minified != nil {
		t.Error("sin debug_meta no hay mapa por nombre para este release")
	}
}

func TestEnrichPrefersDebugIDAndFallsBackToName(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	replay(t, s, "upload", "Bearer "+token)

	// Un debug ID que no está subido no impide la búsqueda por nombre.
	ev := readEvent(t, "web-cli-upload-1")
	ev.DebugMeta = json.RawMessage(`{"images":[{"type":"sourcemap","code_file":"https://app.example.com/` + chunkJS + `","debug_id":"00000000-0000-0000-0000-000000000001"}]}`)
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if top := topFrame(ev); top.Filename != carrito || top.Line != 7 {
		t.Fatalf("primer fotograma = %s:%d", top.Filename, top.Line)
	}
}

func TestDebugIDs(t *testing.T) {
	got := debugIDs([]byte(`{"images":[
		{"type":"sourcemap","code_file":"https://a/x.js","debug_id":"174AB4B575A156FBBE73185871CD4267"},
		{"type":"macho","code_file":"https://a/y.js","debug_id":"174ab4b5-75a1-56fb-be73-185871cd4267"},
		{"type":"sourcemap","code_file":"https://a/z.js","debug_id":"no-es-un-uuid"},
		{"type":"sourcemap","code_file":7,"debug_id":"174ab4b5-75a1-56fb-be73-185871cd4267"}
	]}`))
	want := map[string]string{"https://a/x.js": injectChunkID}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("debugIDs = %v", got)
	}
	if debugIDs([]byte(`{"images":"x"}`)) != nil || debugIDs(nil) != nil {
		t.Error("debug_meta raro debería dar nil")
	}
}

func TestSentryCLIAuth(t *testing.T) {
	s := New(newMemStore(), quiet(), Options{UploadToken: token})
	url := "/api/0/organizations/o/chunk-upload/"
	for _, auth := range []string{"", "Bearer ", "Bearer otro", "Bearer ie_otro", "Basic " + token} {
		if rec := serve(t, s, http.MethodGet, url, auth, "", nil); rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), "invalid token") {
			t.Errorf("%q: %d %s", auth, rec.Code, rec.Body)
		}
	}
	for _, auth := range []string{"Bearer " + token, "Bearer " + apiToken, "bearer " + apiToken} {
		if rec := serve(t, s, http.MethodGet, url, auth, "", nil); rec.Code != http.StatusOK {
			t.Errorf("%q: %d %s", auth, rec.Code, rec.Body)
		}
	}
	// Sin SOURCEMAP_UPLOAD_TOKEN solo valen los de la API.
	s = New(newMemStore(), quiet(), Options{})
	if rec := serve(t, s, http.MethodGet, url, "Bearer "+apiToken, "", nil); rec.Code != http.StatusOK {
		t.Errorf("token de la API sin token de subida: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodPost, "/api/0/organizations/o/artifactbundle/assemble/", "Bearer x", "application/json", []byte(`{}`)); rec.Code != http.StatusUnauthorized {
		t.Errorf("ensamblar con token malo: %d", rec.Code)
	}
}

type bundleFile struct {
	name string
	body []byte
}

func makeZip(t *testing.T, files ...bundleFile) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		w, err := zw.Create(f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sha1Hex(b []byte) string {
	s := sha1.Sum(b)
	return hex.EncodeToString(s[:])
}

// uploadChunks sube los trozos como sentry-cli: en gzip, con su sha1 de nombre.
func uploadChunks(t *testing.T, s *Service, chunks ...[]byte) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, c := range chunks {
		w, err := mw.CreateFormFile("file_gzip", sha1Hex(c))
		if err != nil {
			t.Fatal(err)
		}
		zw := gzip.NewWriter(w)
		if _, err := zw.Write(c); err != nil {
			t.Fatal(err)
		}
		if err := zw.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return serve(t, s, http.MethodPost, "/api/0/organizations/o/chunk-upload/", "Bearer "+token, mw.FormDataContentType(), buf.Bytes())
}

type assembled struct {
	State         string   `json:"state"`
	MissingChunks []string `json:"missingChunks"`
	Detail        *string  `json:"detail"`
}

func assemble(t *testing.T, s *Service, req map[string]any) (int, assembled) {
	t.Helper()
	b, _ := json.Marshal(req)
	rec := serve(t, s, http.MethodPost, "/api/0/organizations/o/artifactbundle/assemble/", "Bearer "+token, "application/json", b)
	var res assembled
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
			t.Fatal(err)
		}
	} else {
		res.Detail = new(string)
		var e struct{ Detail string }
		_ = json.Unmarshal(rec.Body.Bytes(), &e)
		*res.Detail = e.Detail
	}
	return rec.Code, res
}

// assembleBundle sube el zip en trozos de size bytes y lo ensambla.
func assembleBundle(t *testing.T, s *Service, z []byte, size int, extra map[string]any) (int, assembled) {
	t.Helper()
	var chunks [][]byte
	var sums []string
	for len(z) > 0 {
		n := min(size, len(z))
		chunks = append(chunks, z[:n])
		sums = append(sums, sha1Hex(z[:n]))
		z = z[n:]
	}
	if rec := uploadChunks(t, s, chunks...); rec.Code != http.StatusOK {
		t.Fatalf("subir trozos: %d %s", rec.Code, rec.Body)
	}
	req := map[string]any{"checksum": sha1Hex(bytes.Join(chunks, nil)), "chunks": sums, "projects": []string{"web"}}
	for k, v := range extra {
		req[k] = v
	}
	return assemble(t, s, req)
}

const manifestOK = `{"files":{
	"files/_/_/` + chunkJS + `":{"type":"minified_source","url":"~/assets/` + chunkJS + `","headers":{"Sourcemap":"` + chunkJS + `.map"}},
	"files/_/_/` + chunkJS + `.map":{"type":"source_map","url":"~/assets/` + chunkJS + `.map"}},
	"release":"del-manifest","dist":"d1"}`

func TestAssembleInSeveralChunks(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	m := mapFile(t, chunkJS+".map").body
	z := makeZip(t, bundleFile{"manifest.json", []byte(manifestOK)}, bundleFile{"files/_/_/" + chunkJS, []byte("x")}, bundleFile{"files/_/_/" + chunkJS + ".map", m})
	code, res := assembleBundle(t, s, z, 1000, nil)
	if code != http.StatusOK || res.State != "ok" {
		t.Fatalf("ensamblar: %d %+v", code, res)
	}
	// Release y dist del manifest, porque la petición no los trae.
	if _, ok := st.maps[key{project: webID, release: "del-manifest", dist: "d1", file: chunkJS}]; !ok {
		t.Fatalf("mapas = %v", st.maps)
	}
	// Los trozos se quedan hasta caducar: otra subida puede estar usándolos.
	if len(st.chunks) != 3 {
		t.Errorf("quedan %d trozos tras ensamblar, quiero los 3", len(st.chunks))
	}
	ev := readEvent(t, "web-typeerror-1")
	ev.Release, ev.Dist = "del-manifest", "d1"
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if top := topFrame(ev); top.Filename != carrito || top.Line != 7 {
		t.Fatalf("primer fotograma = %s:%d", top.Filename, top.Line)
	}

	// Repetido con los trozos aún guardados, vuelve a guardar los mapas.
	sums := []string{sha1Hex(z[:1000]), sha1Hex(z[1000:2000]), sha1Hex(z[2000:])}
	delete(st.maps, key{project: webID, release: "del-manifest", dist: "d1", file: chunkJS})
	code, res = assemble(t, s, map[string]any{"checksum": sha1Hex(z), "chunks": sums, "projects": []string{"web"}})
	if code != http.StatusOK || res.State != "ok" {
		t.Errorf("repetido: %d %+v", code, res)
	}
	if _, ok := st.maps[key{project: webID, release: "del-manifest", dist: "d1", file: chunkJS}]; !ok {
		t.Error("repetir la subida no repuso el mapa")
	}
	// Con los trozos caducados, un bundle ya guardado no los pide.
	clear(st.chunks)
	code, res = assemble(t, s, map[string]any{"checksum": sha1Hex(z), "chunks": sums, "projects": []string{"web"}})
	if code != http.StatusOK || res.State != "ok" {
		t.Errorf("repetido sin trozos: %d %+v", code, res)
	}
}

// Dos subidas del mismo build a la vez comparten trozos: la primera en
// ensamblar no se los quita a la segunda.
func TestAssembleSharedChunks(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	m := mapFile(t, chunkJS+".map").body
	z := makeZip(t, bundleFile{"manifest.json", []byte(manifestOK)}, bundleFile{"files/_/_/" + chunkJS + ".map", m})
	req := map[string]any{"checksum": sha1Hex(z), "chunks": []string{sha1Hex(z)}, "projects": []string{"web"}, "version": "r1"}
	if code, res := assemble(t, s, req); code != http.StatusOK || res.State != "not_found" {
		t.Fatalf("primera: %d %+v", code, res)
	}
	if code, res := assemble(t, s, req); code != http.StatusOK || res.State != "not_found" {
		t.Fatalf("segunda: %d %+v", code, res)
	}
	if rec := uploadChunks(t, s, z); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	req2 := map[string]any{"checksum": sha1Hex(z), "chunks": []string{sha1Hex(z)}, "projects": []string{"web"}, "version": "r2"}
	for _, r := range []map[string]any{req, req2} {
		if code, res := assemble(t, s, r); code != http.StatusOK || res.State != "ok" {
			t.Errorf("%v: %d %+v", r["version"], code, res)
		}
	}
	for _, rel := range []string{"r1", "r2"} {
		if _, ok := st.maps[key{project: webID, release: rel, dist: "d1", file: chunkJS}]; !ok {
			t.Errorf("falta el mapa de %s", rel)
		}
	}
}

// Si un trozo caduca entre que se miran y se leen, se vuelve a pedir.
func TestAssembleChunkGoneWhileReading(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	a, b := []byte("trozo a"), []byte("trozo b")
	if rec := uploadChunks(t, s, a, b); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	st.vanish = map[string]bool{sha1Hex(b): true}
	code, res := assemble(t, s, map[string]any{
		"checksum": sha1Hex(append(append([]byte{}, a...), b...)),
		"chunks":   []string{sha1Hex(a), sha1Hex(b)},
		"projects": []string{"web"},
	})
	if code != http.StatusOK || res.State != "not_found" || !reflect.DeepEqual(res.MissingChunks, []string{sha1Hex(b)}) {
		t.Fatalf("= %d %+v", code, res)
	}
}

func TestAssembleTotalSize(t *testing.T) {
	st := newMemStore()
	m := mapFile(t, chunkJS+".map").body
	s := New(st, quiet(), Options{UploadToken: token, MaxBundleBytes: int64(len(m)) * 3 / 2})
	manifest := `{"files":{
		"files/a.js.map":{"type":"source_map","url":"~/a.js.map"},
		"files/b.js.map":{"type":"source_map","url":"~/b.js.map"}}}`
	z := makeZip(t, bundleFile{"manifest.json", []byte(manifest)}, bundleFile{"files/a.js.map", m}, bundleFile{"files/b.js.map", m})
	if int64(len(z)) > s.opts.MaxBundleBytes {
		t.Fatalf("el zip (%d) ya pasa del tope", len(z))
	}
	code, res := assembleBundle(t, s, z, 1<<20, nil)
	if code != http.StatusOK || res.State != "error" || res.Detail == nil || !strings.Contains(*res.Detail, "uncompressed") {
		t.Fatalf("= %d %+v", code, res)
	}
	if len(st.maps) != 0 {
		t.Errorf("se guardaron %d mapas", len(st.maps))
	}
}

func TestAssembleErrors(t *testing.T) {
	m := mapFile(t, chunkJS+".map").body
	for _, tc := range []struct {
		name   string
		zip    func(t *testing.T) []byte
		detail string
	}{
		{"no es un zip", func(*testing.T) []byte { return []byte("esto no es un zip") }, "the bundle is not a valid zip"},
		{"sin manifest", func(t *testing.T) []byte {
			return makeZip(t, bundleFile{"files/_/_/a.js.map", m})
		}, "the bundle has no manifest.json"},
		{"manifest roto", func(t *testing.T) []byte {
			return makeZip(t, bundleFile{"manifest.json", []byte("{")})
		}, "manifest.json is not valid"},
		{"mapa roto", func(t *testing.T) []byte {
			return makeZip(t, bundleFile{"manifest.json", []byte(manifestOK)}, bundleFile{"files/_/_/" + chunkJS + ".map", []byte(`{"version":3}`)})
		}, "~/assets/" + chunkJS + ".map is not a valid v3 source map: missing mappings"},
		{"mapa que no está", func(t *testing.T) []byte {
			return makeZip(t, bundleFile{"manifest.json", []byte(manifestOK)})
		}, "manifest.json lists files/_/_/" + chunkJS + ".map, which is not in the bundle"},
		{"sin mapas", func(t *testing.T) []byte {
			return makeZip(t, bundleFile{"manifest.json", []byte(`{"files":{}}`)})
		}, "the bundle has no source maps"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			st := newMemStore()
			s := New(st, quiet(), Options{UploadToken: token})
			code, res := assembleBundle(t, s, tc.zip(t), 1<<20, nil)
			if code != http.StatusOK || res.State != "error" || res.Detail == nil || !strings.HasPrefix(*res.Detail, tc.detail) {
				t.Fatalf("= %d %+v; quiero error %q", code, res, tc.detail)
			}
			if len(st.maps) != 0 {
				t.Errorf("se guardaron %d mapas", len(st.maps))
			}
		})
	}
}

func TestAssembleMissingChunk(t *testing.T) {
	s := New(newMemStore(), quiet(), Options{UploadToken: token})
	a, b := []byte("trozo a"), []byte("trozo b")
	if rec := uploadChunks(t, s, a); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	code, res := assemble(t, s, map[string]any{
		"checksum": sha1Hex(append(append([]byte{}, a...), b...)),
		"chunks":   []string{sha1Hex(a), sha1Hex(b)},
		"projects": []string{"web"},
	})
	if code != http.StatusOK || res.State != "not_found" || !reflect.DeepEqual(res.MissingChunks, []string{sha1Hex(b)}) ||
		res.Detail == nil || *res.Detail != "missing 1 of 2 chunks" {
		t.Fatalf("= %d %+v", code, res)
	}

	// Trozos que no forman el checksum.
	if rec := uploadChunks(t, s, b); rec.Code != http.StatusOK {
		t.Fatal(rec.Body)
	}
	code, res = assemble(t, s, map[string]any{"checksum": sha1Hex([]byte("otra cosa")), "chunks": []string{sha1Hex(a), sha1Hex(b)}, "projects": []string{"web"}})
	if code != http.StatusOK || res.State != "error" || res.Detail == nil || !strings.Contains(*res.Detail, "don't make up the bundle") {
		t.Fatalf("checksum: %d %+v", code, res)
	}
}

func TestAssembleBadRequests(t *testing.T) {
	s := New(newMemStore(), quiet(), Options{UploadToken: token})
	sum := sha1Hex([]byte("x"))
	for _, tc := range []struct {
		name string
		req  map[string]any
		code int
		msg  string
	}{
		{"checksum", map[string]any{"checksum": "x", "chunks": []string{sum}, "projects": []string{"web"}}, 400, "checksum is not a sha1"},
		{"sin trozos", map[string]any{"checksum": sum, "projects": []string{"web"}}, 400, "missing chunks"},
		{"sin proyecto", map[string]any{"checksum": sum, "chunks": []string{sum}}, 400, "missing project"},
		{"proyecto que no existe", map[string]any{"checksum": sum, "chunks": []string{sum}, "projects": []string{"otro"}}, 404, `project "otro" not found`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, res := assemble(t, s, tc.req)
			if code != tc.code || res.Detail == nil || !strings.Contains(*res.Detail, tc.msg) {
				t.Errorf("= %d %+v; quiero %d %q", code, res, tc.code, tc.msg)
			}
		})
	}
}

func TestChunkUploadErrors(t *testing.T) {
	s := New(newMemStore(), quiet(), Options{UploadToken: token})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	w, _ := mw.CreateFormFile("file", sha1Hex([]byte("otro contenido")))
	_, _ = w.Write([]byte("contenido"))
	_ = mw.Close()
	rec := serve(t, s, http.MethodPost, "/api/0/organizations/o/chunk-upload/", "Bearer "+token, mw.FormDataContentType(), buf.Bytes())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "does not match its sha1") {
		t.Errorf("sha1 que no coincide: %d %s", rec.Code, rec.Body)
	}

	buf.Reset()
	mw = multipart.NewWriter(&buf)
	w, _ = mw.CreateFormFile("file_gzip", sha1Hex([]byte("x")))
	_, _ = w.Write([]byte("x"))
	_ = mw.Close()
	rec = serve(t, s, http.MethodPost, "/api/0/organizations/o/chunk-upload/", "Bearer "+token, mw.FormDataContentType(), buf.Bytes())
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not gzip") {
		t.Errorf("gzip roto: %d %s", rec.Code, rec.Body)
	}
}

// Lo que manda sentry-cli 1.x: crea el release, lista sus ficheros y sube cada uno.
func TestLegacyReleaseFiles(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	auth := "Bearer " + token
	base := "/api/0/projects/o/web/releases/"

	rec := serve(t, s, http.MethodPost, base, auth, "application/json", []byte(`{"version":"`+release1+`","projects":["web"]}`))
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"dateCreated"`) {
		t.Fatalf("crear release: %d %s", rec.Code, rec.Body)
	}
	if rec := serve(t, s, http.MethodPost, "/api/0/projects/o/otro/releases/", auth, "application/json", []byte(`{"version":"x"}`)); rec.Code != http.StatusNotFound {
		t.Errorf("proyecto que no existe: %d", rec.Code)
	}
	if rec := serve(t, s, http.MethodGet, base+release1+"/files/?cursor=", auth, "", nil); rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != "[]" {
		t.Errorf("listar: %d %s", rec.Code, rec.Body)
	}

	send := func(filename, name string, body []byte, header string) *httptest.ResponseRecorder {
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		w, _ := mw.CreateFormFile("file", filename)
		_, _ = w.Write(body)
		_ = mw.WriteField("name", name)
		if header != "" {
			_ = mw.WriteField("header", header)
		}
		_ = mw.Close()
		return serve(t, s, http.MethodPost, base+release1+"/files/", auth, mw.FormDataContentType(), buf.Bytes())
	}
	if rec := send(chunkJS, "~/"+chunkJS, []byte("js"), "Sourcemap:"+chunkJS+".map"); rec.Code != http.StatusCreated {
		t.Fatalf("subir .js: %d %s", rec.Code, rec.Body)
	}
	if rec := send(chunkJS+".map", "~/"+chunkJS+".map", []byte(`{"version":3}`), ""); rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "is not a valid v3 source map") {
		t.Errorf("mapa roto: %d %s", rec.Code, rec.Body)
	}
	if rec := send(chunkJS+".map", "~/"+chunkJS+".map", mapFile(t, chunkJS+".map").body, ""); rec.Code != http.StatusCreated {
		t.Fatalf("subir .map: %d %s", rec.Code, rec.Body)
	}
	if len(st.maps) != 1 {
		t.Fatalf("mapas guardados = %d; solo el .map", len(st.maps))
	}

	ev := readEvent(t, "web-typeerror-1")
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	if top := topFrame(ev); top.Filename != carrito || top.Line != 7 {
		t.Fatalf("primer fotograma = %s:%d", top.Filename, top.Line)
	}
}

func TestResolveURL(t *testing.T) {
	for _, tc := range [][3]string{
		{"~/a.js", "a.js.map", "~/a.js.map"},
		{"~/assets/a.js", "a.js.map", "~/assets/a.js.map"},
		{"~/assets/a.js", "../maps/a.js.map", "~/maps/a.js.map"},
		{"https://h/x/a.js", "a.js.map", "https://h/x/a.js.map"},
		{"~/a.js", "~/m/a.js.map", "~/m/a.js.map"},
	} {
		if got := resolveURL(tc[0], tc[1]); got != tc[2] {
			t.Errorf("resolveURL(%q, %q) = %q, quiero %q", tc[0], tc[1], got, tc[2])
		}
	}
}
