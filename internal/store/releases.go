package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// MaxReleaseIssues es cuántos issues de cada grupo trae el detalle de una release.
const MaxReleaseIssues = 100

const maxNameLen = 200

// Querier es lo que comparten el pool y una transacción.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type Deploy struct {
	ID          int64     `json:"id"`
	Environment string    `json:"environment"`
	DeployedAt  time.Time `json:"deployed_at"`
}

type Release struct {
	ID          int64      `json:"id"`
	ProjectID   int64      `json:"project_id"`
	Project     string     `json:"project"`
	Version     string     `json:"version"`
	FirstSeen   time.Time  `json:"first_seen"`
	FinalizedAt *time.Time `json:"finalized_at"`
	Deploys     []Deploy   `json:"deploys"`
	// NewIssues son los issues que aparecieron con esta release.
	NewIssues int64 `json:"new_issues"`
	// Issues son los issues con eventos en esta release, nuevos o no.
	Issues      int64      `json:"issues"`
	Regressions int64      `json:"regressions"`
	Events      int64      `json:"events"`
	LastEvent   *time.Time `json:"last_event"`
}

func validName(what, s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", fmt.Errorf("%w: falta %s", ErrInvalid, what)
	case len(s) > maxNameLen:
		return "", fmt.Errorf("%w: %s pasa de %d caracteres", ErrInvalid, what, maxNameLen)
	case strings.ContainsAny(s, "\n\r\t"):
		return "", fmt.Errorf("%w: %s no puede llevar saltos de línea ni tabuladores", ErrInvalid, what)
	}
	return s, nil
}

// EnsureRelease devuelve el id de la release, y la crea vista en now si no
// existía. No bloquea la fila, para que los eventos de una release no se
// esperen unos a otros.
func EnsureRelease(ctx context.Context, q Querier, projectID int64, version string, now time.Time) (id int64, created bool, err error) {
	err = q.QueryRow(ctx, `SELECT id FROM releases WHERE project_id = $1 AND version = $2`, projectID, version).Scan(&id)
	if err == nil {
		return id, false, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("buscar release: %w", err)
	}
	err = q.QueryRow(ctx, `
		INSERT INTO releases (project_id, version, first_seen) VALUES ($1, $2, $3)
		ON CONFLICT (project_id, version) DO NOTHING RETURNING id`, projectID, version, now).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = q.QueryRow(ctx, `SELECT id FROM releases WHERE project_id = $1 AND version = $2`, projectID, version).Scan(&id)
		return id, false, err
	}
	if err != nil {
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23503" {
			return 0, false, ErrNotFound
		}
		return 0, false, fmt.Errorf("crear release: %w", err)
	}
	return id, true, nil
}

// CompareReleases dice si la release a es anterior (-1), la misma (0) o
// posterior (1) a b: por su número si las dos se leen como versiones del
// mismo paquete (byVersion), y si no, por cuándo se vieron por primera vez;
// entonces ok es false si alguna de las dos no existe.
func CompareReleases(ctx context.Context, q Querier, projectID int64, a, b string) (cmp int, ok, byVersion bool, err error) {
	if a == b {
		return 0, true, true, nil
	}
	if c, ok := compareVersions(a, b); ok {
		return c, true, true, nil
	}
	err = q.QueryRow(ctx, `
		SELECT CASE WHEN (ra.first_seen, ra.id) < (rb.first_seen, rb.id) THEN -1 ELSE 1 END
		FROM releases ra, releases rb
		WHERE ra.project_id = $1 AND ra.version = $2 AND rb.project_id = $1 AND rb.version = $3`,
		projectID, a, b).Scan(&cmp)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, false, nil
	}
	if err != nil {
		return 0, false, false, fmt.Errorf("comparar releases: %w", err)
	}
	return cmp, true, false, nil
}

// TrackEvent suma un evento del issue a su release y su entorno, aunque el
// evento no se guarde.
func TrackEvent(ctx context.Context, q Querier, projectID, issueID int64, release, environment string, at time.Time, regressed bool) error {
	_, err := q.Exec(ctx, `
		INSERT INTO issue_stats (issue_id, project_id, release, environment, first_seen, last_seen, event_count, regressed_at)
		VALUES ($1, $2, $3, $4, $5, $5, 1, CASE WHEN $6 THEN now() END)
		ON CONFLICT (issue_id, release, environment) DO UPDATE SET
			event_count = issue_stats.event_count + 1,
			first_seen = LEAST(issue_stats.first_seen, EXCLUDED.first_seen),
			last_seen = GREATEST(issue_stats.last_seen, EXCLUDED.last_seen),
			regressed_at = COALESCE(EXCLUDED.regressed_at, issue_stats.regressed_at)`,
		issueID, projectID, release, environment, at, regressed)
	if err != nil {
		return fmt.Errorf("contar el evento por release y entorno: %w", err)
	}
	return nil
}

