package sourcemap

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

const (
	testdata = "../../testdata"
	token    = "token-de-prueba"
	webID    = int64(2)
	release1 = "fixture-release-1"
	chunkJS  = "chunk-Z554BZQ6.js"
	mainJS   = "main-3TO43VUM.js"
	carrito  = "src/carrito.ts"
	// La línea 7 de src/carrito.ts, donde salta el TypeError.
	failLine = "  return pedido.lineas!.reduce((total, linea) => total + linea.cantidad, 0);"
)

type stored struct {
	b  []byte
	at time.Time
}

// memStore guarda en memoria; los proyectos "web" y "front" (el de
// testdata/sentry-cli) son webID y el token de la API que vale es apiToken.
type memStore struct {
	mu      sync.Mutex
	maps    map[key]stored
	reads   int
	chunks  map[string][]byte
	bundles map[string]bool
	// vanish son trozos que UploadChunk ya no encuentra, aunque MissingUploadChunks sí.
	vanish map[string]bool
}

const apiToken = "ie_token-de-la-api"

func newMemStore() *memStore {
	return &memStore{maps: map[key]stored{}, chunks: map[string][]byte{}, bundles: map[string]bool{}}
}

func (m *memStore) PutSourceMaps(_ context.Context, projectID int64, release string, files []store.SourceMapFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range files {
		now := stored{f.Map, time.Now()}
		m.maps[key{project: projectID, release: release, dist: f.Dist, file: f.File}] = now
		if f.DebugID != "" {
			m.maps[key{project: projectID, debugID: f.DebugID}] = now
		}
	}
	return nil
}

func (m *memStore) SourceMap(_ context.Context, projectID int64, release, dist, file string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	for _, d := range []string{dist, ""} {
		if s, ok := m.maps[key{project: projectID, release: release, dist: d, file: file}]; ok {
			return s.b, nil
		}
	}
	return nil, store.ErrNotFound
}

func (m *memStore) SourceMapByDebugID(_ context.Context, projectID int64, debugID string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	if s, ok := m.maps[key{project: projectID, debugID: debugID}]; ok {
		return s.b, nil
	}
	return nil, store.ErrNotFound
}

func (m *memStore) DeleteSourceMapsBefore(_ context.Context, t time.Time) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, s := range m.maps {
		if s.at.Before(t) {
			delete(m.maps, k)
			n++
		}
	}
	return n, nil
}

func (m *memStore) ProjectByName(_ context.Context, name string) (store.Project, error) {
	if name != "web" && name != "front" {
		return store.Project{}, store.ErrNotFound
	}
	return store.Project{ID: webID, Name: name}, nil
}

func (m *memStore) Authenticate(_ context.Context, secret string) (store.Token, error) {
	if secret != apiToken {
		return store.Token{}, store.ErrNotFound
	}
	return store.Token{Name: "api"}, nil
}

func (m *memStore) PutUploadChunks(_ context.Context, chunks map[string][]byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, v := range chunks {
		m.chunks[k] = v
	}
	return nil
}

func (m *memStore) MissingUploadChunks(_ context.Context, sums []string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	missing := []string{}
	for _, s := range sums {
		if _, ok := m.chunks[s]; !ok && !slices.Contains(missing, s) {
			missing = append(missing, s)
		}
	}
	return missing, nil
}

func (m *memStore) UploadChunk(_ context.Context, sum string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	c, ok := m.chunks[sum]
	if !ok || m.vanish[sum] {
		return nil, store.ErrNotFound
	}
	return c, nil
}

func (m *memStore) DeleteUploadChunksBefore(context.Context, time.Time) (int64, error) { return 0, nil }

func (m *memStore) BundleAssembled(_ context.Context, checksum string, projectIDs []int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.bundles[checksum], nil
}

