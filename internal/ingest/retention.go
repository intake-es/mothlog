package ingest

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// Purge borra los eventos recibidos antes de cutoff y los issues que se quedan
// sin eventos.
func Purge(ctx context.Context, pool db.DB, cutoff time.Time) (events, issues int64, err error) {
	tag, err := pool.Exec(ctx, `DELETE FROM events WHERE received_at < $1`, cutoff)
	if err != nil {
		return 0, 0, fmt.Errorf("borrar eventos: %w", err)
	}
	events = tag.RowsAffected()
	issues, err = purgeEmptyIssues(ctx, pool)
	if err != nil {
		return events, 0, err
	}
	return events, issues, nil
}

// purgeEmptyIssues salta los issues que una ingesta tiene bloqueados y vuelve a
// mirar sus eventos ya con el cerrojo, en una sentencia nueva, para ver los de
// cualquier ingesta que haya terminado justo antes.
func purgeEmptyIssues(ctx context.Context, pool db.DB) (int64, error) {
	var n int64
	err := db.BeginFunc(ctx, pool, func(tx db.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id FROM issues i
			WHERE NOT EXISTS (SELECT 1 FROM events e WHERE e.issue_id = i.id)`+tx.Dialect().ForUpdateSkipLocked())
		if err != nil {
			return err
		}
		ids, err := db.CollectRows(rows, db.RowTo[int64])
		if err != nil || len(ids) == 0 {
			return err
		}
		tag, err := tx.Exec(ctx, `
			DELETE FROM issues AS i
			WHERE `+tx.Dialect().InList("i.id", "$1")+`
			  AND NOT EXISTS (SELECT 1 FROM events e WHERE e.issue_id = i.id)`, ids)
		n = tag.RowsAffected()
		return err
	})
	if err != nil {
		return 0, fmt.Errorf("borrar issues: %w", err)
	}
	return n, nil
}

// purgeReleasesBatch es cuántas releases candidatas mira PurgeReleases de una vez.
const purgeReleasesBatch = 1000

// releaseUnused son las referencias a la release r que tienen índice.
const releaseUnused = `r.finalized_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM deploys d WHERE d.release_id = r.id)
	AND NOT EXISTS (SELECT 1 FROM import_origins o WHERE o.release_id = r.id)
	AND NOT EXISTS (SELECT 1 FROM issue_stats s WHERE s.project_id = r.project_id AND s.release = r.version)
	AND NOT EXISTS (SELECT 1 FROM release_sessions s WHERE s.project_id = r.project_id AND s.release = r.version)
	AND NOT EXISTS (SELECT 1 FROM release_users u WHERE u.project_id = r.project_id AND u.release = r.version)
	AND NOT EXISTS (SELECT 1 FROM sourcemaps m WHERE m.project_id = r.project_id AND m.release = r.version)`

// PurgeReleases borra las releases sin finalizar vistas antes de cutoff que
// ya nada usa: ni eventos, ni recuentos, ni sesiones, ni despliegues, ni
// source maps, ni bundles, ni las columnas de release de los issues.
func PurgeReleases(ctx context.Context, pool db.DB, cutoff time.Time) (int64, error) {
	var n int64
	var after int64
	for {
		rows, err := pool.Query(ctx, `
			SELECT r.id, r.project_id, r.version FROM releases r
			WHERE r.first_seen < $1 AND r.id > $2 AND `+releaseUnused+`
			ORDER BY r.id LIMIT `+fmt.Sprint(purgeReleasesBatch), cutoff, after)
		if err != nil {
			return n, fmt.Errorf("buscar releases sin usar: %w", err)
		}
		type candidate struct {
			id, project int64
			version     string
		}
		cands, err := db.CollectRows(rows, func(r db.CollectableRow) (candidate, error) {
			var c candidate
			err := r.Scan(&c.id, &c.project, &c.version)
			return c, err
		})
		if err != nil {
			return n, fmt.Errorf("buscar releases sin usar: %w", err)
		}
		if len(cands) == 0 {
			return n, nil
		}
		after = cands[len(cands)-1].id
		byProject := map[int64][]candidate{}
		for _, c := range cands {
			byProject[c.project] = append(byProject[c.project], c)
		}
		var ids []int64
		for project, cs := range byProject {
			versions := make([]string, len(cs))
			for i, c := range cs {
				versions[i] = c.version
			}
			used, err := releasesInUse(ctx, pool, project, versions)
			if err != nil {
				return n, err
			}
			for _, c := range cs {
				if !used[c.version] {
					ids = append(ids, c.id)
				}
			}
		}
		if len(ids) > 0 {
			// Lo que tiene índice se vuelve a mirar al borrar, por si algo la
			// ha usado mientras tanto.
			tag, err := pool.Exec(ctx, `DELETE FROM releases AS r
				WHERE `+pool.Dialect().InList("r.id", "$1")+` AND `+releaseUnused, ids)
			if err != nil {
				return n, fmt.Errorf("borrar releases: %w", err)
			}
			n += tag.RowsAffected()
		}
		if len(cands) < purgeReleasesBatch {
			return n, nil
		}
	}
}

// releasesInUse dice cuáles de las versiones usan las tablas que no tienen
// índice por release: una sola pasada por proyecto para todas.
func releasesInUse(ctx context.Context, pool db.DB, projectID int64, versions []string) (map[string]bool, error) {
	in := func(col string) string { return pool.Dialect().InList(col, "$2") }
	rows, err := pool.Query(ctx, `
		SELECT release FROM events WHERE project_id = $1 AND `+in("release")+`
		UNION SELECT first_release FROM issues WHERE project_id = $1 AND `+in("first_release")+`
		UNION SELECT resolved_in_release FROM issues WHERE project_id = $1 AND `+in("resolved_in_release")+`
		UNION SELECT fixed_in_release FROM issues WHERE project_id = $1 AND `+in("fixed_in_release")+`
		UNION SELECT release FROM artifact_bundles WHERE project_id = $1 AND `+in("release")+`
		UNION SELECT release FROM session_states WHERE project_id = $1 AND `+in("release"), projectID, versions)
	if err != nil {
		return nil, fmt.Errorf("releases en uso: %w", err)
	}
	used, err := db.CollectRows(rows, db.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("releases en uso: %w", err)
	}
	out := make(map[string]bool, len(used))
	for _, v := range used {
		out[v] = true
	}
	return out, nil
}

// RunRetention purga eventos, sesiones y releases al arrancar y luego cada hora hasta que se cancela ctx.
func RunRetention(ctx context.Context, pool db.DB, days int, log *slog.Logger) {
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		cutoff := time.Now().AddDate(0, 0, -days)
		ev, is, err := Purge(ctx, pool, cutoff)
		if err != nil {
			log.ErrorContext(ctx, "retención", "err", err)
		} else if ev > 0 || is > 0 {
			log.InfoContext(ctx, "retención", "eventos", ev, "issues", is)
		}
		if n, err := PurgeSessions(ctx, pool, cutoff, time.Now()); err != nil {
			log.ErrorContext(ctx, "retención de sesiones", "err", err)
		} else if n > 0 {
			log.InfoContext(ctx, "retención de sesiones", "filas", n)
		}
		if n, err := PurgeReleases(ctx, pool, cutoff); err != nil {
			log.ErrorContext(ctx, "retención de releases", "err", err)
		} else if n > 0 {
			log.InfoContext(ctx, "retención de releases", "releases", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
