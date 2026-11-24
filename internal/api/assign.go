package api

import (
	"encoding/json"
	"net/http"

	"github.com/intake-es/mothlog/internal/store"
)

func (h *handlers) registerAssign(route func(string, http.HandlerFunc)) {
	route("GET /api/0/assignees/{$}", h.assignees)
	route("POST /api/0/issues/{id}/assignee", h.assign)
	route("DELETE /api/0/issues/{id}/assignee", h.unassign)
}

func (h *handlers) assignees(w http.ResponseWriter, r *http.Request) {
	out, err := h.st.Assignees(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": out})
}

// assign recibe {"assignee": "<login, nombre del token o me>", "kind": "user|token"};
// kind solo hace falta si el nombre es de una persona y de un token. Un
// assignee vacío o null desasigna.
func (h *handlers) assign(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Assignee *string `json:"assignee"`
		Kind     string  `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be {\"assignee\": \"...\"}")
		return
	}
	name := ""
	if body.Assignee != nil {
		name = *body.Assignee
	}
	to, err := store.AssigneeRef(name, body.Kind, actorFrom(r.Context()))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.setAssignee(w, r, id, to)
}

func (h *handlers) unassign(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	h.setAssignee(w, r, id, nil)
}

func (h *handlers) setAssignee(w http.ResponseWriter, r *http.Request, id int64, to *store.Actor) {
	issue, err := h.st.AssignIssue(r.Context(), id, to, store.AssignChange{
		Actor: actorFrom(r.Context()), Source: sourceFrom(r.Context()),
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, issue)
}
