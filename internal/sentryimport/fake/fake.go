// Package fake es una API de Sentry falsa para los tests y para probar la
// importación a mano. Sirve las respuestas de testdata/sentry/<org>/ con la
// paginación por cursor y la cabecera Link de Sentry, sus cabeceras de
// límite y, si se pide, respuestas 429 con Retry-After.
package fake

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Server struct {
	// PageSize es cuántos elementos da cada página, pida lo que pida el cliente.
	PageSize int

	token string
	mu    sync.Mutex
	files map[string]json.RawMessage
	// throttle son las próximas respuestas que serán un 429.
	throttle   int
	retryAfter string
	requests   []Request
	mux        *http.ServeMux
}

// Request es una petición recibida, para que los tests la revisen.
type Request struct {
	Path, Query   string
	Authorization string
	Status        int
}

// New carga los ficheros JSON de dir (testdata/sentry). token es el que
// acepta en Authorization: Bearer.
func New(dir, token string) (*Server, error) {
	s := &Server{PageSize: 2, token: token, files: map[string]json.RawMessage{}}
	orgs, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, org := range orgs {
		if !org.IsDir() {
			continue
		}
		names, err := filepath.Glob(filepath.Join(dir, org.Name(), "*.json"))
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			b, err := os.ReadFile(n)
			if err != nil {
				return nil, err
			}
			s.files[org.Name()+"/"+strings.TrimSuffix(filepath.Base(n), ".json")] = b
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/0/organizations/{org}/projects/", s.projects)
	mux.HandleFunc("GET /api/0/organizations/{org}/releases/", s.releases)
	mux.HandleFunc("GET /api/0/organizations/{org}/releases/{version}/deploys/", s.deploys)
	mux.HandleFunc("GET /api/0/projects/{org}/{project}/issues/", s.issues)
	mux.HandleFunc("GET /api/0/organizations/{org}/issues/{id}/events/", s.events)
	s.mux = mux
	return s, nil
}

// Throttle hace que las próximas n respuestas sean 429 con ese Retry-After.
func (s *Server) Throttle(n int, retryAfter string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.throttle, s.retryAfter = n, retryAfter
}

// Requests devuelve las peticiones recibidas hasta ahora.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.requests...)
}

// Edit cambia el fichero org/name (sin .json): fn recibe la lista y la devuelve cambiada.
func (s *Server) Edit(name string, fn func([]map[string]any) []map[string]any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var list []map[string]any
	if err := json.Unmarshal(s.files[name], &list); err != nil {
		return err
	}
	b, err := json.Marshal(fn(list))
	if err != nil {
		return err
	}
	s.files[name] = b
	return nil
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rec := &statusWriter{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		s.mu.Lock()
		s.requests = append(s.requests, Request{Path: r.URL.Path, Query: r.URL.RawQuery,
			Authorization: r.Header.Get("Authorization"), Status: rec.status})
		s.mu.Unlock()
	}()
	if r.Header.Get("Authorization") != "Bearer "+s.token {
		writeJSON(rec, http.StatusUnauthorized, map[string]string{"detail": "Invalid token"})
		return
	}
	s.mu.Lock()
	throttled := s.throttle > 0
	if throttled {
		s.throttle--
	}
	retry := s.retryAfter
	s.mu.Unlock()
	reset := strconv.FormatInt(time.Now().Add(time.Second).Unix(), 10)
	rec.Header().Set("X-Sentry-Rate-Limit-Limit", "40")
	rec.Header().Set("X-Sentry-Rate-Limit-Reset", reset)
	rec.Header().Set("X-Sentry-Rate-Limit-ConcurrentLimit", "25")
	rec.Header().Set("X-Sentry-Rate-Limit-ConcurrentRemaining", "24")
	if throttled {
		rec.Header().Set("X-Sentry-Rate-Limit-Remaining", "0")
		rec.Header().Set("Retry-After", retry)
		writeJSON(rec, http.StatusTooManyRequests, map[string]string{"detail": "You are attempting to use this endpoint too frequently. Limit is 40 requests in 1 seconds"})
		return
	}
	rec.Header().Set("X-Sentry-Rate-Limit-Remaining", "39")
	s.mux.ServeHTTP(rec, r)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) list(name string) ([]json.RawMessage, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.files[name]
	if !ok {
		return nil, false
	}
	var list []json.RawMessage
	if json.Unmarshal(b, &list) != nil {
		return nil, false
	}
	return list, true
}

func notFound(w http.ResponseWriter) {
	writeJSON(w, http.StatusNotFound, map[string]string{"detail": "The requested resource does not exist"})
}

