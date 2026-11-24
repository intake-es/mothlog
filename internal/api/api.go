// Package api sirve la API REST bajo /api/0/ y el MCP en /mcp.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/intake-es/mothlog/internal/lang"
	"github.com/intake-es/mothlog/internal/markdown"
	"github.com/intake-es/mothlog/internal/mcpserver"
	"github.com/intake-es/mothlog/internal/search"
	"github.com/intake-es/mothlog/internal/store"
)

type Options struct {
	// PublicURL es la URL pública del servicio, para los DSN. Vacía, se saca de la petición.
	PublicURL string
	// Auth envuelve cada ruta. Por defecto, RequireAuth(BearerToken(st)).
	Auth func(http.Handler) http.Handler
	// MCPAuth envuelve /mcp; por defecto, Auth.
	MCPAuth func(http.Handler) http.Handler
	// Alerts comprueba y prueba las reglas de aviso; sin él, no se prueban.
	Alerts Alerts
	// Integrations da el estado de Linear y GitHub; sin él, no hay ruta.
	Integrations Integrations
	// Digests comprueba y manda el resumen; sin él, no se manda a mano.
	Digests Digests
	// Channels son los canales por defecto; sin él, ninguno está configurado.
	Channels Channels
	// Lang es el idioma del Markdown, del MCP y de llms.txt; vacío, inglés.
	Lang lang.Lang
}

type handlers struct {
	st        *store.Store
	publicURL *url.URL
	alerts    Alerts
	integ     Integrations
	digests   Digests
	channels  Channels
	lang      lang.Lang
}

// Register añade las rutas de la API y del MCP a mux.
func Register(mux *http.ServeMux, st *store.Store, opts Options) error {
	h := &handlers{st: st, alerts: opts.Alerts, integ: opts.Integrations, digests: opts.Digests, channels: opts.Channels, lang: opts.Lang}
	if opts.PublicURL != "" {
		u, err := url.Parse(opts.PublicURL)
		if err != nil || u.Host == "" {
			return errors.New("PUBLIC_URL is not an absolute URL")
		}
		h.publicURL = u
	}
	auth := opts.Auth
	if auth == nil {
		auth = RequireAuth(BearerToken(st))
	}
	mcpAuth := opts.MCPAuth
	if mcpAuth == nil {
		mcpAuth = auth
	}
	route := func(pattern string, f http.HandlerFunc) { mux.Handle(pattern, auth(f)) }

	route("GET /api/0/projects/{$}", h.projects)
	for _, p := range []string{"/api/0/projects/{id}/filters", "/api/0/projects/{id}/filters/{$}"} {
		route("GET "+p, h.filters)
		route("PATCH "+p, h.setFilters)
	}
	route("GET /api/0/projects/{id}/dropped", h.dropped)
	route("GET /api/0/projects/{id}/dropped/{$}", h.dropped)
	h.registerRules(route)
	h.registerDigest(route)
	h.registerDefaults(route)
	route("GET /api/0/issues/{$}", h.issues)
	route("GET /api/0/issues/{id}/{$}", h.issue)
	route("GET /api/0/issues/{id}/events/{$}", h.issueEvents)
	route("GET /api/0/issues/{id}/tags/{$}", h.issueTags)
	route("GET /api/0/tags/{$}", h.tagKeys)
	route("GET /api/0/tags/{key}/values/{$}", h.tagValues)
	route("GET /api/0/issues/{id}/stacktrace.md", h.stacktrace)
	route("POST /api/0/issues/{id}/status", h.setStatus)
	h.registerHistory(route)
	h.registerAssign(route)
	route("GET /api/0/events/{id}/{$}", h.event)
	h.registerReleases(route)
	h.registerMonitors(route)
	h.registerUptime(route)
	route("GET /api/0/integrations/{$}", h.integrations)
	mcpHandler := mcpserver.Handler(st, mcpserver.Options{
		Lang:   opts.Lang,
		Actor:  actorFrom,
		Scopes: scopesFrom,
		IssueURL: func(ctx context.Context, id int64) string {
			base, _ := ctx.Value(baseURLKey{}).(string)
			return base + "/issues/" + strconv.FormatInt(id, 10)
		},
		BaseURL: func(ctx context.Context) string {
			base, _ := ctx.Value(baseURLKey{}).(string)
			return base
		},
	})
	mux.Handle("/mcp", mcpAuth(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.WithValue(r.Context(), baseURLKey{}, h.baseURL(r).String())
		mcpHandler.ServeHTTP(w, r.WithContext(ctx))
	})))
	mux.HandleFunc("GET /llms.txt", h.llms)
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
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
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
		Project:     q.Get("project"),
		Status:      q.Get("status"),
		Query:       q.Get("query"),
		Environment: q.Get("environment"),
		Sort:        q.Get("sort"),
		Cursor:      q.Get("cursor"),
		Limit:       limit,
		Viewer:      actorFrom(r.Context()),
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
	daily, err := h.st.DailyCountsIn(r.Context(), ids, q.Get("environment"), time.Now())
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
	d, err := h.st.IssueDetailIn(r.Context(), id, r.URL.Query().Get("environment"), time.Now())
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
	page, err := h.st.IssueEventsIn(r.Context(), id, r.URL.Query().Get("environment"), r.URL.Query().Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func (h *handlers) issueTags(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	tags, err := h.st.IssueTags(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, tags)
}

func (h *handlers) tagKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.st.TagKeys(r.Context(), r.URL.Query().Get("project"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": keys})
}

func (h *handlers) tagValues(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	vals, err := h.st.TagValues(r.Context(), q.Get("project"), r.PathValue("key"), q.Get("prefix"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": vals})
}

func (h *handlers) stacktrace(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := h.st.IssueDetailIn(r.Context(), id, r.URL.Query().Get("environment"), time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(markdown.Issue(d, h.lang)))
}

func (h *handlers) setStatus(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Status  string `json:"status"`
		Comment string `json:"comment"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be {\"status\": \"...\"}")
		return
	}
	issue, err := h.st.ChangeIssueStatus(r.Context(), id, body.Status, store.StatusChange{
		Actor: actorFrom(r.Context()), Source: sourceFrom(r.Context()), Comment: body.Comment,
	})
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
		writeError(w, http.StatusNotFound, "not found")
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
		writeError(w, http.StatusBadRequest, name+" must be a number")
		return 0, false
	}
	return n, true
}

func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	var qe *search.Error
	switch {
	case errors.As(err, &qe):
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": qe.Error(), "query_error": qe})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrInvalid):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrNoRelease):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrNotOwner):
		writeError(w, http.StatusForbidden, err.Error())
	default:
		slog.ErrorContext(r.Context(), "api", "path", r.URL.Path, "err", err)
		writeError(w, http.StatusInternalServerError, "internal error")
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
