package sourcemap

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/intake-es/mothlog/internal/event"
	"github.com/intake-es/mothlog/internal/store"
)

// longLineMap es un mapa de app.js con un original de 11 líneas de n
// caracteres; la columna 0 de la línea 1 de app.js va a la línea 6 del original.
func longLineMap(n int) []byte {
	lines := make([]string, 11)
	for i := range lines {
		lines[i] = strings.Repeat("ñ", n)
	}
	b, _ := json.Marshal(map[string]any{
		"version": 3, "sources": []string{"src/a.ts"}, "sourcesContent": []string{strings.Join(lines, "\n")},
		"mappings": "AAKA",
	})
	return b
}

func framesEvent(release string, n int, file func(i int) string) *event.Event {
	ev := &event.Event{ID: strings.Repeat("a", 32), Level: "error", Release: release}
	frames := make([]event.Frame, n)
	for i := range frames {
		frames[i] = event.Frame{Filename: file(i), Line: 1, Column: 1}
	}
	ev.Exceptions = []event.Exception{{Type: "Error", Value: "x", Frames: frames}}
	return ev
}

func appJS(int) string { return "https://ejemplo.com/app.js" }

func translated(ev *event.Event) int {
	n := 0
	for _, e := range ev.Exceptions {
		for _, f := range e.Frames {
			if f.Minified != nil {
				n++
			}
		}
	}
	return n
}

func TestEnrichFrameBudget(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{})
	_ = st.PutSourceMaps(context.Background(), webID, "r", []store.SourceMapFile{{File: "app.js", Map: longLineMap(10)}})

	// 20.000 fotogramas de ficheros sin mapa: como mucho maxEnrichedFrames lecturas.
	ev := framesEvent("r", 20_000, func(i int) string { return fmt.Sprintf("https://ejemplo.com/f%d.js", i) })
	_ = s.Enrich(context.Background(), webID, ev)
	if st.reads > maxEnrichedFrames {
		t.Errorf("%d lecturas del store para un evento", st.reads)
	}

	// Se traducen los más internos: el último fotograma sí, el primero no.
	for _, innermost := range []bool{true, false} {
		ev := framesEvent("r", 1000, func(i int) string { return fmt.Sprintf("https://ejemplo.com/g%d.js", i) })
		frames := ev.Exceptions[0].Frames
		at := 0
		if innermost {
			at = len(frames) - 1
		}
		frames[at].Filename = appJS(0)
		_ = s.Enrich(context.Background(), webID, ev)
		if got := frames[at].Minified != nil; got != innermost {
			t.Errorf("fotograma %d de %d traducido: %v", at, len(frames), got)
		}
	}

	ev = framesEvent("r", 1000, appJS)
	_ = s.Enrich(context.Background(), webID, ev)
	if n := translated(ev); n != maxEnrichedFrames {
		t.Errorf("%d fotogramas traducidos, quiero %d", n, maxEnrichedFrames)
	}
}

// Un mapa que no cabe en la caché (de antes de rechazarlos al subir) se lee
// una vez por evento, no una por fotograma.
func TestEnrichLoadsMapOncePerEvent(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{CacheBytes: 1 << 10})
	_ = st.PutSourceMaps(context.Background(), webID, "r", []store.SourceMapFile{{File: "app.js", Map: longLineMap(100)}})
	for i := 1; i <= 2; i++ {
		ev := framesEvent("r", 200, appJS)
		_ = s.Enrich(context.Background(), webID, ev)
		if translated(ev) != 200 {
			t.Fatalf("evento %d: %d fotogramas traducidos", i, translated(ev))
		}
		if st.reads != i {
			t.Errorf("tras %d eventos de 200 fotogramas, %d lecturas", i, st.reads)
		}
	}
}

func TestEnrichClipsContextLines(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{})
	_ = st.PutSourceMaps(context.Background(), webID, "r", []store.SourceMapFile{{File: "app.js", Map: longLineMap(2000)}})
	ev := framesEvent("r", 1, appJS)
	_ = s.Enrich(context.Background(), webID, ev)
	f := ev.Exceptions[0].Frames[0]
	if f.Minified == nil || len(f.PreContext) != contextLines || len(f.PostContext) != contextLines {
		t.Fatalf("fotograma = %+v", f)
	}
	for _, l := range append(append([]string{f.ContextLine}, f.PreContext...), f.PostContext...) {
		if n := utf8.RuneCountInString(l); n != maxContextLine || !utf8.ValidString(l) {
			t.Errorf("línea de contexto de %d caracteres (válida %v)", n, utf8.ValidString(l))
		}
	}
	if got := clipLine("corta"); got != "corta" {
		t.Errorf("clipLine(corta) = %q", got)
	}
}

