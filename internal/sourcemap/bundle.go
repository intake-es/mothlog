package sourcemap

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"path"
	"slices"
	"strings"

	"github.com/intake-es/mothlog/internal/store"
)

// manifest es el manifest.json de un artifact bundle de sentry-cli.
type manifest struct {
	Release string                  `json:"release"`
	Dist    string                  `json:"dist"`
	Files   map[string]manifestFile `json:"files"`
}

type manifestFile struct {
	Type    string            `json:"type"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

func (f manifestFile) header(names ...string) string {
	for k, v := range f.Headers {
		for _, n := range names {
			if strings.EqualFold(k, n) {
				return v
			}
		}
	}
	return ""
}

func (f manifestFile) debugID() string {
	return normalizeDebugID(f.header("debug-id", "debug_id"))
}

// readBundle abre un artifact bundle y devuelve su manifest y sus source
// maps, validados. Los .js no se guardan: solo sirven para saber a qué
// fichero y debug ID va cada mapa. Cada mapa puede ocupar maxFile y todos
// juntos, descomprimidos, maxTotal; check rechaza los que no caben en la caché.
func readBundle(b []byte, maxFile, maxTotal int64, check func(string, *Map) error) (manifest, []store.SourceMapFile, error) {
	var m manifest
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return m, nil, fmt.Errorf("the bundle is not a valid zip: %w", err)
	}
	entries := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	mf, ok := entries["manifest.json"]
	if !ok {
		return m, nil, errors.New("the bundle has no manifest.json")
	}
	raw, err := readZipFile(mf, maxFile)
	if err != nil {
		return m, nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, nil, fmt.Errorf("manifest.json is not valid: %w", err)
	}

	// El .js de cada mapa, por la URL del mapa que dice su cabecera sourcemap.
	jsFor := map[string]manifestFile{}
	for _, f := range m.Files {
		if f.Type != "minified_source" {
			continue
		}
		if ref := f.header("sourcemap", "x-sourcemap"); ref != "" {
			jsFor[resolveURL(f.URL, ref)] = f
		}
	}

	var (
		files []store.SourceMapFile
		total int64
	)
	for _, name := range slices.Sorted(maps.Keys(m.Files)) {
		f := m.Files[name]
		if f.Type != "source_map" {
			continue
		}
		zf, ok := entries[name]
		if !ok {
			return m, nil, fmt.Errorf("manifest.json lists %s, which is not in the bundle", name)
		}
		data, err := readZipFile(zf, maxFile)
		if err != nil {
			return m, nil, err
		}
		if total += int64(len(data)); total > maxTotal {
			return m, nil, fmt.Errorf("the bundle's maps are over %d MiB uncompressed", maxTotal>>20)
		}
		label := f.URL
		if label == "" {
			label = name
		}
		sm, err := Parse(data)
		if err != nil {
			return m, nil, fmt.Errorf("%s is not a valid v3 source map: %v", label, err)
		}
		if err := check(label, sm); err != nil {
			return m, nil, err
		}
		js := jsFor[f.URL]
		id := f.debugID()
		if id == "" {
			id = mapDebugID(data)
		}
		if id == "" {
			id = js.debugID()
		}
		jsURL := js.URL
		if jsURL == "" {
			jsURL = strings.TrimSuffix(f.URL, ".map")
		}
		files = append(files, store.SourceMapFile{File: path.Base(jsURL), URL: jsURL, DebugID: id, Map: data})
	}
	if len(files) == 0 {
		return m, nil, errors.New("the bundle has no source maps")
	}
	return m, files, nil
}

// mapDebugID es el debug ID que `sourcemaps inject` escribe dentro del mapa.
func mapDebugID(b []byte) string {
	var ids struct {
		Snake string `json:"debug_id"`
		Camel string `json:"debugId"`
	}
	if json.Unmarshal(b, &ids) != nil {
		return ""
	}
	if id := normalizeDebugID(ids.Snake); id != "" {
		return id
	}
	return normalizeDebugID(ids.Camel)
}

func readZipFile(f *zip.File, maxFile int64) ([]byte, error) {
	if f.UncompressedSize64 > uint64(maxFile) {
		return nil, fmt.Errorf("%s is over %d MiB", f.Name, maxFile>>20)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("can't read %s from the bundle: %w", f.Name, err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(io.LimitReader(rc, maxFile+1))
	if err != nil {
		return nil, fmt.Errorf("can't read %s from the bundle: %w", f.Name, err)
	}
	if int64(len(b)) > maxFile {
		return nil, fmt.Errorf("%s is over %d MiB", f.Name, maxFile>>20)
	}
	return b, nil
}

// resolveURL resuelve ref (la cabecera sourcemap de un .js) contra la URL del .js.
func resolveURL(base, ref string) string {
	if strings.HasPrefix(ref, "~/") || strings.HasPrefix(ref, "/") || strings.Contains(ref, "://") {
		return ref
	}
	if strings.Contains(base, "://") {
		if b, err := url.Parse(base); err == nil {
			if r, err := url.Parse(ref); err == nil {
				return b.ResolveReference(r).String()
			}
		}
	}
	return path.Join(path.Dir(base), ref)
}