func (m *memStore) PutBundle(ctx context.Context, b store.Bundle) error {
	for _, p := range b.ProjectIDs {
		if err := m.PutSourceMaps(ctx, p, b.Release, b.Files); err != nil {
			return err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.bundles[b.Checksum] = true
	return nil
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func readEvent(t *testing.T, name string) *event.Event {
	t.Helper()
	body, err := os.ReadFile(filepath.Join(testdata, "envelopes", name+".envelope"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := envelope.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	ev, err := event.Parse(env.Events()[0].Payload)
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

type upload struct {
	field, name string
	body        []byte
}

func mapFile(t *testing.T, rel string) upload {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(testdata, "sourcemaps", rel))
	if err != nil {
		t.Fatal(err)
	}
	return upload{"file", filepath.Base(rel), b}
}

func multipartBody(t *testing.T, files ...upload) (*bytes.Buffer, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for _, f := range files {
		w, err := mw.CreateFormFile(f.field, f.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(f.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &buf, mw.FormDataContentType()
}

func post(t *testing.T, s *Service, url, auth string, body io.Reader, ctype string) *httptest.ResponseRecorder {
	t.Helper()
	mux := http.NewServeMux()
	s.Register(mux)
	req := httptest.NewRequest(http.MethodPost, url, body)
	req.Header.Set("Content-Type", ctype)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func uploadURL(project int64, release string) string {
	return "/api/" + jsonInt(project) + "/releases/" + release + "/sourcemaps/"
}

func jsonInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func uploadOK(t *testing.T, s *Service, project int64, release string, files ...upload) {
	t.Helper()
	body, ct := multipartBody(t, files...)
	rec := post(t, s, uploadURL(project, release), "Bearer "+token, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("subida: %d %s", rec.Code, rec.Body)
	}
}

// webService tiene subido el mapa del chunk de los pedidos, no el de main.
func webService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	uploadOK(t, s, webID, release1, mapFile(t, chunkJS+".map"))
	return s, st
}

func findFrame(t *testing.T, ev *event.Event, file string, col int) event.Frame {
	t.Helper()
	for _, f := range ev.Exceptions[len(ev.Exceptions)-1].Frames {
		if f.Minified != nil && strings.HasSuffix(f.Minified.Filename, "/"+file) && f.Minified.Column == col {
			return f
		}
	}
	t.Fatalf("no hay fotograma traducido de %s:%d", file, col)
	return event.Frame{}
}

func TestEnrichWeb(t *testing.T) {
	s, _ := webService(t)
	ev := readEvent(t, "web-typeerror-1")
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	frames := ev.Exceptions[len(ev.Exceptions)-1].Frames

	top := frames[len(frames)-1]
	if top.Filename != carrito || top.Line != 7 || top.Column != 25 {
		t.Fatalf("primer fotograma = %s:%d:%d", top.Filename, top.Line, top.Column)
	}
	if top.ContextLine != failLine {
		t.Errorf("context_line = %q", top.ContextLine)
	}
	if len(top.PreContext) != 5 || top.PreContext[3] != "function contarUnidades(pedido: Pedido): number {" || top.PreContext[4] != "  // Un pedido recién creado aún no tiene líneas." {
		t.Errorf("pre_context = %q", top.PreContext)
	}
	if len(top.PostContext) != 5 || top.PostContext[0] != "}" || top.PostContext[2] != "function pintarResumen(pedido: Pedido): void {" {
		t.Errorf("post_context = %q", top.PostContext)
	}
	if top.InApp == nil || !*top.InApp {
		t.Error("in_app debería ser verdadero")
	}
	if top.Function != "a" {
		t.Errorf("función = %q; debe quedar la del SDK", top.Function)
	}
	want := &event.Position{Filename: "https://app.example.com/" + chunkJS, Line: 1, Column: 383}
	if !reflect.DeepEqual(top.Minified, want) {
		t.Errorf("minified = %+v", top.Minified)
	}

	if f := findFrame(t, ev, chunkJS, 442); f.Filename != carrito || f.Line != 12 {
		t.Errorf("columna 442 = %s:%d", f.Filename, f.Line)
	}
	// Es casi el final del fichero: solo quedan dos líneas.
	if f := findFrame(t, ev, chunkJS, 492); f.Filename != carrito || f.Line != 19 || !reflect.DeepEqual(f.PostContext, []string{"}", ""}) {
		t.Errorf("columna 492 = %s:%d, post_context %q", f.Filename, f.Line, f.PostContext)
	}

	var mitt int
	for _, f := range frames {
		if f.Minified == nil || !strings.HasPrefix(f.Filename, "node_modules/") {
			continue
		}
		mitt++
		if f.Filename != "node_modules/mitt/src/index.ts" || f.ContextLine == "" {
			t.Errorf("%s:%d = %s %q", chunkJS, f.Minified.Column, f.Filename, f.ContextLine)
		}
		if f.InApp == nil || *f.InApp {
			t.Errorf("%s debería ser in_app falso", f.Filename)
		}
	}
	if mitt != 2 {
		t.Errorf("%d fotogramas de mitt traducidos, quiero 2", mitt)
	}

	// Sin mapa subido para estos: se quedan igual.
	var untouched int
	for _, f := range frames {
		if strings.HasSuffix(f.Filename, "/"+mainJS) || f.Filename == "<anonymous>" {
			untouched++
			if f.Minified != nil {
				t.Errorf("%s no debería cambiar", f.Filename)
			}
		}
	}
	if untouched != 3 {
		t.Errorf("%d fotogramas sin mapa, quiero 3", untouched)
	}
}

func TestEnrichMain(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	uploadOK(t, s, webID, "r-main", mapFile(t, mainJS+".map"))

	ev := readEvent(t, "web-typeerror-1")
	ev.Release = "r-main"
	if err := s.Enrich(context.Background(), webID, ev); err != nil {
		t.Fatal(err)
	}
	// Las mismas posiciones que da source-map 0.8.0 (tools/fixtures/navegador/posiciones.mjs).
	want := []struct {
		file   string
		line   int
		inApp  bool
		source string
	}{
		{"node_modules/@sentry/browser/src/helpers.ts", 157, false, ""},
		{"src/main.ts", 11, true, "  enviarPedido('p_1042');"},
	}
	var n int
	for _, f := range ev.Exceptions[0].Frames {
		if f.Minified == nil {
			if strings.HasSuffix(f.Filename, "/"+mainJS) {
				t.Errorf("sin traducir: %+v", f)
			}
			continue
		}
		if n < len(want) {
			w := want[n]
			if f.Filename != w.file || f.Line != w.line || f.InApp == nil || *f.InApp != w.inApp || f.ContextLine == "" {
				t.Errorf("fotograma %d = %s:%d in_app %v %q; quiero %s:%d in_app %v", n, f.Filename, f.Line, f.InApp != nil && *f.InApp, f.ContextLine, w.file, w.line, w.inApp)
			}
			if w.source != "" && f.ContextLine != w.source {
				t.Errorf("context_line = %q, quiero %q", f.ContextLine, w.source)
			}
		}
		n++
	}
	if n != len(want) {
		t.Errorf("%d fotogramas traducidos, quiero %d", n, len(want))
	}
}

func TestEnrichLeavesFramesAlone(t *testing.T) {
	s, st := webService(t)
	original := readEvent(t, "web-typeerror-1")

	for _, tc := range []struct {
		name    string
		release string
		project int64
	}{
		{"sin release", "", webID},
		{"otro release", "otro-release", webID},
		{"otro proyecto", release1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := readEvent(t, "web-typeerror-1")
			ev.Release = tc.release
			if err := s.Enrich(context.Background(), tc.project, ev); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(ev.Exceptions, original.Exceptions) {
				t.Error("los fotogramas cambiaron")
			}
		})
	}

	t.Run("mapa corrupto", func(t *testing.T) {
		const rel = "release-roto"
		_ = st.PutSourceMaps(context.Background(), webID, rel, []store.SourceMapFile{
			{File: chunkJS, Map: []byte(`{"version":3,"sources":["a.ts"],"mappings":"AAAA,!!!"}`)},
			{File: mainJS, Map: []byte(`{"version":3,"sources":[`)},
		})
		ev := readEvent(t, "web-typeerror-1")
		ev.Release = rel
		if err := s.Enrich(context.Background(), webID, ev); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ev.Exceptions, original.Exceptions) {
			t.Error("los fotogramas cambiaron")
		}
	})
}

func TestEnrichCache(t *testing.T) {
	s, st := webService(t)
	for range 3 {
		ev := readEvent(t, "web-typeerror-1")
		_ = s.Enrich(context.Background(), webID, ev)
	}
	// El chunk y main, que no tiene mapa, una vez cada uno.
	if st.reads != 2 {
		t.Errorf("%d lecturas del store, quiero 2", st.reads)
	}

	small := New(st, quiet(), Options{CacheBytes: 1 << 10})
	for range 2 {
		ev := readEvent(t, "web-typeerror-1")
		_ = small.Enrich(context.Background(), webID, ev)
		if ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1].Line != 7 {
			t.Fatal("sin caché también debe traducir")
		}
	}
	if small.cache.size > 1<<10 {
		t.Errorf("caché de %d bytes con tope de 1 KiB", small.cache.size)
	}
}

func TestUploadAuth(t *testing.T) {
	s := New(newMemStore(), quiet(), Options{UploadToken: token})
	for _, auth := range []string{"", "Bearer ", "Bearer otro", token, "Basic " + token} {
		body, ct := multipartBody(t, mapFile(t, chunkJS+".map"))
		if rec := post(t, s, uploadURL(webID, release1), auth, body, ct); rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d", auth, rec.Code)
		}
	}

	noToken := New(newMemStore(), quiet(), Options{})
	body, ct := multipartBody(t, mapFile(t, chunkJS+".map"))
	if rec := post(t, noToken, uploadURL(webID, release1), "Bearer ", body, ct); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin token configurado: %d", rec.Code)
	}
}

func TestUploadErrors(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	good := mapFile(t, chunkJS+".map")

	for _, tc := range []struct {
		name  string
		files []upload
		want  int
		msg   string
	}{
		{"JSON roto", []upload{good, {"file", "chunk-ROTO.js.map", []byte("{")}}, http.StatusBadRequest, "chunk-ROTO.js.map"},
		{"versión 2", []upload{{"file", "chunk-V2.js.map", []byte(`{"version":2,"sources":[],"mappings":""}`)}}, http.StatusBadRequest, "chunk-V2.js.map"},
		{"VLQ roto", []upload{{"file", "chunk-VLQ.js.map", []byte(`{"version":3,"sources":["a"],"mappings":"AAAA,g"}`)}}, http.StatusBadRequest, "chunk-VLQ.js.map"},
		{"fuente fuera de rango", []upload{{"file", "chunk-SRC.js.map", []byte(`{"version":3,"sources":["a"],"mappings":"ACAA"}`)}}, http.StatusBadRequest, "chunk-SRC.js.map"},
		{"no es .map", []upload{{"file", "chunk.js", good.body}}, http.StatusBadRequest, "chunk.js"},
		{"sin ficheros", []upload{{"otro", "chunk-A.js.map", good.body}}, http.StatusBadRequest, "file"},
		{"fichero de más de 20 MiB", []upload{good, {"file", "chunk-GRANDE.js.map", bytes.Repeat([]byte(" "), 20<<20+1)}}, http.StatusRequestEntityTooLarge, "chunk-GRANDE.js.map"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, ct := multipartBody(t, tc.files...)
			rec := post(t, s, uploadURL(webID, release1), "Bearer "+token, body, ct)
			if rec.Code != tc.want || !strings.Contains(rec.Body.String(), tc.msg) {
				t.Errorf("= %d %s; quiero %d con %q", rec.Code, rec.Body, tc.want, tc.msg)
			}
		})
	}
	if len(st.maps) != 0 {
		t.Errorf("con un error no se guarda nada; hay %d", len(st.maps))
	}

	t.Run("petición de más de 100 MiB", func(t *testing.T) {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			chunk := bytes.Repeat([]byte(" "), 19<<20)
			for i := range 6 {
				w, err := mw.CreateFormFile("file", "chunk-"+jsonInt(int64(i))+".js.map")
				if err != nil {
					_ = pw.CloseWithError(err)
					return
				}
				if _, err := w.Write(chunk); err != nil {
					_ = pw.CloseWithError(err)
					return
				}
			}
			_ = pw.CloseWithError(mw.Close())
		}()
		rec := post(t, s, uploadURL(webID, release1), "Bearer "+token, pr, mw.FormDataContentType())
		_ = pr.Close()
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("= %d %s", rec.Code, rec.Body)
		}
	})
}