// CreateRelease crea la release si no existe y la devuelve.
func (s *Store) CreateRelease(ctx context.Context, projectID int64, version string) (Release, bool, error) {
	version, err := validName("version", version)
	if err != nil {
		return Release{}, false, err
	}
	_, created, err := EnsureRelease(ctx, s.pool, projectID, version, time.Now())
	if err != nil {
		return Release{}, false, err
	}
	r, err := s.Release(ctx, projectID, version, "")
	return r, created, err
}

// FinalizeRelease marca la release como terminada (la crea si no existía).
// Si ya lo estaba, se queda con su fecha.
func (s *Store) FinalizeRelease(ctx context.Context, projectID int64, version string, at time.Time) (Release, error) {
	version, err := validName("version", version)
	if err != nil {
		return Release{}, err
	}
	id, _, err := EnsureRelease(ctx, s.pool, projectID, version, time.Now())
	if err != nil {
		return Release{}, err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE releases SET finalized_at = COALESCE(finalized_at, $2) WHERE id = $1`, id, at); err != nil {
		return Release{}, fmt.Errorf("finalizar release: %w", err)
	}
	return s.Release(ctx, projectID, version, "")
}

// AddDeploy apunta un despliegue de la release en un entorno, y la crea si no
// existía. Los issues en resolved_next que se resolvieron con una release
// anterior pasan a resueltos con esta: devuelve sus ids.
func (s *Store) AddDeploy(ctx context.Context, projectID int64, version, environment string, at time.Time) (Deploy, []int64, error) {
	version, err := validName("version", version)
	if err != nil {
		return Deploy{}, nil, err
	}
	if environment, err = validName("environment", environment); err != nil {
		return Deploy{}, nil, err
	}
	if at.IsZero() {
		at = time.Now()
	}
	d := Deploy{Environment: environment}
	var resolved []int64
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		id, _, err := EnsureRelease(ctx, tx, projectID, version, time.Now())
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO deploys (release_id, environment, deployed_at) VALUES ($1, $2, $3)
			RETURNING id, deployed_at`, id, environment, at).Scan(&d.ID, &d.DeployedAt); err != nil {
			return fmt.Errorf("guardar despliegue: %w", err)
		}
		rows, err := tx.Query(ctx, `
			SELECT id, COALESCE(resolved_in_release, '') FROM issues
			WHERE project_id = $1 AND status = 'resolved_next' FOR UPDATE`, projectID)
		if err != nil {
			return fmt.Errorf("resolver issues: %w", err)
		}
		type pending struct {
			id      int64
			release string
		}
		next, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (pending, error) {
			var p pending
			err := r.Scan(&p.id, &p.release)
			return p, err
		})
		if err != nil {
			return fmt.Errorf("resolver issues: %w", err)
		}
		// Una release resuelta que no está en la tabla cuenta como anterior.
		for _, p := range next {
			c, ok, _, err := CompareReleases(ctx, tx, projectID, version, p.release)
			if err != nil {
				return err
			}
			if !ok || c > 0 {
				resolved = append(resolved, p.id)
			}
		}
		if len(resolved) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE issues SET status = 'resolved', fixed_in_release = $2,
				resolved_at = COALESCE(resolved_at, now())
			WHERE id = ANY($1)`, resolved, version)
		if err != nil {
			return fmt.Errorf("resolver issues: %w", err)
		}
		for _, id := range resolved {
			if err := recordStatusChange(ctx, tx, id, nil, SourceDeploy, "resolved_next", "resolved", &version, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return Deploy{}, nil, err
	}
	return d, resolved, nil
}

// ReleaseFilter son los parámetros de la lista de releases.
type ReleaseFilter struct {
	// Project es el id o el nombre del proyecto; vacío, todos.
	Project string
	// Environment deja las releases con eventos o despliegues en ese entorno
	// y cuenta solo lo suyo; vacío, todos.
	Environment string
	Cursor      string
	Limit       int
}

type ReleasePage struct {
	Releases   []Release `json:"data"`
	NextCursor string    `json:"next_cursor,omitempty"`
}

// releaseColumns necesita el entorno en $1 (” para todos).
const releaseColumns = `
	r.id, r.project_id, p.name, r.version, r.first_seen, r.finalized_at,
	(SELECT count(*) FROM issues i
	  WHERE i.project_id = r.project_id AND i.first_release = r.version
	    AND ($1 = '' OR EXISTS (SELECT 1 FROM issue_stats s
	      WHERE s.issue_id = i.id AND s.release = r.version AND s.environment = $1))),
	st.issues, st.regressions, st.events, st.last_event`

const releaseFrom = `releases r JOIN projects p ON p.id = r.project_id
	CROSS JOIN LATERAL (
		SELECT count(DISTINCT s.issue_id) AS issues,
			count(DISTINCT s.issue_id) FILTER (WHERE s.regressed_at IS NOT NULL) AS regressions,
			COALESCE(sum(s.event_count), 0)::bigint AS events,
			max(s.last_seen) AS last_event
		FROM issue_stats s
		WHERE s.project_id = r.project_id AND s.release = r.version AND ($1 = '' OR s.environment = $1)) st`

func scanRelease(r pgx.Row) (Release, error) {
	var rel Release
	err := r.Scan(&rel.ID, &rel.ProjectID, &rel.Project, &rel.Version, &rel.FirstSeen, &rel.FinalizedAt,
		&rel.NewIssues, &rel.Issues, &rel.Regressions, &rel.Events, &rel.LastEvent)
	return rel, err
}

// ListReleases lista las releases de la más nueva a la más vieja.
func (s *Store) ListReleases(ctx context.Context, f ReleaseFilter) (ReleasePage, error) {
	limit, err := normalizeLimit(f.Limit)
	if err != nil {
		return ReleasePage{}, err
	}
	args := []any{f.Environment}
	where := []string{"true"}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if f.Project != "" {
		where = append(where, fmt.Sprintf("(p.id::text = %s OR p.name = %[1]s)", arg(f.Project)))
	}
	if f.Environment != "" {
		where = append(where, `(st.events > 0 OR EXISTS (SELECT 1 FROM deploys d WHERE d.release_id = r.id AND d.environment = $1))`)
	}
	if f.Cursor != "" {
		c, err := decodeCursor(f.Cursor, "releases")
		if err != nil {
			return ReleasePage{}, err
		}
		at, err := time.Parse(time.RFC3339Nano, c.Value)
		if err != nil {
			return ReleasePage{}, fmt.Errorf("%w: cursor", ErrInvalid)
		}
		where = append(where, fmt.Sprintf("(r.first_seen, r.id) < (%s, %s)", arg(at), arg(c.ID)))
	}
	rows, err := s.pool.Query(ctx, fmt.Sprintf(`SELECT %s FROM %s WHERE %s
		ORDER BY r.first_seen DESC, r.id DESC LIMIT %d`, releaseColumns, releaseFrom, strings.Join(where, " AND "), limit+1), args...)
	if err != nil {
		return ReleasePage{}, fmt.Errorf("listar releases: %w", err)
	}
	rels, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Release, error) { return scanRelease(r) })
	if err != nil {
		return ReleasePage{}, fmt.Errorf("listar releases: %w", err)
	}
	page := ReleasePage{Releases: rels}
	if len(rels) > limit {
		page.Releases = rels[:limit]
		last := page.Releases[limit-1]
		page.NextCursor = encodeCursor(cursor{Sort: "releases", Value: last.FirstSeen.Format(time.RFC3339Nano), ID: last.ID})
	}
	if page.Releases == nil {
		page.Releases = []Release{}
	}
	if err := s.loadDeploys(ctx, page.Releases, f.Environment); err != nil {
		return ReleasePage{}, err
	}
	return page, nil
}

func (s *Store) loadDeploys(ctx context.Context, rels []Release, env string) error {
	ids := make([]int64, len(rels))
	at := make(map[int64]int, len(rels))
	for i := range rels {
		ids[i] = rels[i].ID
		at[rels[i].ID] = i
		rels[i].Deploys = []Deploy{}
	}
	if len(ids) == 0 {
		return nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT release_id, id, environment, deployed_at FROM deploys
		WHERE release_id = ANY($1) AND ($2 = '' OR environment = $2)
		ORDER BY deployed_at DESC, id DESC`, ids, env)
	if err != nil {
		return fmt.Errorf("leer despliegues: %w", err)
	}
	var rid int64
	var d Deploy
	_, err = pgx.ForEachRow(rows, []any{&rid, &d.ID, &d.Environment, &d.DeployedAt}, func() error {
		rels[at[rid]].Deploys = append(rels[at[rid]].Deploys, d)
		return nil
	})
	if err != nil {
		return fmt.Errorf("leer despliegues: %w", err)
	}
	return nil
}