func TestEnrichSkipsLongKeys(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{})
	long := strings.Repeat("r", maxKeyLen+1)
	for _, ev := range []*event.Event{
		framesEvent(long, 10, appJS),
		framesEvent("r", 10, func(int) string { return "https://ejemplo.com/" + long + ".js" }),
		func() *event.Event { ev := framesEvent("r", 10, appJS); ev.Dist = long; return ev }(),
	} {
		_ = s.Enrich(context.Background(), webID, ev)
	}
	if st.reads != 0 {
		t.Errorf("%d búsquedas con claves de más de %d caracteres", st.reads, maxKeyLen)
	}
}

func allocated(f func()) uint64 {
	runtime.GC()
	var a, b runtime.MemStats
	runtime.ReadMemStats(&a)
	f()
	runtime.ReadMemStats(&b)
	return b.TotalAlloc - a.TotalAlloc
}

// La entrada de la auditoría: 10.000 fotogramas sobre un mapa con líneas de
// 1.000 caracteres asignaban 753 MiB más que sin traducir.
func TestEnrichEventCostBounded(t *testing.T) {
	st := newMemStore()
	s := New(st, quiet(), Options{})
	_ = st.PutSourceMaps(context.Background(), webID, "r", []store.SourceMapFile{{File: "app.js", Map: longLineMap(1000)}})
	cost := func(enrich bool) (uint64, int) {
		ev := framesEvent("r", 10_000, appJS)
		var size int
		a := allocated(func() {
			if enrich {
				_ = s.Enrich(context.Background(), webID, ev)
			}
			enc, err := store.EncodeEvent(ev)
			if err != nil {
				t.Fatal(err)
			}
			size = len(enc.Data)
		})
		return a, size
	}
	ev := framesEvent("r", 10_000, appJS)
	_ = s.Enrich(context.Background(), webID, ev)
	frames := ev.Exceptions[0].Frames
	total := 0
	for _, f := range frames {
		total += len(f.ContextLine)
		for _, l := range append(f.PreContext, f.PostContext...) {
			total += len(l)
		}
	}
	if total > maxContextBytes || frames[len(frames)-1].ContextLine == "" {
		t.Errorf("%d bytes de contexto en el evento; el más interno %q", total, frames[len(frames)-1].ContextLine)
	}

	_, _ = cost(false) // prepara el compresor
	plain, _ := cost(false)
	enriched, size := cost(true)
	t.Logf("sin traducir %d B, traducido %d B, guardado %d B", plain, enriched, size)
	if enriched > plain+8<<20 {
		t.Errorf("traducir un evento de 10.000 fotogramas asigna %d MB más", (enriched-plain)>>20)
	}
}

func heapAfterGC() uint64 {
	runtime.GC()
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return m.HeapAlloc
}

// Las releases largas de eventos sin mapa no dejan en la caché más de lo
// que cobra, y la caché no pasa de su tope.
func TestCacheRetainsWhatItCharges(t *testing.T) {
	const max = 1 << 20
	c := newCache(max)
	before := heapAfterGC()
	for i := range 400 {
		c.putMissing(key{project: 1, release: fmt.Sprintf("%010d", i) + strings.Repeat("r", 10<<10), file: "app.js"})
	}
	retained := int64(heapAfterGC()) - int64(before)
	t.Logf("cobrado %d B, retenido %d B", c.size, retained)
	if c.size > max {
		t.Errorf("la caché cobra %d bytes con un tope de %d", c.size, max)
	}
	if retained > max*3/2 {
		t.Errorf("la caché retiene %d bytes con un tope de %d", retained, max)
	}
	runtime.KeepAlive(c)

	// Por Enrich, con releases distintas del largo máximo que se busca.
	st := newMemStore()
	s := New(st, quiet(), Options{CacheBytes: 64 << 10})
	for i := range 2000 {
		ev := framesEvent(fmt.Sprintf("%06d", i)+strings.Repeat("r", maxKeyLen-6), 1, appJS)
		_ = s.Enrich(context.Background(), webID, ev)
	}
	if s.cache.size > 64<<10 {
		t.Errorf("la caché cobra %d bytes con un tope de %d", s.cache.size, 64<<10)
	}
}