func TestUploadReplaces(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	g := mapFile(t, chunkJS+".map")

	// Primero el mapa de main con el nombre del chunk: no traduce el carrito.
	wrong := mapFile(t, mainJS+".map")
	wrong.name = g.name
	body, ct := multipartBody(t, wrong)
	rec := post(t, s, uploadURL(webID, release1), "Bearer "+token, body, ct)
	if rec.Code != http.StatusOK {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	var resp struct {
		Release string      `json:"release"`
		Files   []savedFile `json:"files"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Release != release1 || len(resp.Files) != 1 || resp.Files[0] != (savedFile{Name: g.name, File: chunkJS, Size: len(wrong.body)}) {
		t.Errorf("respuesta = %+v", resp)
	}
	ev := readEvent(t, "web-typeerror-1")
	_ = s.Enrich(context.Background(), webID, ev)
	frames := ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename == carrito {
		t.Fatal("con el mapa equivocado no debería salir el carrito")
	}

	// Resubir sustituye y deja de usar el mapa viejo de la caché.
	uploadOK(t, s, webID, release1, g)
	if len(st.maps) != 1 {
		t.Errorf("%d mapas, quiero 1", len(st.maps))
	}
	ev = readEvent(t, "web-typeerror-1")
	_ = s.Enrich(context.Background(), webID, ev)
	frames = ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename != carrito || top.Line != 7 {
		t.Errorf("tras resubir = %s:%d", top.Filename, top.Line)
	}
}

func TestPurge(t *testing.T) {
	s, st := webService(t)
	old := key{project: webID, release: "viejo", file: chunkJS}
	st.maps[old] = stored{st.maps[key{project: webID, release: release1, file: chunkJS}].b, time.Now().AddDate(0, 0, -91)}

	ev := readEvent(t, "web-typeerror-1")
	ev.Release = "viejo"
	_ = s.Enrich(context.Background(), webID, ev)

	n, err := s.Purge(context.Background(), 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("borrados = %d, %v", n, err)
	}
	if _, ok := st.maps[old]; ok {
		t.Error("el viejo sigue")
	}
	ev = readEvent(t, "web-typeerror-1")
	ev.Release = "viejo"
	_ = s.Enrich(context.Background(), webID, ev)
	if ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1].Minified != nil {
		t.Error("tras borrarlo se sigue usando desde la caché")
	}
	if len(st.maps) != 1 {
		t.Errorf("quedan %d mapas, quiero 1", len(st.maps))
	}
}

func TestEnrichDoesNotShareCachedLines(t *testing.T) {
	s, _ := webService(t)
	ev := readEvent(t, "web-typeerror-1")
	_ = s.Enrich(context.Background(), webID, ev)
	top := &ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1]
	top.PreContext[0] = "cambiado"
	_ = append(top.PreContext[:1], "también cambiado")

	ev = readEvent(t, "web-typeerror-1")
	_ = s.Enrich(context.Background(), webID, ev)
	top = &ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1]
	if top.PreContext[0] == "cambiado" || top.PreContext[1] == "también cambiado" || top.ContextLine != failLine {
		t.Errorf("la caché cambió con el fotograma: %q", top.PreContext)
	}
}

func BenchmarkEnrichWeb(b *testing.B) {
	st := newMemStore()
	for _, f := range []string{chunkJS, mainJS} {
		body, err := os.ReadFile(filepath.Join(testdata, "sourcemaps", f+".map"))
		if err != nil {
			b.Fatal(err)
		}
		st.maps[key{project: webID, release: release1, file: f}] = stored{body, time.Now()}
	}
	s := New(st, quiet(), Options{})
	raw, err := os.ReadFile(filepath.Join(testdata, "envelopes", "web-typeerror-1.envelope"))
	if err != nil {
		b.Fatal(err)
	}
	env, err := envelope.Parse(raw)
	if err != nil {
		b.Fatal(err)
	}
	payload := env.Events()[0].Payload
	b.ReportAllocs()
	for b.Loop() {
		ev, _ := event.Parse(payload)
		_ = s.Enrich(context.Background(), webID, ev)
	}
}