// Release busca la release de un proyecto por su versión, con los recuentos
// de un entorno (vacío, todos).
func (s *Store) Release(ctx context.Context, projectID int64, version, env string) (Release, error) {
	r, err := scanRelease(s.pool.QueryRow(ctx, `SELECT `+releaseColumns+` FROM `+releaseFrom+`
		WHERE r.project_id = $2 AND r.version = $3`, env, projectID, version))
	if errors.Is(err, pgx.ErrNoRows) {
		return Release{}, ErrNotFound
	}
	if err != nil {
		return Release{}, fmt.Errorf("leer release: %w", err)
	}
	rels := []Release{r}
	if err := s.loadDeploys(ctx, rels, env); err != nil {
		return Release{}, err
	}
	return rels[0], nil
}

// ReleaseIssue es un issue en el detalle de una release, con lo suyo en ella.
type ReleaseIssue struct {
	Issue
	// ReleaseEvents son sus eventos en la release (o en la anterior, en Stopped).
	ReleaseEvents   int64     `json:"release_events"`
	ReleaseLastSeen time.Time `json:"release_last_seen"`
}

type ReleaseDetail struct {
	Release
	// Previous y Next son las versiones de al lado, por cuándo se vieron.
	Previous *string `json:"previous"`
	Next     *string `json:"next"`
	// New son los issues que aparecieron con la release; Continued, los que
	// ya existían y siguieron saliendo; Regressed, los que volvieron con ella.
	New       []ReleaseIssue `json:"new"`
	Continued []ReleaseIssue `json:"continued"`
	Regressed []ReleaseIssue `json:"regressed"`
	// Stopped son los que salían en la release anterior y en esta no, y los
	// que se resolvieron al desplegarla.
	Stopped []ReleaseIssue `json:"stopped"`
}