// Lo que retiene un mapa parseado no pasa de lo que cobra, también con
// muchas líneas generadas vacías o muchas fuentes.
func TestMapSizeCoversRetained(t *testing.T) {
	sources := make([]string, 20_000)
	for i := range sources {
		sources[i] = fmt.Sprintf("src/%d.ts", i)
	}
	srcJSON, _ := json.Marshal(sources)
	for name, b := range map[string][]byte{
		"líneas vacías":  []byte(`{"version":3,"sources":["a.ts"],"mappings":"` + strings.Repeat(";", 1<<20) + `AAAA"}`),
		"muchas fuentes": []byte(`{"version":3,"sources":` + string(srcJSON) + `,"mappings":"AAAA"}`),
		"del navegador":  mapFile(t, chunkJS+".map").body,
	} {
		before := heapAfterGC()
		m, err := Parse(b)
		if err != nil {
			t.Fatal(err)
		}
		retained := int64(heapAfterGC()) - int64(before)
		t.Logf("%s: cobra %d B, retiene %d B", name, m.size, retained)
		// El montón tras GC tiene ruido de unos KB; lo que importa es la escala.
		if retained > m.size+64<<10 {
			t.Errorf("%s: retiene %d bytes y cobra %d", name, retained, m.size)
		}
		runtime.KeepAlive(m)
	}
}

func TestCacheMapTTL(t *testing.T) {
	c := newCache(1 << 20)
	now := time.Now()
	c.now = func() time.Time { return now }
	k := key{project: 1, release: "r", file: "app.js"}
	m, err := Parse(longLineMap(10))
	if err != nil {
		t.Fatal(err)
	}
	c.put(k, m)
	now = now.Add(mapTTL - time.Minute)
	if _, ok := c.get(k); !ok {
		t.Fatal("el mapa caducó antes de tiempo")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := c.get(k); ok {
		t.Error("el mapa sigue en la caché pasada una hora")
	}
}

// Un mapa que no cabría en la caché se rechaza al subirlo, por las tres vías.
func TestUploadRejectsMapBiggerThanCache(t *testing.T) {
	m := mapFile(t, chunkJS+".map")
	parsed, err := Parse(m.body)
	if err != nil {
		t.Fatal(err)
	}
	opts := Options{UploadToken: token, CacheBytes: parsed.size + maxKeySize - 1}

	t.Run("endpoint propio", func(t *testing.T) {
		st := newMemStore()
		s := New(st, quiet(), opts)
		body, ct := multipartBody(t, m)
		rec := post(t, s, uploadURL(webID, release1), "Bearer "+token, body, ct)
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "source map cache") {
			t.Errorf("= %d %s", rec.Code, rec.Body)
		}
		if len(st.maps) != 0 {
			t.Error("se guardó")
		}
		ok := New(st, quiet(), Options{UploadToken: token, CacheBytes: parsed.size + maxKeySize})
		uploadOK(t, ok, webID, release1, m)
	})

	t.Run("ficheros de release", func(t *testing.T) {
		st := newMemStore()
		s := New(st, quiet(), opts)
		var buf bytes.Buffer
		mw := multipart.NewWriter(&buf)
		w, _ := mw.CreateFormFile("file", chunkJS+".map")
		_, _ = w.Write(m.body)
		_ = mw.WriteField("name", "~/"+chunkJS+".map")
		_ = mw.Close()
		rec := serve(t, s, http.MethodPost, "/api/0/projects/o/web/releases/"+release1+"/files/", "Bearer "+token, mw.FormDataContentType(), buf.Bytes())
		if rec.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rec.Body.String(), "source map cache") {
			t.Errorf("= %d %s", rec.Code, rec.Body)
		}
		if len(st.maps) != 0 {
			t.Error("se guardó")
		}
	})

	t.Run("artifact bundle", func(t *testing.T) {
		st := newMemStore()
		s := New(st, quiet(), opts)
		z := makeZip(t, bundleFile{"manifest.json", []byte(manifestOK)}, bundleFile{"files/_/_/" + chunkJS, []byte("x")}, bundleFile{"files/_/_/" + chunkJS + ".map", m.body})
		code, res := assembleBundle(t, s, z, 1<<20, nil)
		if code != http.StatusOK || res.State != "error" || res.Detail == nil || !strings.Contains(*res.Detail, "source map cache") {
			t.Errorf("= %d %+v", code, res)
		}
		if len(st.maps) != 0 {
			t.Error("se guardó")
		}
	})
}
