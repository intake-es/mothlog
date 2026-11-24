package sourcemap

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// releaseJSON es una release como la devuelve Sentry a sentry-cli.
func releaseJSON(version string, rels []store.Release, projects []string) map[string]any {
	created := time.Now()
	var released, lastEvent *time.Time
	var newGroups int64
	deploys := 0
	for _, r := range rels {
		if r.FirstSeen.Before(created) {
			created = r.FirstSeen
		}
		if r.FinalizedAt != nil && (released == nil || r.FinalizedAt.Before(*released)) {
			released = r.FinalizedAt
		}
		if r.LastEvent != nil && (lastEvent == nil || r.LastEvent.After(*lastEvent)) {
			lastEvent = r.LastEvent
		}
		newGroups += r.NewIssues
		deploys += len(r.Deploys)
	}
	ps := make([]map[string]string, len(projects))
	for i, p := range projects {
		ps[i] = map[string]string{"slug": p, "name": p}
	}
	if released != nil {
		released = new(released.UTC())
	}
	if lastEvent != nil {
		lastEvent = new(lastEvent.UTC())
	}
	return map[string]any{
		"version": version, "shortVersion": version, "dateCreated": created.UTC(),
		"dateReleased": released, "url": nil, "ref": nil, "lastEvent": lastEvent, "newGroups": newGroups,
		"projects": ps, "commitCount": 0, "authors": []string{}, "deployCount": deploys,
	}
}

func notFoundErr(err error, format string, a ...any) error {
	if errors.Is(err, store.ErrNotFound) {
		return cliErr(http.StatusNotFound, format, a...)
	}
	if errors.Is(err, store.ErrInvalid) {
		return cliErr(http.StatusBadRequest, "%v", err)
	}
	return err
}

// newRelease crea la release en cada proyecto del cuerpo o de la ruta; si ya
// existía, la deja como estaba.
func (s *Service) newRelease(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Version  string   `json:"version"`
		Projects []string `json:"projects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return cliErr(http.StatusBadRequest, "JSON no válido: %v", err)
	}
	if req.Version == "" {
		return cliErr(http.StatusBadRequest, "falta version")
	}
	if p := r.PathValue("project"); p != "" {
		req.Projects = append(req.Projects, p)
	}
	ids, err := s.projectIDs(r, req.Projects)
	if err != nil {
		return err
	}
	rels := make([]store.Release, 0, len(ids))
	for _, id := range ids {
		rel, created, err := s.store.CreateRelease(r.Context(), id, req.Version)
		if err != nil {
			return notFoundErr(err, "proyecto no encontrado")
		}
		if created {
			s.log.InfoContext(r.Context(), "release creada", "project", rel.Project, "release", rel.Version)
		}
		rels = append(rels, rel)
	}
	writeJSON(w, http.StatusCreated, releaseJSON(req.Version, rels, req.Projects))
	return nil
}

// releaseProjects son los proyectos del cuerpo, o si no trae, los que ya
// tienen la release declarada con un token (`sentry-cli deploys new` no manda
// proyectos). Las releases que solo ha creado la ingesta no cuentan.
func (s *Service) releaseProjects(r *http.Request, version string, names []string) ([]int64, []string, error) {
	if p := r.PathValue("project"); p != "" {
		names = append(names, p)
	}
	if len(names) > 0 {
		ids, err := s.projectIDs(r, names)
		return ids, names, err
	}
	ps, err := s.store.ReleaseProjects(r.Context(), version)
	if err != nil {
		return nil, nil, err
	}
	if len(ps) == 0 {
		return nil, nil, cliErr(http.StatusNotFound, "no hay ninguna release %q", version)
	}
	ids := make([]int64, len(ps))
	names = make([]string, len(ps))
	for i, p := range ps {
		ids[i], names[i] = p.ID, p.Name
	}
	return ids, names, nil
}

// finalizeRelease es `sentry-cli releases finalize`: marca la release como
// terminada en dateReleased, o ahora.
func (s *Service) finalizeRelease(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Projects     []string   `json:"projects"`
		DateReleased *time.Time `json:"dateReleased"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return cliErr(http.StatusBadRequest, "JSON no válido: %v", err)
	}
	version := r.PathValue("version")
	ids, names, err := s.releaseProjects(r, version, req.Projects)
	if err != nil {
		return err
	}
	at := time.Now()
	if req.DateReleased != nil {
		at = *req.DateReleased
	}
	rels := make([]store.Release, 0, len(ids))
	for _, id := range ids {
		rel, err := s.store.FinalizeRelease(r.Context(), id, version, at)
		if err != nil {
			return notFoundErr(err, "proyecto no encontrado")
		}
		rels = append(rels, rel)
	}
	writeJSON(w, http.StatusOK, releaseJSON(version, rels, names))
	return nil
}

// newDeploy es `sentry-cli deploys new`: apunta el despliegue en todos los
// proyectos que tienen la release declarada, o en los del cuerpo si los trae.
func (s *Service) newDeploy(w http.ResponseWriter, r *http.Request) error {
	var req struct {
		Environment  string     `json:"environment"`
		Name         *string    `json:"name"`
		URL          *string    `json:"url"`
		DateStarted  *time.Time `json:"dateStarted"`
		DateFinished *time.Time `json:"dateFinished"`
		Projects     []string   `json:"projects"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		return cliErr(http.StatusBadRequest, "JSON no válido: %v", err)
	}
	if req.Environment == "" {
		return cliErr(http.StatusBadRequest, "falta environment")
	}
	version := r.PathValue("version")
	ids, names, err := s.releaseProjects(r, version, req.Projects)
	if err != nil {
		return err
	}
	at := time.Now()
	if req.DateFinished != nil {
		at = *req.DateFinished
	}
	var d store.Deploy
	for i, id := range ids {
		var resolved []int64
		d, resolved, err = s.store.AddDeploy(r.Context(), id, version, req.Environment, at)
		if err != nil {
			return notFoundErr(err, "proyecto no encontrado")
		}
		s.log.InfoContext(r.Context(), "despliegue apuntado", "project", names[i], "release", version,
			"environment", req.Environment, "resolved_issues", len(resolved))
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id": strconv.FormatInt(d.ID, 10), "environment": d.Environment, "name": req.Name, "url": req.URL,
		"dateStarted": req.DateStarted, "dateFinished": d.DeployedAt.UTC(),
	})
	return nil
}