// page responde con una página de list y el Link de Sentry: cursor
// "<tamaño>:<desplazamiento>:<es la anterior>", con results="true" o "false".
func (s *Server) page(w http.ResponseWriter, r *http.Request, list []json.RawMessage) {
	size := s.PageSize
	offset := 0
	if c := r.URL.Query().Get("cursor"); c != "" {
		parts := strings.Split(c, ":")
		if len(parts) == 3 {
			offset, _ = strconv.Atoi(parts[1])
		}
	}
	offset = min(max(offset, 0), len(list))
	end := min(offset+size, len(list))
	link := func(rel string, off int, prev, results bool) string {
		u := *r.URL
		u.Scheme, u.Host = "http", r.Host
		q := u.Query()
		p := 0
		if prev {
			p = 1
		}
		cursor := fmt.Sprintf("%d:%d:%d", size, off, p)
		q.Set("cursor", cursor)
		u.RawQuery = q.Encode()
		return fmt.Sprintf(`<%s>; rel="%s"; results="%t"; cursor="%s"`, u.String(), rel, results, cursor)
	}
	w.Header().Set("Link", link("previous", max(offset-size, 0), true, offset > 0)+", "+link("next", end, false, end < len(list)))
	page := list[offset:end]
	if page == nil {
		page = []json.RawMessage{}
	}
	writeJSON(w, http.StatusOK, page)
}

func (s *Server) projects(w http.ResponseWriter, r *http.Request) {
	list, ok := s.list(r.PathValue("org") + "/projects")
	if !ok {
		notFound(w)
		return
	}
	s.page(w, r, list)
}

func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	list, ok := s.list(r.PathValue("org") + "/releases")
	if !ok {
		notFound(w)
		return
	}
	project := r.URL.Query().Get("project")
	var out []json.RawMessage
	for _, raw := range list {
		var rel struct {
			Projects []struct {
				ID json.Number `json:"id"`
			} `json:"projects"`
		}
		_ = json.Unmarshal(raw, &rel)
		for _, p := range rel.Projects {
			if project == "" || p.ID.String() == project {
				out = append(out, raw)
				break
			}
		}
	}
	s.page(w, r, out)
}

func (s *Server) deploys(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	b, ok := s.files[r.PathValue("org")+"/deploys"]
	s.mu.Unlock()
	var all map[string][]json.RawMessage
	if !ok || json.Unmarshal(b, &all) != nil {
		notFound(w)
		return
	}
	list, ok := all[r.PathValue("version")]
	if !ok {
		notFound(w)
		return
	}
	s.page(w, r, list)
}

// issues filtra como Sentry: sin query, solo los no resueltos; con
// lastSeen:>=<fecha>, los vistos desde entonces.
func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	list, ok := s.list(r.PathValue("org") + "/issues-" + r.PathValue("project"))
	if !ok {
		notFound(w)
		return
	}
	q := r.URL.Query()
	query := "is:unresolved"
	if q.Has("query") {
		query = q.Get("query")
	}
	var out []json.RawMessage
	for _, raw := range list {
		var is struct {
			Status   string    `json:"status"`
			LastSeen time.Time `json:"lastSeen"`
		}
		_ = json.Unmarshal(raw, &is)
		keep := true
		for term := range strings.FieldsSeq(query) {
			switch {
			case term == "is:unresolved":
				keep = keep && is.Status == "unresolved"
			case strings.HasPrefix(term, "lastSeen:>="):
				since, err := time.Parse(time.RFC3339, strings.TrimPrefix(term, "lastSeen:>="))
				if err != nil {
					writeJSON(w, http.StatusBadRequest, map[string]string{"detail": "Error parsing search query"})
					return
				}
				keep = keep && !is.LastSeen.Before(since)
			}
		}
		if keep {
			out = append(out, raw)
		}
	}
	s.page(w, r, out)
}

// events quita entries si no se pide full=true, como Sentry.
func (s *Server) events(w http.ResponseWriter, r *http.Request) {
	list, ok := s.list(r.PathValue("org") + "/events-" + r.PathValue("id"))
	if !ok {
		notFound(w)
		return
	}
	if r.URL.Query().Get("full") != "true" && r.URL.Query().Get("full") != "1" {
		out := make([]json.RawMessage, len(list))
		for i, raw := range list {
			var m map[string]json.RawMessage
			_ = json.Unmarshal(raw, &m)
			delete(m, "entries")
			out[i], _ = json.Marshal(m)
		}
		list = out
	}
	s.page(w, r, list)
}
