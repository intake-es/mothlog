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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/envelope"
	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

const (
	testdata  = "../../testdata"
	token     = "token-de-prueba"
	frontID   = int64(3)
	release1  = "fixture-release-1"
	loginPage = "src/app/pages/pagina-de-ejemplo/pagina-de-ejemplo"
)

type stored struct {
	b  []byte
	at time.Time
}

type memStore struct {
	mu    sync.Mutex
	maps  map[key]stored
	reads int
}

func newMemStore() *memStore { return &memStore{maps: map[key]stored{}} }

func (m *memStore) PutSourceMaps(_ context.Context, projectID int64, release string, files []store.SourceMapFile) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, f := range files {
		m.maps[key{projectID, release, f.File}] = stored{f.Map, time.Now()}
	}
	return nil
}

func (m *memStore) SourceMap(_ context.Context, projectID int64, release, file string) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reads++
	s, ok := m.maps[key{projectID, release, file}]
	if !ok {
		return nil, store.ErrNotFound
	}
	return s.b, nil
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

func loginService(t *testing.T) (*Service, *memStore) {
	t.Helper()
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	uploadOK(t, s, frontID, release1, mapFile(t, "login/chunk-A1B2C3D4.js.map"), mapFile(t, "login/chunk-E5F6G7H8.js.map"))
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

func TestEnrichLogin(t *testing.T) {
	s, _ := loginService(t)
	ev := readEvent(t, "frontend-login-typeerror-1")
	if err := s.Enrich(context.Background(), frontID, ev); err != nil {
		t.Fatal(err)
	}
	frames := ev.Exceptions[len(ev.Exceptions)-1].Frames

	top := frames[len(frames)-1]
	if top.Filename != loginPage+".ts" || top.Line != 268 {
		t.Fatalf("primer fotograma = %s:%d", top.Filename, top.Line)
	}
	if top.ContextLine != "    return data.cuenta!.id;" {
		t.Errorf("context_line = %q", top.ContextLine)
	}
	if len(top.PreContext) != 5 || top.PreContext[4] != "  private failForFixture(data: { cuenta?: { id: string } }): string {" {
		t.Errorf("pre_context = %q", top.PreContext)
	}
	// Es casi el final del fichero: solo quedan tres líneas.
	if !reflect.DeepEqual(top.PostContext, []string{"  }", "}", ""}) {
		t.Errorf("post_context = %q", top.PostContext)
	}
	if top.InApp == nil || !*top.InApp {
		t.Error("in_app debería ser verdadero")
	}
	if top.Function != "t.failForFixture" {
		t.Errorf("función = %q; debe quedar la del SDK", top.Function)
	}
	want := &event.Position{Filename: "https://app.example.com/chunk-A1B2C3D4.js", Line: 1, Column: 11129}
	if !reflect.DeepEqual(top.Minified, want) {
		t.Errorf("minified = %+v", top.Minified)
	}

	if f := findFrame(t, ev, "chunk-A1B2C3D4.js", 8546); f.Filename != loginPage+".ts" || f.Line != 134 {
		t.Errorf("columna 8546 = %s:%d", f.Filename, f.Line)
	}
	if f := findFrame(t, ev, "chunk-A1B2C3D4.js", 4939); f.Filename != loginPage+".html" || f.Line != 133 {
		t.Errorf("columna 4939 = %s:%d", f.Filename, f.Line)
	}

	var angular int
	for _, f := range frames {
		if f.Minified == nil || !strings.HasSuffix(f.Minified.Filename, "/chunk-E5F6G7H8.js") {
			continue
		}
		angular++
		if !strings.HasPrefix(f.Filename, "node_modules/@angular/core/") {
			t.Errorf("chunk-E5F6G7H8.js:%d = %s", f.Minified.Column, f.Filename)
		}
		if f.InApp == nil || *f.InApp {
			t.Errorf("%s debería ser in_app falso", f.Filename)
		}
	}
	if angular != 3 {
		t.Errorf("%d fotogramas de chunk-E5F6G7H8.js traducidos, quiero 3", angular)
	}

	// Sin mapa subido para estos: se quedan igual.
	for _, f := range frames {
		if strings.Contains(f.Filename, "polyfills-") || strings.Contains(f.Filename, "chunk-DKRR2FIJ") || f.Filename == "<anonymous>" {
			if f.Minified != nil {
				t.Errorf("%s no debería cambiar", f.Filename)
			}
		}
	}
}

func TestEnrichPolyfills(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	uploadOK(t, s, frontID, "r-polyfills", mapFile(t, "polyfills-J9K8L7M6.js.map"))

	ev := readEvent(t, "frontend-angular-typeerror-1")
	ev.Release = "r-polyfills"
	if err := s.Enrich(context.Background(), frontID, ev); err != nil {
		t.Fatal(err)
	}
	// Las mismas líneas que da source-map 0.7.6.
	wantLines := []int{1778, 504, 515, 161, 431, 1809}
	var n int
	for _, f := range ev.Exceptions[0].Frames {
		if f.Minified == nil {
			if strings.Contains(f.Filename, "polyfills-") {
				t.Errorf("sin traducir: %+v", f)
			}
			continue
		}
		if n < len(wantLines) && f.Line != wantLines[n] {
			t.Errorf("fotograma %d en la línea %d, quiero %d", n, f.Line, wantLines[n])
		}
		n++
		if f.Filename != "node_modules/zone.js/fesm2015/zone.js" || f.ContextLine == "" {
			t.Errorf("fotograma = %s:%d %q", f.Filename, f.Line, f.ContextLine)
		}
		if f.InApp == nil || *f.InApp {
			t.Errorf("zone.js debería ser in_app falso")
		}
	}
	if n != 6 {
		t.Errorf("%d fotogramas traducidos, quiero 6", n)
	}
}

func TestEnrichLeavesFramesAlone(t *testing.T) {
	s, st := loginService(t)
	original := readEvent(t, "frontend-login-typeerror-1")

	for _, tc := range []struct {
		name    string
		release string
		project int64
	}{
		{"sin release", "", frontID},
		{"otro release", "otro-release", frontID},
		{"otro proyecto", release1, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := readEvent(t, "frontend-login-typeerror-1")
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
		_ = st.PutSourceMaps(context.Background(), frontID, rel, []store.SourceMapFile{
			{File: "chunk-A1B2C3D4.js", Map: []byte(`{"version":3,"sources":["a.ts"],"mappings":"AAAA,!!!"}`)},
			{File: "chunk-E5F6G7H8.js", Map: []byte(`{"version":3,"sources":[`)},
		})
		ev := readEvent(t, "frontend-login-typeerror-1")
		ev.Release = rel
		if err := s.Enrich(context.Background(), frontID, ev); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(ev.Exceptions, original.Exceptions) {
			t.Error("los fotogramas cambiaron")
		}
	})
}

func TestEnrichCache(t *testing.T) {
	s, st := loginService(t)
	for range 3 {
		ev := readEvent(t, "frontend-login-typeerror-1")
		_ = s.Enrich(context.Background(), frontID, ev)
	}
	// chunk-A1B2C3D4, chunk-E5F6G7H8, polyfills y chunk-DKRR2FIJ, una vez cada uno.
	if st.reads != 4 {
		t.Errorf("%d lecturas del store, quiero 4", st.reads)
	}

	small := New(st, quiet(), Options{CacheBytes: 1 << 10})
	for range 2 {
		ev := readEvent(t, "frontend-login-typeerror-1")
		_ = small.Enrich(context.Background(), frontID, ev)
		if ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1].Line != 268 {
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
		body, ct := multipartBody(t, mapFile(t, "login/chunk-A1B2C3D4.js.map"))
		if rec := post(t, s, uploadURL(frontID, release1), auth, body, ct); rec.Code != http.StatusUnauthorized {
			t.Errorf("Authorization %q: %d", auth, rec.Code)
		}
	}

	noToken := New(newMemStore(), quiet(), Options{})
	body, ct := multipartBody(t, mapFile(t, "login/chunk-A1B2C3D4.js.map"))
	if rec := post(t, noToken, uploadURL(frontID, release1), "Bearer ", body, ct); rec.Code != http.StatusUnauthorized {
		t.Errorf("sin token configurado: %d", rec.Code)
	}
}

func TestUploadErrors(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	good := mapFile(t, "login/chunk-A1B2C3D4.js.map")

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
			rec := post(t, s, uploadURL(frontID, release1), "Bearer "+token, body, ct)
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
		rec := post(t, s, uploadURL(frontID, release1), "Bearer "+token, pr, mw.FormDataContentType())
		_ = pr.Close()
		if rec.Code != http.StatusRequestEntityTooLarge {
			t.Errorf("= %d %s", rec.Code, rec.Body)
		}
	})
}

