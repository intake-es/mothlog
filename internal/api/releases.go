package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

func (h *handlers) registerReleases(route func(string, http.HandlerFunc)) {
	route("GET /api/0/environments/{$}", h.environments)
	route("GET /api/0/projects/{id}/environments/{$}", h.environments)
	route("GET /api/0/releases/{$}", h.releases)
	route("GET /api/0/projects/{id}/releases/{$}", h.releases)
	route("POST /api/0/projects/{id}/releases/{$}", h.createRelease)
	route("GET /api/0/projects/{id}/releases/{version}/{$}", h.release)
	route("GET /api/0/projects/{id}/releases/{version}/deploys/{$}", h.deploys)
	route("POST /api/0/projects/{id}/releases/{version}/deploys/{$}", h.createDeploy)
}

// projectParam es el {id} de la ruta, o el ?project= si la ruta no lo lleva.
func projectParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.PathValue("id") == "" {
		return r.URL.Query().Get("project"), true
	}
	id, ok := idParam(w, r)
	if !ok {
		return "", false
	}
	return strconv.FormatInt(id, 10), true
}

func (h *handlers) environments(w http.ResponseWriter, r *http.Request) {
	project, ok := projectParam(w, r)
	if !ok {
		return
	}
	envs, err := h.st.Environments(r.Context(), project)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": envs})
}

func (h *handlers) releases(w http.ResponseWriter, r *http.Request) {
	project, ok := projectParam(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	limit, ok := intParam(w, q.Get("limit"), "limit")
	if !ok {
		return
	}
	page, err := h.st.ListReleases(r.Context(), store.ReleaseFilter{
		Project:     project,
		Environment: q.Get("environment"),
		Cursor:      q.Get("cursor"),
		Limit:       limit,
	})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

type deployBody struct {
	Environment string    `json:"environment"`
	DeployedAt  time.Time `json:"deployed_at"`
}

// releaseCreated es la release creada o que ya existía y, si el cuerpo traía
// entorno, su despliegue y los issues que resolvió.
type releaseCreated struct {
	store.Release
	Deploy   *store.Deploy `json:"deploy,omitempty"`
	Resolved []int64       `json:"resolved_issues,omitempty"`
}

// createRelease crea la release {version} (201, o 200 si ya existía). Con
// environment apunta además un despliegue en ese entorno.
func (h *handlers) createRelease(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body struct {
		Version string `json:"version"`
		deployBody
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, `el cuerpo debe ser {"version": "...", "environment": "..."}`)
		return
	}
	if _, err := h.st.ProjectByID(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	rel, created, err := h.st.CreateRelease(r.Context(), id, body.Version)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := releaseCreated{Release: rel}
	if body.Environment != "" {
		d, resolved, err := h.st.AddDeploy(r.Context(), id, rel.Version, body.Environment, body.DeployedAt)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		out.Deploy, out.Resolved = &d, resolved
		if out.Release, err = h.st.Release(r.Context(), id, rel.Version, ""); err != nil {
			h.fail(w, r, err)
			return
		}
	}
	code := http.StatusOK
	if created {
		code = http.StatusCreated
	}
	writeJSON(w, code, out)
}

func (h *handlers) release(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	d, err := h.st.ReleaseDetail(r.Context(), id, r.PathValue("version"), r.URL.Query().Get("environment"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func (h *handlers) deploys(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	rel, err := h.st.Release(r.Context(), id, r.PathValue("version"), r.URL.Query().Get("environment"))
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": rel.Deploys})
}

// createDeploy apunta un despliegue de {version}, que se crea si no existía.
func (h *handlers) createDeploy(w http.ResponseWriter, r *http.Request) {
	id, ok := idParam(w, r)
	if !ok {
		return
	}
	var body deployBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, `el cuerpo debe ser {"environment": "..."}`)
		return
	}
	if _, err := h.st.ProjectByID(r.Context(), id); err != nil {
		h.fail(w, r, err)
		return
	}
	d, resolved, err := h.st.AddDeploy(r.Context(), id, r.PathValue("version"), body.Environment, body.DeployedAt)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusCreated, struct {
		store.Deploy
		Resolved []int64 `json:"resolved_issues"`
	}{d, nonNil(resolved)})
}

func nonNil(ids []int64) []int64 {
	if ids == nil {
		return []int64{}
	}
	return ids
}
