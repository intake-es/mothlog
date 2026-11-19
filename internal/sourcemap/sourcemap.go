// Package sourcemap guarda los source maps del front y traduce con ellos los
// fotogramas de los eventos al código original.
package sourcemap

import (
	"context"
	"errors"
	"log/slog"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

type Store interface {
	PutSourceMaps(ctx context.Context, projectID int64, release string, files []store.SourceMapFile) error
	SourceMap(ctx context.Context, projectID int64, release, file string) ([]byte, error)
	DeleteSourceMapsBefore(ctx context.Context, t time.Time) (int64, error)
}

type Options struct {
	// UploadToken protege la subida; si está vacío, toda subida da 401.
	UploadToken     string
	CacheBytes      int64
	MaxFileBytes    int64
	MaxRequestBytes int64
}

const (
	DefaultCacheBytes      = 64 << 20
	DefaultMaxFileBytes    = 20 << 20
	DefaultMaxRequestBytes = 100 << 20

	contextLines = 5
	missingTTL   = time.Minute
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
	return &Service{store: st, log: log, opts: opts, cache: newCache(opts.CacheBytes)}
}

// Enrich traduce los fotogramas .js que tengan mapa para el release del
// evento. Lo que no se puede traducir se queda como estaba: nunca devuelve error.
func (s *Service) Enrich(ctx context.Context, projectID int64, ev *event.Event) error {
	if ev == nil || ev.Release == "" {
		return nil
	}
	for i := range ev.Exceptions {
		frames := ev.Exceptions[i].Frames
		for j := range frames {
			s.enrichFrame(ctx, projectID, ev.Release, &frames[j])
		}
	}
	return nil
}

func (s *Service) enrichFrame(ctx context.Context, projectID int64, release string, f *event.Frame) {
	if f.Minified != nil || f.Line <= 0 {
		return
	}
	minified := f.AbsPath
	if minified == "" {
		minified = f.Filename
	}
	file := jsFile(minified)
	if file == "" {
		return
	}
	m := s.load(ctx, key{projectID, release, file})
	if m == nil {
		return
	}
	orig, ok := m.Lookup(f.Line-1, max(f.Column-1, 0))
	if !ok || orig.Source == "" {
		return
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
		f.PreContext = slices.Clone(lines[max(orig.Line-contextLines, 0):orig.Line])
		f.ContextLine = lines[orig.Line]
		f.PostContext = slices.Clone(lines[orig.Line+1 : min(orig.Line+1+contextLines, len(lines))])
	}
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

func (s *Service) load(ctx context.Context, k key) *Map {
	if m, ok := s.cache.get(k); ok {
		return m
	}
	b, err := s.store.SourceMap(ctx, k.project, k.release, k.file)
	if errors.Is(err, store.ErrNotFound) {
		s.cache.putMissing(k)
		return nil
	}
	if err != nil {
		s.log.WarnContext(ctx, "source map: no se pudo leer", "project", k.project, "release", k.release, "file", k.file, "err", err)
		return nil
	}
	m, err := Parse(b)
	if err != nil {
		s.log.WarnContext(ctx, "source map roto", "project", k.project, "release", k.release, "file", k.file, "err", err)
		s.cache.putMissing(k)
		return nil
	}
	s.cache.put(k, m)
	return m
}

// Purge borra los mapas subidos hace más de maxAge.
func (s *Service) Purge(ctx context.Context, maxAge time.Duration) (int64, error) {
	n, err := s.store.DeleteSourceMapsBefore(ctx, time.Now().Add(-maxAge))
	if n > 0 {
		s.cache.clear()
	}
	return n, err
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
