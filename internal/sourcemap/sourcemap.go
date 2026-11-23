// Package sourcemap guarda los source maps del front y traduce con ellos los
// fotogramas de los eventos al código original.
package sourcemap

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

type Store interface {
	PutSourceMaps(ctx context.Context, projectID int64, release string, files []store.SourceMapFile) error
	SourceMap(ctx context.Context, projectID int64, release, dist, file string) ([]byte, error)
	SourceMapByDebugID(ctx context.Context, projectID int64, debugID string) ([]byte, error)
	DeleteSourceMapsBefore(ctx context.Context, t time.Time) (int64, error)

	ProjectByName(ctx context.Context, name string) (store.Project, error)
	Authenticate(ctx context.Context, secret string) (store.Token, error)

	PutUploadChunks(ctx context.Context, chunks map[string][]byte) error
	MissingUploadChunks(ctx context.Context, sums []string) ([]string, error)
	UploadChunk(ctx context.Context, sum string) ([]byte, error)
	DeleteUploadChunksBefore(ctx context.Context, t time.Time) (int64, error)
	BundleAssembled(ctx context.Context, checksum string, projectIDs []int64) (bool, error)
	PutBundle(ctx context.Context, b store.Bundle) error

	CreateRelease(ctx context.Context, projectID int64, version string) (store.Release, bool, error)
	FinalizeRelease(ctx context.Context, projectID int64, version string, at time.Time) (store.Release, error)
	AddDeploy(ctx context.Context, projectID int64, version, environment string, at time.Time) (store.Deploy, []int64, error)
	ReleaseProjects(ctx context.Context, version string) ([]store.Project, error)
}

type Options struct {
	// UploadToken es el token de subida además de los de la API; si está
	// vacío, solo valen los de la API.
	UploadToken     string
	CacheBytes      int64
	MaxFileBytes    int64
	MaxRequestBytes int64
	// MaxBundleBytes es el tope de un artifact bundle ensamblado.
	MaxBundleBytes int64
}

const (
	DefaultCacheBytes      = 64 << 20
	DefaultMaxFileBytes    = 20 << 20
	DefaultMaxRequestBytes = 100 << 20
	DefaultMaxBundleBytes  = 100 << 20

	contextLines = 5
	missingTTL   = time.Minute
	// mapTTL es lo que dura un mapa en la caché, para que una réplica no siga
	// usando uno que otra ha borrado.
	mapTTL = time.Hour
	// maxEnrichedFrames es lo más que se traduce de un evento, empezando por
	// los fotogramas más internos.
	maxEnrichedFrames = 250
	// maxContextLine es lo más que se copia de cada línea de contexto, en caracteres.
	maxContextLine = 512
	// maxContextBytes es lo más que se añade de contexto a un evento; pasado
	// eso, los fotogramas se traducen sin sus líneas.
	maxContextBytes = 256 << 10
	// maxKeyLen es lo más largo que puede ser el release, el dist o el
	// fichero con los que se busca un mapa.
	maxKeyLen = 512
	// chunkTTL es lo que se guarda un trozo, se ensamble o no.
	chunkTTL = 24 * time.Hour
)

type Service struct {
	store Store
	log   *slog.Logger
	opts  Options
	cache *cache
}

