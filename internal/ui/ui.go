// Package ui sirve la interfaz web (web/), compilada dentro del binario.
//
// `npm run build` en web/ la deja en dist/app; sin compilarla, go build y
// go test funcionan y la interfaz es una página que dice cómo compilarla.
package ui

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"io/fs"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
)

//go:embed all:dist
var dist embed.FS

// Handler sirve la app y pasa a next /api/, /mcp, /healthz, /llms.txt, /auth/ y todo lo
// que no sea GET ni HEAD.
func Handler(next http.Handler) http.Handler {
	app, err := fs.Sub(dist, "dist/app")
	if err != nil {
		panic(err)
	}
	return New(app, next)
}

var assetExt = map[string]bool{
	".js": true, ".mjs": true, ".css": true, ".map": true, ".json": true, ".txt": true, ".html": true, ".xml": true,
	".ico": true, ".png": true, ".jpg": true, ".jpeg": true, ".gif": true, ".svg": true, ".webp": true, ".avif": true,
	".woff": true, ".woff2": true, ".ttf": true, ".otf": true, ".webmanifest": true,
}

// hashed son los ficheros con el hash de Angular en el nombre (main-ABCD1234.js).
var hashed = regexp.MustCompile(`-[A-Z0-9]{8}\.[a-z0-9]+$`)

const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; " +
	"font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'self'; frame-ancestors 'none'; form-action 'self'"

type file struct {
	data []byte
	etag string
}

type server struct {
	files map[string]file
	index file
	next  http.Handler
}

// New sirve los ficheros de app; si no tiene index.html, el marcador de posición.
func New(app fs.FS, next http.Handler) http.Handler {
	s := &server{files: map[string]file{}, next: next}
	_ = fs.WalkDir(app, ".", func(name string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		b, err := fs.ReadFile(app, name)
		if err != nil {
			return nil
		}
		sum := sha256.Sum256(b)
		s.files[name] = file{data: b, etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
		return nil
	})
	idx, ok := s.files["index.html"]
	if !ok {
		sum := sha256.Sum256([]byte(placeholder))
		idx = file{data: []byte(placeholder), etag: `"` + hex.EncodeToString(sum[:8]) + `"`}
	}
	s.index = idx
	return s
}

func reserved(p string) bool {
	return p == "/api" || strings.HasPrefix(p, "/api/") ||
		p == "/mcp" || strings.HasPrefix(p, "/mcp/") ||
		p == "/healthz" || p == "/llms.txt" || p == "/metrics" ||
		p == "/auth" || strings.HasPrefix(p, "/auth/") ||
		p == "/oauth" || strings.HasPrefix(p, "/oauth/") || strings.HasPrefix(p, "/.well-known/")
}

func (s *server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if (r.Method != http.MethodGet && r.Method != http.MethodHead) || reserved(r.URL.Path) {
		s.next.ServeHTTP(w, r)
		return
	}
	name := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if f, ok := s.files[name]; ok && name != "index.html" {
		if hashed.MatchString(name) {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-cache")
		}
		serve(w, r, name, f)
		return
	}
	// Un fichero que no existe es un 404: un main-*.js de un build anterior no
	// debe recibir el HTML. Una ruta con un punto que no es de fichero, como
	// /releases/1/web@1.2.3, es de la app.
	if assetExt[strings.ToLower(path.Ext(name))] && name != "index.html" {
		http.NotFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-cache")
	h.Set("Content-Security-Policy", csp)
	h.Set("X-Frame-Options", "DENY")
	h.Set("Referrer-Policy", "same-origin")
	serve(w, r, "index.html", s.index)
}

func serve(w http.ResponseWriter, r *http.Request, name string, f file) {
	w.Header().Set("ETag", f.etag)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, r, name, time.Time{}, bytes.NewReader(f.data))
}

const placeholder = `<!doctype html>
<html lang="en">
<head><meta charset="utf-8"><title>Mothlog</title>
<meta name="viewport" content="width=device-width, initial-scale=1"></head>
<body style="font-family: system-ui, sans-serif; max-width: 36rem; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a">
<h1 style="font-size: 1.25rem">The UI is not built</h1>
<p>This binary was built without the web app. To include it, <code>cd web && npm ci && npm run build</code> and build Go again.
The API (<code>/api/0/</code>) and the MCP (<code>/mcp</code>) work the same.</p>
</body>
</html>
`
