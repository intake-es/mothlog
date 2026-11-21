// Package api sirve la API REST bajo /api/0/ y el MCP en /mcp.
package api

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/mcpserver"
	"github.com/intake-es/intake-errors/internal/store"
)

type Options struct {
	// PublicURL es la URL pública del servicio, para los DSN. Vacía, se saca de la petición.
	PublicURL string
	// Auth envuelve cada ruta. Por defecto, RequireAuth(BearerToken(st)).
	Auth func(http.Handler) http.Handler
}

type handlers struct {
	st        *store.Store
	publicURL *url.URL
}

// Register añade las rutas de la API y del MCP a mux.
func Register(mux *http.ServeMux, st *store.Store, opts Options) error {
	h := &handlers{st: st}
	if opts.PublicURL != "" {
		u, err := url.Parse(opts.PublicURL)
		if err != nil || u.Host == "" {
			return errors.New("PUBLIC_URL no es una URL absoluta")
		}
		h.publicURL = u
	}
	auth := opts.Auth
	if auth == nil {
		auth = RequireAuth(BearerToken(st))
	}
	route := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, auth(f)) }

	route("GET /api/0/projects/{$}", h.projects)
	for _, p := range []string{"/api/0/projects/{id}/filters", "/api/0/projects/{id}/filters/{$}"} {
		route("GET "+p, h.filters)
		route("PATCH "+p, h.setFilters)
	}
	route("GET /api/0/projects/{id}/dropped", h.dropped)
	route("GET /api/0/projects/{id}/dropped/{$}", h.dropped)
	route("GET /api/0/issues/{$}", h.issues)
	route("GET /api/0/issues/{id}/{$}", h.issue)
	route("GET /api/0/issues/{id}/events/{$}", h.issueEvents)
	route("GET /api/0/issues/{id}/stacktrace.md", h.stacktrace)
	route("POST /api/0/issues/{id}/status", h.setStatus)
	route("GET /api/0/events/{id}/{$}", h.event)
	mux.Handle("/mcp", auth(mcpserver.Handler(st)))
	return nil
}

type projectJSON struct {
	ID             int64    `json:"id"`
	Name           string   `json:"name"`
	DSN            string   `json:"dsn"`
	LinearLabelIDs []string `json:"linear_label_ids"`
}

func (h *handlers) projects(w http.ResponseWriter, r *http.Request) {
	ps, err := h.st.ListProjects(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	base := h.publicURL
	if base == nil {
		base = &url.URL{Scheme: "http", Host: r.Host}
		if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
			base.Scheme = "https"
		}
	}
	out := make([]projectJSON, len(ps))
	for i, p := range ps {
		dsn := url.URL{Scheme: base.Scheme, User: url.User(p.PublicKey), Host: base.Host, Path: "/" + strconv.FormatInt(p.ID, 10)}
		labels := p.LinearLabelIDs
		if labels == nil {
			labels = []string{}
		}
		out[i] = projectJSON{ID: p.ID, Name: p.Name, DSN: dsn.String(), LinearLabelIDs: labels}
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *handlers) filters(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	f, err := h.st.Filters(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

// setFilters cambia solo los campos que trae el cuerpo; null en un número
// vuelve a su valor por defecto.
func (h *handlers) setFilters(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	f, err := h.st.Filters(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo no válido: "+err.Error())
		return
	}
	f, err = h.st.SetFilters(r.Context(), id, f)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (h *handlers) dropped(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	days, ok := intParam(w, r.URL.Query().Get("days"), "days")
	if !ok {
		return
	}
	if days == 0 {
		days = 30
	}
	st, err := h.st.DroppedSince(r.Context(), id, days, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}

func (h *handlers) issues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	limit, ok := intParam(w, q.Get("limit"), "limit")
	if !ok {
		return
	}
	page, err := h.st.ListIssues(r.Context(), store.IssueFilter{
		Project: q.Get("project"),
		Status:  q.Get("status"),
		Query:   q.Get("query"),
		Sort:    q.Get("sort"),
		Cursor:  q.Get("cursor"),
		Limit:   limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if q.Get("daily") != "1" {
		writeJSON(w, http.StatusOK, page)
		return
	}
	ids := make([]int64, len(page.Issues))
	for i, is := range page.Issues {
		ids[i] = is.ID
	}
	daily, err := h.st.DailyCounts(r.Context(), ids, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	items := make([]issueWithDaily, len(page.Issues))
	for i, is := range page.Issues {
		items[i] = issueWithDaily{Issue: is, Daily: daily[is.ID]}
	}
	writeJSON(w, http.StatusOK, struct {
		Issues     []issueWithDaily `json:"data"`
		NextCursor string           `json:"next_cursor,omitempty"`
	}{items, page.NextCursor})
}

// issueWithDaily es un issue de la lista con daily=1: sus eventos por día, como en el detalle.
type issueWithDaily struct {
	store.Issue
	Daily []store.DayCount `json:"daily"`
}

func (h *handlers) issue(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := h.st.IssueDetail(r.Context(), id, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handlers) issueEvents(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r.URL.Query().Get("limit"), "limit")
	if !ok {
		return
	}
	page, err := h.st.IssueEvents(r.Context(), id, r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *handlers) stacktrace(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := h.st.IssueDetail(r.Context(), id, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(markdown.Issue(d)))
}

func (h *handlers) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "el cuerpo debe ser {\"status\": \"...\"}")
		return
	}
	issue, err := h.st.SetIssueStatus(r.Context(), id, body.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, issue)
}

func (h *handlers) event(w http.ResponseWriter, r *http.Request) {
	e, err := h.st.Event(r.Context(), r.PathValue("id"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func idParam(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "no encontrado")
		return 0, false
	}
	return id, true
}

func intParam(w http.ResponseWriter, s, name string) (int, bool) {
	if s == "" {
		return 0, true
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		writeError(w, http.StatusBadRequest, name+" debe ser un número")
		return 0, false
	}
	return n, true
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no encontrado")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNoRelease):
		writeError(w, http.StatusConflict, err.Error())
	default:
		slog.ErrorContext(r.Context(), "api", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "error interno")
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
