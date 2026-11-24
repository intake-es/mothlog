package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

func (h *handlers) registerUptime(route func(string, http.HandlerFunc)) {
	route("GET /api/0/uptime-monitors/{$}", h.uptimeMonitors)
	route("GET /api/0/projects/{id}/uptime-monitors/{$}", h.uptimeMonitors)
	route("POST /api/0/projects/{id}/uptime-monitors/{$}", h.createUptime)
	route("GET /api/0/projects/{id}/uptime-monitors/{slug}/{$}", h.uptimeMonitor)
	route("PATCH /api/0/projects/{id}/uptime-monitors/{slug}/{$}", h.updateUptime)
	route("DELETE /api/0/projects/{id}/uptime-monitors/{slug}/{$}", h.deleteUptime)
}

func (h *handlers) uptimeMonitors(w http.ResponseWriter, r *http.Request) {
	project, ok := projectParam(w, r)
	if !ok {
		return
	}
	list, err := h.st.ListUptimeMonitors(r.Context(),
		store.UptimeFilter{Project: project, Status: r.URL.Query().Get("status")}, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": list})
}

func (h *handlers) uptimeMonitor(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	m, err := h.st.UptimeMonitor(r.Context(), project, r.PathValue("slug"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	d, err := h.st.UptimeDetail(r.Context(), m, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func decodeUptime(w http.ResponseWriter, r *http.Request) (store.UptimeInput, bool) {
	var in store.UptimeInput
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return in, false
	}
	return in, true
}

func (h *handlers) createUptime(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	in, ok := decodeUptime(w, r)
	if !ok {
		return
	}
	m, err := h.st.CreateUptimeMonitor(r.Context(), project, in)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, m)
}

// updateUptime cambia los campos que trae el cuerpo; {"paused": true} lo pausa.
func (h *handlers) updateUptime(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	in, ok := decodeUptime(w, r)
	if !ok {
		return
	}
	m, err := h.st.UpdateUptimeMonitor(r.Context(), project, r.PathValue("slug"), in, time.Now())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, m)
}

func (h *handlers) deleteUptime(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	if err := h.st.DeleteUptimeMonitor(r.Context(), project, r.PathValue("slug")); err != nil {
		h.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
