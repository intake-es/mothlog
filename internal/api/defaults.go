package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"slices"

	"github.com/intake-es/intake-errors/internal/alert"
	"github.com/intake-es/intake-errors/internal/store"
)

// Channels son los canales por defecto del servidor (Slack y Linear) y la
// issue de Linear que se crea a mano.
type Channels interface {
	// Channels son los canales configurados en el servidor.
	Channels() []string
	// DefaultsChanged se llama tras cambiar los canales por defecto de un proyecto.
	DefaultsChanged(projectID int64)
	CreateLinear(ctx context.Context, issueID int64, by store.Actor) (store.IssueLink, bool, error)
}

func (h *handlers) registerDefaults(route func(string, http.HandlerFunc)) {
	for _, p := range []string{"/api/0/projects/{id}/default-alerts", "/api/0/projects/{id}/default-alerts/{$}"} {
		route("GET "+p, h.defaultAlerts)
		route("PATCH "+p, h.setDefaultAlerts)
	}
	route("POST /api/0/issues/{id}/linear", h.createLinear)
	route("POST /api/0/issues/{id}/linear/{$}", h.createLinear)
}

// defaultAlertsJSON son los canales por defecto del proyecto, los que tiene
// configurados el servidor y si el proyecto tiene reglas, con las que no cuentan.
type defaultAlertsJSON struct {
	store.DefaultAlerts
	Configured []string `json:"configured"`
	HasRules   bool     `json:"has_rules"`
}

func (h *handlers) writeDefaults(w http.ResponseWriter, r *http.Request, id int64, d store.DefaultAlerts) {
	has, err := h.st.HasIssueRules(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	configured := []string{}
	if h.channels != nil {
		configured = append(configured, h.channels.Channels()...)
	}
	writeJSON(w, http.StatusOK, defaultAlertsJSON{DefaultAlerts: d, Configured: configured, HasRules: has})
}

func (h *handlers) defaultAlerts(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := h.st.DefaultAlerts(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.writeDefaults(w, r, id, d)
}

// setDefaultAlerts cambia solo los canales que trae el cuerpo: {"linear": false}.
func (h *handlers) setDefaultAlerts(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil || body == nil {
		writeError(w, http.StatusBadRequest, `el cuerpo debe ser {"slack": true|false, "linear": true|false}`)
		return
	}
	d, err := h.st.DefaultAlerts(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	for k, raw := range body {
		var v *bool
		if err := json.Unmarshal(raw, &v); err != nil || v == nil {
			writeError(w, http.StatusBadRequest, k+" debe ser true o false")
			return
		}
		switch k {
		case "slack":
			d.Slack = *v
		case "linear":
			d.Linear = *v
		default:
			writeError(w, http.StatusBadRequest, "canal desconocido: "+k+"; son slack y linear")
			return
		}
	}
	if d, err = h.st.SetDefaultAlerts(r.Context(), id, d); err != nil {
		h.fail(w, r, err)
		return
	}
	if h.channels != nil {
		h.channels.DefaultsChanged(id)
	}
	h.writeDefaults(w, r, id, d)
}

// createLinear crea la issue de Linear del error y la enlaza; si ya tiene
// una, la devuelve con 200 en vez de 201.
func (h *handlers) createLinear(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	if h.channels == nil || !slices.Contains(h.channels.Channels(), "linear") {
		writeError(w, http.StatusConflict, alert.ErrLinearOff.Error())
		return
	}
	a := actorFrom(r.Context())
	if a == nil {
		writeError(w, http.StatusForbidden, "no se sabe quién la crea")
		return
	}
	link, created, err := h.channels.CreateLinear(r.Context(), id, *a)
	var le *alert.LinearError
	switch {
	case errors.As(err, &le):
		slog.ErrorContext(r.Context(), "crear issue de Linear", "issue", id, "err", le.Err)
		writeError(w, http.StatusBadGateway, "Linear no ha podido crear la issue; prueba otra vez en un rato")
		return
	case errors.Is(err, alert.ErrLinearOff):
		writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		h.fail(w, r, err)
		return
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, link)
}