// ReleaseDetail es la release con sus issues, solo de un entorno si env no
// está vacío.
func (s *Store) ReleaseDetail(ctx context.Context, projectID int64, version, env string) (ReleaseDetail, error) {
	r, err := s.Release(ctx, projectID, version, env)
	if err != nil {
		return ReleaseDetail{}, err
	}
	d := ReleaseDetail{Release: r}
	err = s.pool.QueryRow(ctx, `
		SELECT
			(SELECT o.version FROM releases o WHERE o.project_id = r.project_id AND (o.first_seen, o.id) < (r.first_seen, r.id)
			  ORDER BY o.first_seen DESC, o.id DESC LIMIT 1),
			(SELECT o.version FROM releases o WHERE o.project_id = r.project_id AND (o.first_seen, o.id) > (r.first_seen, r.id)
			  ORDER BY o.first_seen, o.id LIMIT 1)
		FROM releases r WHERE r.id = $1`, r.ID).Scan(&d.Previous, &d.Next)
	if err != nil {
		return ReleaseDetail{}, fmt.Errorf("releases de al lado: %w", err)
	}

	cols, from := issueSQL("")
	// $1 proyecto, $2 versión, $3 entorno; st son los recuentos del issue en la release.
	inRelease := `JOIN LATERAL (
			SELECT sum(s.event_count)::bigint AS n, max(s.last_seen) AS last_seen,
				bool_or(s.regressed_at IS NOT NULL) AS regressed
			FROM issue_stats s WHERE s.issue_id = i.id AND s.release = $2 AND ($3 = '' OR s.environment = $3)
			HAVING count(*) > 0) st ON true`
	list := func(join, where string, args ...any) ([]ReleaseIssue, error) {
		q := fmt.Sprintf(`SELECT %s, st.n, st.last_seen FROM %s %s
			WHERE i.project_id = $1 AND %s
			ORDER BY st.n DESC, i.id DESC LIMIT %d`, cols, from, join, where, MaxReleaseIssues)
		rows, err := s.pool.Query(ctx, q, args...)
		if err != nil {
			return nil, fmt.Errorf("issues de la release: %w", err)
		}
		out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (ReleaseIssue, error) {
			var ri ReleaseIssue
			i := &ri.Issue
			err := row.Scan(&i.ID, &i.ProjectID, &i.Project, &i.Title, &i.Culprit, &i.Type, &i.Level, &i.Status,
				&i.Count, &i.UserCount, &i.FirstSeen, &i.LastSeen, &i.LastRelease, &i.ResolvedInRelease, &i.LastRegression,
				&i.FirstRelease, &i.FixedInRelease, &ri.ReleaseEvents, &ri.ReleaseLastSeen)
			return ri, err
		})
		if err != nil {
			return nil, fmt.Errorf("issues de la release: %w", err)
		}
		if out == nil {
			out = []ReleaseIssue{}
		}
		return out, nil
	}
	if d.New, err = list(inRelease, `i.first_release = $2`, projectID, version, env); err != nil {
		return ReleaseDetail{}, err
	}
	if d.Continued, err = list(inRelease, `i.first_release IS DISTINCT FROM $2 AND NOT st.regressed`, projectID, version, env); err != nil {
		return ReleaseDetail{}, err
	}
	if d.Regressed, err = list(inRelease, `st.regressed`, projectID, version, env); err != nil {
		return ReleaseDetail{}, err
	}

	// Lo que dejó de salir: con los recuentos de la release anterior, y los
	// resueltos con el despliegue de esta aunque no salieran en la anterior.
	prev := ""
	if d.Previous != nil {
		prev = *d.Previous
	}
	stoppedJoin := `JOIN LATERAL (
			SELECT COALESCE(sum(s.event_count), 0)::bigint AS n, COALESCE(max(s.last_seen), i.last_seen) AS last_seen
			FROM issue_stats s
			WHERE s.issue_id = i.id AND $4 <> '' AND s.release = $4 AND ($3 = '' OR s.environment = $3)) st ON true`
	d.Stopped, err = list(stoppedJoin, `
		((i.fixed_in_release = $2 AND ($3 = '' OR EXISTS (SELECT 1 FROM issue_stats s
			WHERE s.issue_id = i.id AND s.environment = $3)))
		  OR (st.n > 0 AND $5::bigint > 0))
		AND NOT EXISTS (SELECT 1 FROM issue_stats s WHERE s.issue_id = i.id AND s.release = $2
			AND ($3 = '' OR s.environment = $3))`, projectID, version, env, prev, d.Events)
	if err != nil {
		return ReleaseDetail{}, err
	}
	return d, nil
}