func New(st Store, log *slog.Logger, opts Options) *Service {
	if opts.CacheBytes <= 0 {
		opts.CacheBytes = DefaultCacheBytes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.MaxRequestBytes <= 0 {
		opts.MaxRequestBytes = DefaultMaxRequestBytes
	}
	if opts.MaxBundleBytes <= 0 {
		opts.MaxBundleBytes = DefaultMaxBundleBytes
	}
	return &Service{store: st, log: log, opts: opts, cache: newCache(opts.CacheBytes)}
}

// Enrich traduce los fotogramas .js: con el mapa de su debug ID si el evento
// lo trae en debug_meta y, si no, con el de su nombre en el release del
// evento. Lo que no se puede traducir se queda como estaba: nunca devuelve error.
// Traduce como mucho maxEnrichedFrames fotogramas, los más internos, y lee
// cada mapa una vez por evento.
func (s *Service) Enrich(ctx context.Context, projectID int64, ev *event.Event) error {
	if ev == nil {
		return nil
	}
	ids := debugIDs(ev.DebugMeta)
	if ev.Release == "" && len(ids) == 0 {
		return nil
	}
	b := &budget{frames: maxEnrichedFrames, context: maxContextBytes, seen: map[key]*Map{}}
	for i := len(ev.Exceptions) - 1; i >= 0 && b.frames > 0; i-- {
		frames := ev.Exceptions[i].Frames
		for j := len(frames) - 1; j >= 0 && b.frames > 0; j-- {
			if s.enrichFrame(ctx, projectID, ev, ids, b, &frames[j]) {
				b.frames--
			}
		}
	}
	return nil
}

// budget es lo que le queda a un evento por traducir, y los mapas que ya ha leído.
type budget struct {
	frames  int
	context int
	seen    map[key]*Map
}

// load es Service.load con lo ya leído en este evento.
func (b *budget) load(ctx context.Context, s *Service, k key) *Map {
	if m, ok := b.seen[k]; ok {
		return m
	}
	m := s.load(ctx, k)
	b.seen[k] = m
	return m
}

// enrichFrame traduce f si puede; dice si ha buscado algún mapa.
func (s *Service) enrichFrame(ctx context.Context, projectID int64, ev *event.Event, ids map[string]string, b *budget, f *event.Frame) bool {
	if f.Minified != nil || f.Line <= 0 {
		return false
	}
	minified := f.AbsPath
	if minified == "" {
		minified = f.Filename
	}
	var (
		m      *Map
		looked bool
	)
	id := ids[minified]
	if id == "" {
		id = ids[f.Filename]
	}
	if id != "" {
		m, looked = b.load(ctx, s, key{project: projectID, debugID: id}), true
	}
	if m == nil && ev.Release != "" && len(ev.Release) <= maxKeyLen && len(ev.Dist) <= maxKeyLen {
		if file := jsFile(minified); file != "" && len(file) <= maxKeyLen {
			m, looked = b.load(ctx, s, key{project: projectID, release: ev.Release, dist: ev.Dist, file: file}), true
		}
	}
	if m == nil {
		return looked
	}
	orig, ok := m.Lookup(f.Line-1, max(f.Column-1, 0))
	if !ok || orig.Source == "" {
		return true
	}

	f.Minified = &event.Position{Filename: minified, Line: f.Line, Column: f.Column}
	f.Filename = orig.Source
	f.AbsPath = orig.Source
	f.Line = orig.Line + 1
	f.Column = orig.Column + 1
	inApp := !strings.HasPrefix(orig.Source, "node_modules/") && !strings.Contains(orig.Source, "/node_modules/")
	f.InApp = &inApp
	f.PreContext, f.ContextLine, f.PostContext = nil, "", nil
	if lines := m.SourceLines(orig.Source); orig.Line < len(lines) {
		pre := clipLines(lines[max(orig.Line-contextLines, 0):orig.Line])
		line := clipLine(lines[orig.Line])
		post := clipLines(lines[orig.Line+1 : min(orig.Line+1+contextLines, len(lines))])
		n := len(line)
		for _, l := range pre {
			n += len(l)
		}
		for _, l := range post {
			n += len(l)
		}
		if n <= b.context {
			b.context -= n
			f.PreContext, f.ContextLine, f.PostContext = pre, line, post
		}
	}
	return true
}

// clipLine recorta una línea de contexto a maxContextLine caracteres.
func clipLine(l string) string {
	if len(l) <= maxContextLine {
		return l
	}
	n := 0
	for i := range l {
		if n == maxContextLine {
			return l[:i]
		}
		n++
	}
	return l
}

func clipLines(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = clipLine(l)
	}
	return out
}

// jsFile devuelve el nombre del .js de una URL o ruta, o "" si no es un .js.
func jsFile(s string) string {
	p := s
	if u, err := url.Parse(s); err == nil {
		p = u.Path
	}
	name := path.Base(p)
	if !strings.HasSuffix(name, ".js") || name == ".js" {
		return ""
	}
	return name
}

