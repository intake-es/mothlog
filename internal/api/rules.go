package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/intake-es/mothlog/internal/alert"
	"github.com/intake-es/mothlog/internal/store"
)

// Alerts comprueba y prueba las reglas de aviso.
type Alerts interface {
	CheckRule(store.Rule) error
	TestAction(ctx context.Context, r store.Rule, actionID string) error
	// RulesChanged se llama tras crear, editar o borrar una regla del proyecto.
	RulesChanged(projectID int64)
}

func (h *handlers) registerRules(route func(string, http.HandlerFunc)) {
	for _, p := range []string{"/api/0/projects/{id}/rules", "/api/0/projects/{id}/rules/{$}"} {
		route("GET "+p, h.rules)
		route("POST "+p, h.createRule)
	}
	for _, p := range []string{"/api/0/projects/{id}/rules/{rule}", "/api/0/projects/{id}/rules/{rule}/{$}"} {
		route("GET "+p, h.rule)
		route("PUT "+p, h.updateRule)
		route("DELETE "+p, h.deleteRule)
	}
	route("POST /api/0/projects/{id}/rules/{rule}/test", h.testRule)
}

func ruleIDs(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	project, ok := idParam(w, r)
	if !ok {
		return 0, 0, false
	}
	id, err := strconv.ParseInt(r.PathValue("rule"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusNotFound, "not found")
		return 0, 0, false
	}
	return project, id, true
}

func (h *handlers) rules(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	rules, err := h.st.Rules(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]store.Rule, len(rules))
	for i, ru := range rules {
		out[i] = ru.Masked()
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

func (h *handlers) rule(w http.ResponseWriter, r *http.Request) {
	project, id, ok := ruleIDs(w, r)
	if !ok {
		return
	}
	ru, err := h.st.Rule(r.Context(), project, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, ru.Masked())
}

func decodeRule(w http.ResponseWriter, r *http.Request) (store.Rule, bool) {
	ru := store.Rule{Enabled: true}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ru); err != nil {
		writeError(w, http.StatusBadRequest, "invalid body: "+err.Error())
		return ru, false
	}
	return ru, true
}

func (h *handlers) check(ru store.Rule) error {
	if h.alerts == nil {
		return nil
	}
	return h.alerts.CheckRule(ru)
}

func (h *handlers) changed(projectID int64) {
	if h.alerts != nil {
		h.alerts.RulesChanged(projectID)
	}
}

// createRule guarda la regla; el secreto del webhook que se genere se
// devuelve entero solo en esta respuesta.
func (h *handlers) createRule(w http.ResponseWriter, r *http.Request) {
	project, ok := idParam(w, r)
	if !ok {
		return
	}
	ru, ok := decodeRule(w, r)
	if !ok {
		return
	}
	if err := h.check(ru); err != nil {
		h.fail(w, r, err)
		return
	}
	out, generated, err := h.st.CreateRule(r.Context(), project, ru)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.changed(project)
	writeJSON(w, http.StatusCreated, out.Masked(generated...))
}

// updateRule sustituye la regla entera; los secretos que llegan vacíos o tapados no cambian.
func (h *handlers) updateRule(w http.ResponseWriter, r *http.Request) {
	project, id, ok := ruleIDs(w, r)
	if !ok {
		return
	}
	ru, ok := decodeRule(w, r)
	if !ok {
		return
	}
	ru.ID = id
	if err := h.check(ru); err != nil {
		h.fail(w, r, err)
		return
	}
	out, generated, err := h.st.UpdateRule(r.Context(), project, ru)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.changed(project)
	writeJSON(w, http.StatusOK, out.Masked(generated...))
}

func (h *handlers) deleteRule(w http.ResponseWriter, r *http.Request) {
	project, id, ok := ruleIDs(w, r)
	if !ok {
		return
	}
	if err := h.st.DeleteRule(r.Context(), project, id); err != nil {
		h.fail(w, r, err)
		return
	}
	h.changed(project)
	w.WriteHeader(http.StatusNoContent)
}

// testRule manda un aviso de prueba por una acción de la regla guardada.
func (h *handlers) testRule(w http.ResponseWriter, r *http.Request) {
	project, id, ok := ruleIDs(w, r)
	if !ok {
		return
	}
	var body struct {
		ActionID string `json:"action_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body.ActionID == "" {
		writeError(w, http.StatusBadRequest, "the body must be {\"action_id\": \"...\"}")
		return
	}
	if h.alerts == nil {
		writeError(w, http.StatusServiceUnavailable, "alerts are not active on this server")
		return
	}
	ru, err := h.st.Rule(r.Context(), project, id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	err = h.alerts.TestAction(r.Context(), ru, body.ActionID)
	switch {
	case err == nil:
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	case errors.Is(err, alert.ErrTestFailed):
		writeError(w, http.StatusBadGateway, err.Error())
	default:
		h.fail(w, r, err)
	}
}