// Environment es un entorno con eventos, con lo que lleva visto.
type Environment struct {
	Name     string    `json:"name"`
	Issues   int64     `json:"issues"`
	Events   int64     `json:"events"`
	LastSeen time.Time `json:"last_seen"`
}

// Environments son los entornos de los eventos de un proyecto (id o nombre;
// vacío, de todos), del visto más recientemente al que menos.
func (s *Store) Environments(ctx context.Context, project string) ([]Environment, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.environment, count(DISTINCT s.issue_id), sum(s.event_count)::bigint, max(s.last_seen)
		FROM issue_stats s JOIN projects p ON p.id = s.project_id
		WHERE s.environment <> '' AND ($1 = '' OR p.id::text = $1 OR p.name = $1)
		GROUP BY s.environment ORDER BY max(s.last_seen) DESC, s.environment`, project)
	if err != nil {
		return nil, fmt.Errorf("listar entornos: %w", err)
	}
	envs, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Environment, error) {
		var e Environment
		err := r.Scan(&e.Name, &e.Issues, &e.Events, &e.LastSeen)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("listar entornos: %w", err)
	}
	if envs == nil {
		envs = []Environment{}
	}
	return envs, nil
}

// ReleaseProjects son los proyectos que tienen una release con esa versión.
func (s *Store) ReleaseProjects(ctx context.Context, version string) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectColumns+` FROM projects
		WHERE id IN (SELECT project_id FROM releases WHERE version = $1) ORDER BY id`, version)
	if err != nil {
		return nil, fmt.Errorf("proyectos de la release: %w", err)
	}
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Project, error) {
		var p Project
		err := r.Scan(p.fields()...)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("proyectos de la release: %w", err)
	}
	return ps, nil
}