// debugIDs devuelve el debug ID de cada .js de las imágenes sourcemap de
// debug_meta, por su URL.
func debugIDs(debugMeta []byte) map[string]string {
	if len(debugMeta) == 0 {
		return nil
	}
	var dm struct {
		Images []struct {
			Type     any `json:"type"`
			CodeFile any `json:"code_file"`
			DebugID  any `json:"debug_id"`
		} `json:"images"`
	}
	if json.Unmarshal(debugMeta, &dm) != nil {
		return nil
	}
	ids := map[string]string{}
	for _, img := range dm.Images {
		typ, _ := img.Type.(string)
		file, _ := img.CodeFile.(string)
		raw, _ := img.DebugID.(string)
		if id := normalizeDebugID(raw); typ == "sourcemap" && file != "" && id != "" {
			ids[file] = id
		}
	}
	return ids
}

// normalizeDebugID deja un debug ID como UUID en minúscula, o "" si no lo es.
func normalizeDebugID(s string) string {
	h := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(s), "-", ""))
	if len(h) != 32 {
		return ""
	}
	if _, err := hex.DecodeString(h); err != nil {
		return ""
	}
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

func (s *Service) load(ctx context.Context, k key) *Map {
	if m, ok := s.cache.get(k); ok {
		return m
	}
	var (
		b   []byte
		err error
	)
	if k.debugID != "" {
		b, err = s.store.SourceMapByDebugID(ctx, k.project, k.debugID)
	} else {
		b, err = s.store.SourceMap(ctx, k.project, k.release, k.dist, k.file)
	}
	if errors.Is(err, store.ErrNotFound) {
		s.cache.putMissing(k)
		return nil
	}
	if err != nil {
		s.log.WarnContext(ctx, "source map: no se pudo leer", "project", k.project, "release", k.release, "file", k.file, "debug_id", k.debugID, "err", err)
		return nil
	}
	m, err := Parse(b)
	if err != nil {
		s.log.WarnContext(ctx, "source map roto", "project", k.project, "release", k.release, "file", k.file, "debug_id", k.debugID, "err", err)
		s.cache.putMissing(k)
		return nil
	}
	// Uno que no cabe en la caché se vuelve a leer en cada evento que lo usa;
	// desde que se rechazan al subirlos solo pueden ser de antes.
	if !s.cache.put(k, m) {
		s.log.WarnContext(ctx, "source map más grande que la caché", "project", k.project, "release", k.release, "file", k.file, "debug_id", k.debugID, "bytes", m.size, "cache_bytes", s.opts.CacheBytes)
	}
	return m
}

// checkSize es el error de un mapa que no cabría en la caché, o nil.
func (s *Service) checkSize(name string, m *Map) error {
	if m.size+maxKeySize <= s.opts.CacheBytes {
		return nil
	}
	return fmt.Errorf("%s ocupa %d MiB ya parseado y la caché de source maps es de %d MiB: divídelo en mapas más pequeños", name, (m.size+1<<20-1)>>20, s.opts.CacheBytes>>20)
}

// Purge borra los mapas subidos hace más de maxAge y los trozos de más de chunkTTL.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) (int64, error) {
	n, err := s.store.DeleteSourceMapsBefore(ctx, time.Now().Add(-maxAge))
	// Siempre: otra réplica puede haber borrado mapas que esta tiene en caché.
	s.cache.clear()
	if err != nil {
		return n, err
	}
	if _, err := s.store.DeleteUploadChunksBefore(ctx, time.Now().Add(-min(chunkTTL, maxAge))); err != nil {
		return n, err
	}
	return n, nil
}

// RunRetention llama a Purge al empezar y luego cada every, hasta que ctx acabe.
func (s *Service) RunRetention(ctx context.Context, maxAge, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		n, err := s.Purge(ctx, maxAge)
		if err != nil {
			s.log.ErrorContext(ctx, "retención de source maps", "err", err)
		} else if n > 0 {
			s.log.InfoContext(ctx, "source maps caducados borrados", "n", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
