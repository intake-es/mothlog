package api

import (
	"encoding/json"
	"net/http"

	"github.com/intake-es/intake-errors/internal/store"
)

func (h *handlers) registerMonitors(route func(string, http.HandlerFunc)) {
	route("GET /api/0/monitors/{$}", h.monitors)
	route("GET /api/0/projects/{id}/monitors/{$}", h.monitors)
	route("POST /api/0/projects/{id}/monitors/{$}", h.createMonitor)
	route("GET /api/0/projects/{id}/monitors/{slug}/{$}", h.monitor)
	route("PATCH /api/0/projects/{id}/monitors/{slug}/{$}", h.updateMonitor)
	route("DELETE /api/0/projects/{id}/monitors/{slug}/{$}", h.deleteMonitor)
	route("GET /api/0/projects/{id}/monitors/{slug}/checkins/{$}", h.monitorCheckIns)
}

func (h *handlers) monitors(w http.ResponseWriter, r *http.Request) {
	project, ok := projectParam(w, r)
	if !ok {
		return
	}
	list, err := h.st.ListMonitors(r.Context(), store.MonitorFilter{Project: project, Status: r.URL.Query().Get("status")})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

// monitorDetail es el monitor con la primera página de su historial.
type monitorDetail struct {
	store.Monitor
	CheckIns   []store.CheckIn `json:"checkins"`
	NextCursor string          `json:"next_cursor,omitempty"`
}

func (h *handlers) monitor(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	m, err := h.st.Monitor(r.Context(), project, r.PathValue("slug"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	limit, ok := intParam(w, r.URL.Query().Get("limit"), "limit")
	if !ok {
		return
	}
	page, err := h.st.CheckIns(r.Context(), m.ID, "", limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, monitorDetail{Monitor: m, CheckIns: page.CheckIns, NextCursor: page.NextCursor})
}

func (h *handlers) monitorCheckIns(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	m, err := h.st.Monitor(r.Context(), project, r.PathValue("slug"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	q := r.URL.Query()
	limit, ok := intParam(w, q.Get("limit"), "limit")
	if !ok {
		return
	}
	page, err := h.st.CheckIns(r.Context(), m.ID, q.Get("cursor"), limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func decodeMonitor(w http.ResponseWriter, r *http.Request) (store.MonitorInput, bool) {
	var in store.MonitorInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "cuerpo no válido: "+err.Error())
		return in, false
	}
	return in, true
}

func (h *handlers) createMonitor(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	in, ok := decodeMonitor(w, r)
	if !ok {
		return
	}
	m, err := h.st.CreateMonitor(r.Context(), project, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// updateMonitor cambia los campos que trae el cuerpo; {"muted": true} lo silencia.
func (h *handlers) updateMonitor(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	in, ok := decodeMonitor(w, r)
	if !ok {
		return
	}
	m, err := h.st.UpdateMonitor(r.Context(), project, r.PathValue("slug"), in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *handlers) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	if err := h.st.DeleteMonitor(r.Context(), project, r.PathValue("slug")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