func TestUploadReplaces(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{UploadToken: token})
	g := mapFile(t, "login/chunk-A1B2C3D4.js.map")

	// Primero un mapa del polyfills con el nombre del chunk: no traduce el login.
	wrong := mapFile(t, "polyfills-J9K8L7M6.js.map")
	wrong.name = g.name
	body, ct := multipartBody(t, wrong)
	rec := post(t, s, uploadURL(frontID, release1), "Bearer "+token, body, ct)
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
	if resp.Release != release1 || len(resp.Files) != 1 || resp.Files[0] != (savedFile{Name: g.name, File: "chunk-A1B2C3D4.js", Size: len(wrong.body)}) {
		t.Errorf("respuesta = %+v", resp)
	}
	ev := readEvent(t, "frontend-login-typeerror-1")
	_ = s.Enrich(context.Background(), frontID, ev)
	frames := ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename == loginPage+".ts" {
		t.Fatal("con el mapa equivocado no debería salir el login")
	}

	// Resubir sustituye y deja de usar el mapa viejo de la caché.
	uploadOK(t, s, frontID, release1, g)
	if len(st.maps) != 1 {
		t.Errorf("%d mapas, quiero 1", len(st.maps))
	}
	ev = readEvent(t, "frontend-login-typeerror-1")
	_ = s.Enrich(context.Background(), frontID, ev)
	frames = ev.Exceptions[0].Frames
	if top := frames[len(frames)-1]; top.Filename != loginPage+".ts" || top.Line != 268 {
		t.Errorf("tras resubir = %s:%d", top.Filename, top.Line)
	}
}

