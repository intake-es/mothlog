package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/digest"
	"github.com/intake-es/intake-errors/internal/store"
)

// Digests comprueba y manda el resumen de un proyecto.
type Digests interface {
	CheckDigest(store.DigestConfig) error
	SendDigestNow(ctx context.Context, projectID int64) ([]alert.DigestResult, error)
}

func (h *handlers) registerDigest(route func(string, http.HandlerFunc)) {
	for _, p := range []string{"/api/0/projects/{id}/digest", "/api/0/projects/{id}/digest/{$}"} {
		route("GET "+p, h.digest)
		route("PUT "+p, h.setDigest)
	}
	route("GET /api/0/projects/{id}/digest/preview", h.digestPreview)
	route("POST /api/0/projects/{id}/digest/send", h.sendDigest)
}

func (h *handlers) digest(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	c, err := h.st.Digest(r.Context(), id, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, c.Masked())
}

// setDigest sustituye la configuración; los secretos que llegan vacíos o
// tapados no cambian y el del webhook que se genere se devuelve entero solo
// en esta respuesta.
func (h *handlers) setDigest(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	in := store.DefaultDigest(id)
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo no válido: "+err.Error())
		return
	}
	if h.digests != nil {
		if err := h.digests.CheckDigest(in); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	out, generated, err := h.st.SetDigest(r.Context(), id, in, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, out.Masked(generated...))
}

type digestPreview struct {
	Subject  string         `json:"subject"`
	HTML     string         `json:"html"`
	Text     string         `json:"text"`
	Markdown string         `json:"markdown"`
	Digest   *digest.Digest `json:"digest"`
}

// digestPreview arma el resumen del periodo que acaba ahora con la
// configuración guardada; language cambia el idioma.
func (h *handlers) digestPreview(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	now := time.Now()
	c, err := h.st.Digest(r.Context(), id, now)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	if l := q.Get("language"); l != "" {
		c.Language = l
	}
	if f := q.Get("frequency"); f != "" {
		c.Frequency = f
	}
	if (c.Language != digest.Spanish && c.Language != digest.English) ||
		(c.Frequency != store.DigestDaily && c.Frequency != store.DigestWeekly) {
		writeError(w, http.StatusBadRequest, "language es es o en, y frequency, daily o weekly")
		return
	}
	d, err := digest.Build(r.Context(), h.st, digest.Options{Config: c, Start: now.Add(-c.Length()), End: now,
		Base: strings.TrimRight(h.baseURL(r).String(), "/")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, digestPreview{Subject: d.Subject(), HTML: d.HTML(), Text: d.Text(), Markdown: d.Markdown(), Digest: d})
}

// sendDigest manda ya el resumen del periodo que acaba ahora a cada destino guardado.
func (h *handlers) sendDigest(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if h.digests == nil {
		writeError(w, http.StatusServiceUnavailable, "los avisos no están activos en este servidor")
		return
	}
	res, err := h.digests.SendDigestNow(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": res})
}
