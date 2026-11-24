package api

import (
	"context"
	_ "embed"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/markdown"
	"github.com/intake-es/intake-errors/internal/store"
)

func (h *handlers) registerHistory(route func(string, http.HandlerFunc)) {
	route("GET /api/0/issues/{id}/history/{$}", h.history)
	route("POST /api/0/issues/{id}/comments/{$}", h.addComment)
	route("DELETE /api/0/issues/{id}/comments/{comment}", h.deleteComment)
	route("DELETE /api/0/issues/{id}/comments/{comment}/{$}", h.deleteComment)
	route("GET /api/0/issues/{id}/context.md", h.contextMD)
}

// actorFrom es el autor de lo que hace la petición: el usuario de la sesión o
// el nombre del token.
func actorFrom(ctx context.Context) *store.Actor {
	p, ok := PrincipalFrom(ctx)
	if !ok || p.Name == "" {
		return nil
	}
	switch p.Kind {
	case "session":
		return &store.Actor{Name: p.Name, Kind: store.ActorUser}
	case "oauth":
		return &store.Actor{Name: p.Name, Kind: store.ActorUser, Via: p.Via}
	}
	return &store.Actor{Name: p.Name, Kind: store.ActorToken}
}

// scopesFrom son los permisos de un token de OAuth; nil, todos.
func scopesFrom(ctx context.Context) []string {
	if p, ok := PrincipalFrom(ctx); ok && p.Kind == "oauth" {
		if p.Scopes == nil {
			return []string{}
		}
		return p.Scopes
	}
	return nil
}

// sourceFrom: con sesión, la petición viene de la interfaz; con token, de la API.
func sourceFrom(ctx context.Context) string {
	if p, ok := PrincipalFrom(ctx); ok && p.Kind == "session" {
		return store.SourceUI
	}
	return store.SourceAPI
}

func (h *handlers) history(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	entries, err := h.st.History(r.Context(), id, actorFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": entries})
}

func (h *handlers) addComment(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Body string `json:"body"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<18)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "el cuerpo debe ser {\"body\": \"...\"}")
		return
	}
	a := actorFrom(r.Context())
	if a == nil {
		writeError(w, http.StatusForbidden, "no se sabe quién comenta")
		return
	}
	c, err := h.st.AddComment(r.Context(), id, *a, body.Body)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, c)
}

func (h *handlers) deleteComment(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	comment, err := strconv.ParseInt(r.PathValue("comment"), 10, 64)
	if err != nil || comment <= 0 {
		writeError(w, http.StatusNotFound, "no encontrado")
		return
	}
	a := actorFrom(r.Context())
	if a == nil {
		writeError(w, http.StatusForbidden, store.ErrNotOwner.Error())
		return
	}
	if err := h.st.DeleteComment(r.Context(), id, comment, *a); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handlers) contextMD(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	c, err := h.st.IssueContextIn(r.Context(), id, q.Get("environment"), q.Get("event"), actorFrom(r.Context()), time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	_, _ = w.Write([]byte(markdown.Context(c, h.issueURL(r, id), markdown.MaxContextBytes)))
}

// baseURL es PUBLIC_URL o, sin ella, la de la petición.
func (h *handlers) baseURL(r *http.Request) *url.URL {
	if h.publicURL != nil {
		u := *h.publicURL
		u.Path = strings.TrimSuffix(u.Path, "/")
		return &u
	}
	u := &url.URL{Scheme: "http", Host: r.Host}
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		u.Scheme = "https"
	}
	return u
}

type baseURLKey struct{}

func (h *handlers) issueURL(r *http.Request, id int64) string {
	return h.baseURL(r).String() + "/issues/" + strconv.FormatInt(id, 10)
}

//go:embed llms.txt
var llmsTxt string

// llms es /llms.txt: qué es el servicio y cómo usarlo, sin token.
func (h *handlers) llms(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte(strings.ReplaceAll(llmsTxt, "{base}", h.baseURL(r).String())))
}