func TestPurge(t *testing.T) {
	s, st := loginService(t)
	old := key{frontID, "viejo", "chunk-A1B2C3D4.js"}
	st.maps[old] = stored{st.maps[key{frontID, release1, "chunk-A1B2C3D4.js"}].b, time.Now().AddDate(0, 0, -91)}

	ev := readEvent(t, "frontend-login-typeerror-1")
	ev.Release = "viejo"
	_ = s.Enrich(context.Background(), frontID, ev)

	n, err := s.Purge(context.Background(), 90*24*time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("borrados = %d, %v", n, err)
	}
	if _, ok := st.maps[old]; ok {
		t.Error("el viejo sigue")
	}
	ev = readEvent(t, "frontend-login-typeerror-1")
	ev.Release = "viejo"
	_ = s.Enrich(context.Background(), frontID, ev)
	if ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1].Minified != nil {
		t.Error("tras borrarlo se sigue usando desde la caché")
	}
	if len(st.maps) != 2 {
		t.Errorf("quedan %d mapas, quiero 2", len(st.maps))
	}
}

func TestEnrichDoesNotShareCachedLines(t *testing.T) {
	s, _ := loginService(t)
	ev := readEvent(t, "frontend-login-typeerror-1")
	_ = s.Enrich(context.Background(), frontID, ev)
	top := &ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1]
	top.PreContext[0] = "cambiado"
	_ = append(top.PreContext[:1], "también cambiado")

	ev = readEvent(t, "frontend-login-typeerror-1")
	_ = s.Enrich(context.Background(), frontID, ev)
	top = &ev.Exceptions[0].Frames[len(ev.Exceptions[0].Frames)-1]
	if top.PreContext[0] == "cambiado" || top.PreContext[1] == "también cambiado" || top.ContextLine != "    return data.cuenta!.id;" {
		t.Errorf("la caché cambió con el fotograma: %q", top.PreContext)
	}
}

func BenchmarkEnrichLogin(b *testing.B) {
	st := newMemStore()
	for _, f := range []string{"chunk-A1B2C3D4.js", "chunk-E5F6G7H8.js"} {
		body, err := os.ReadFile(filepath.Join(testdata, "sourcemaps", "login", f+".map"))
		if err != nil {
			b.Fatal(err)
		}
		st.maps[key{frontID, release1, f}] = stored{body, time.Now()}
	}
	s := New(st, quiet(), Options{})
	raw, err := os.ReadFile(filepath.Join(testdata, "envelopes", "frontend-login-typeerror-1.envelope"))
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
		_ = s.Enrich(context.Background(), frontID, ev)
	}
}
