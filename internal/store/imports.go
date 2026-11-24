package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// maxImportedTitle es el mismo tope que pone la ingesta a los títulos.
const maxImportedTitle = 200

// SourceImport es el origen de los cambios de estado que trae una importación.
const SourceImport = "import"

// Origin es de dónde se importa: el servicio, su servidor y la organización.
type Origin struct {
	Source string
	Server string
	Org    string
}

// ImportAction es lo que hizo, o haría en una prueba, una importación con una fila.
type ImportAction int

const (
	ImportUnchanged ImportAction = iota
	ImportCreated
	ImportUpdated
)

// importTx corre fn en una transacción. En una prueba (dry) la transacción
// es de solo lectura y se deshace: fn no debe escribir, y si lo intenta, falla.
func (s *Store) importTx(ctx context.Context, dry bool, fn func(db.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, db.TxOptions{ReadOnly: dry})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(tx); err != nil {
		return err
	}
	if dry {
		return nil
	}
	return tx.Commit(ctx)
}

type origin struct {
	localID  int64
	imported json.RawMessage
}

func findOrigin(ctx context.Context, q Querier, o Origin, kind, externalID string) (*origin, error) {
	var r origin
	err := q.QueryRow(ctx, `
		SELECT COALESCE(project_id, issue_id, release_id, deploy_id), imported FROM import_origins
		WHERE source = $1 AND server = $2 AND org = $3 AND kind = $4 AND external_id = $5`,
		o.Source, o.Server, o.Org, kind, externalID).Scan(&r.localID, &r.imported)
	if errors.Is(err, db.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("buscar lo importado: %w", err)
	}
	return &r, nil
}

func saveOrigin(ctx context.Context, q Querier, o Origin, kind, externalID string, localID int64, imported any) error {
	data, err := json.Marshal(imported)
	if err != nil {
		return err
	}
	_, err = q.Exec(ctx, `
		INSERT INTO import_origins (source, server, org, kind, external_id, project_id, issue_id, release_id, deploy_id, imported)
		VALUES ($1, $2, $3, $4, $5,
			CASE WHEN $4 = 'project' THEN CAST($6 AS bigint) END, CASE WHEN $4 = 'issue' THEN CAST($6 AS bigint) END,
			CASE WHEN $4 = 'release' THEN CAST($6 AS bigint) END, CASE WHEN $4 = 'deploy' THEN CAST($6 AS bigint) END, $7)
		ON CONFLICT (source, server, org, kind, external_id) DO UPDATE SET
			project_id = EXCLUDED.project_id, issue_id = EXCLUDED.issue_id, release_id = EXCLUDED.release_id,
			deploy_id = EXCLUDED.deploy_id, imported = EXCLUDED.imported, updated_at = now()`,
		o.Source, o.Server, o.Org, kind, externalID, localID, json.RawMessage(data))
	if err != nil {
		return fmt.Errorf("apuntar de dónde viene: %w", err)
	}
	return nil
}

// ImportProject busca el proyecto importado antes o uno con el mismo nombre,
// que se usa tal cual, y si no hay ninguno lo crea. En una prueba, un
// proyecto que se crearía devuelve id 0.
func (s *Store) ImportProject(ctx context.Context, o Origin, externalID, name string, dry bool) (int64, ImportAction, error) {
	name, err := validName("el nombre del proyecto", name)
	if err != nil {
		return 0, 0, err
	}
	var id int64
	act := ImportUnchanged
	err = s.importTx(ctx, dry, func(tx db.Tx) error {
		prev, err := findOrigin(ctx, tx, o, "project", externalID)
		if err != nil || prev != nil {
			if prev != nil {
				id = prev.localID
			}
			return err
		}
		err = tx.QueryRow(ctx, `SELECT id FROM projects WHERE name = $1 ORDER BY id LIMIT 1`, name).Scan(&id)
		switch {
		case errors.Is(err, db.ErrNoRows):
			act = ImportCreated
			if dry {
				return nil
			}
			key := make([]byte, 16)
			_, _ = rand.Read(key)
			if err := tx.QueryRow(ctx, `INSERT INTO projects (name, public_key) VALUES ($1, $2) RETURNING id`,
				name, hex.EncodeToString(key)).Scan(&id); err != nil {
				return fmt.Errorf("crear proyecto: %w", err)
			}
		case err != nil:
			return fmt.Errorf("buscar proyecto: %w", err)
		}
		if dry {
			return nil
		}
		return saveOrigin(ctx, tx, o, "project", externalID, id, map[string]string{"name": name})
	})
	return id, act, err
}

// ImportedRelease es una release tal como llega del otro servicio.
type ImportedRelease struct {
	ExternalID string     `json:"-"`
	Version    string     `json:"version"`
	Created    time.Time  `json:"created"`
	Released   *time.Time `json:"released,omitempty"`
}

func (r ImportedRelease) same(o ImportedRelease) bool {
	return r.Version == o.Version && r.Created.Equal(o.Created) &&
		(r.Released == nil) == (o.Released == nil) && (r.Released == nil || r.Released.Equal(*o.Released))
}

// ImportRelease crea la release o, si ya existía, adelanta su first_seen a
// cuando se creó allí y le pone la fecha de publicación si no tenía.
func (s *Store) ImportRelease(ctx context.Context, o Origin, projectID int64, r ImportedRelease, dry bool) (int64, ImportAction, error) {
	version, err := validName("version", r.Version)
	if err != nil {
		return 0, 0, err
	}
	r.Version, r.Created = version, r.Created.UTC()
	if r.Released != nil {
		t := r.Released.UTC()
		r.Released = &t
	}
	var id int64
	act := ImportUnchanged
	err = s.importTx(ctx, dry, func(tx db.Tx) error {
		prev, err := findOrigin(ctx, tx, o, "release", r.ExternalID)
		if err != nil {
			return err
		}
		if prev != nil {
			id = prev.localID
			var old ImportedRelease
			if json.Unmarshal(prev.imported, &old) == nil && old.same(r) {
				return nil
			}
		} else {
			err := tx.QueryRow(ctx, `SELECT id FROM releases WHERE project_id = $1 AND version = $2`, projectID, version).Scan(&id)
			if errors.Is(err, db.ErrNoRows) {
				act = ImportCreated
				if dry {
					return nil
				}
				if err := tx.QueryRow(ctx, `
					INSERT INTO releases (project_id, version, first_seen, finalized_at, declared_at) VALUES ($1, $2, $3, $4, $5)
					RETURNING id`, projectID, version, r.Created, r.Released, time.Now()).Scan(&id); err != nil {
					return fmt.Errorf("crear release: %w", err)
				}
				return saveOrigin(ctx, tx, o, "release", r.ExternalID, id, r)
			}
			if err != nil {
				return fmt.Errorf("buscar release: %w", err)
			}
		}
		var changed bool
		err = tx.QueryRow(ctx, `
			SELECT first_seen > $2 OR (finalized_at IS NULL AND `+tx.Dialect().Typed("$3", "timestamptz")+` IS NOT NULL)
			FROM releases WHERE id = $1`, id, r.Created, r.Released).Scan(&changed)
		if err != nil {
			return fmt.Errorf("leer release: %w", err)
		}
		if changed || prev != nil {
			act = ImportUpdated
		}
		if dry {
			return nil
		}
		if changed {
			if _, err := tx.Exec(ctx, `
				UPDATE releases SET first_seen = `+tx.Dialect().Least("first_seen", "$2")+`, finalized_at = COALESCE(finalized_at, $3)
				WHERE id = $1`, id, r.Created, r.Released); err != nil {
				return fmt.Errorf("actualizar release: %w", err)
			}
		}
		if err := declareRelease(ctx, tx, id, time.Now()); err != nil {
			return err
		}
		return saveOrigin(ctx, tx, o, "release", r.ExternalID, id, r)
	})
	return id, act, err
}

// ImportDeploy apunta un despliegue de la release sin resolver nada: lo que
// resolvió allí ya viene en el estado de cada issue.
func (s *Store) ImportDeploy(ctx context.Context, o Origin, releaseID int64, externalID, environment string,
	at time.Time, dry bool) (ImportAction, error) {
	environment, err := validName("el entorno", environment)
	if err != nil {
		return 0, err
	}
	act := ImportUnchanged
	err = s.importTx(ctx, dry, func(tx db.Tx) error {
		prev, err := findOrigin(ctx, tx, o, "deploy", externalID)
		if err != nil || prev != nil {
			return err
		}
		act = ImportCreated
		if dry {
			return nil
		}
		var id int64
		if err := tx.QueryRow(ctx, `INSERT INTO deploys (release_id, environment, deployed_at) VALUES ($1, $2, $3) RETURNING id`,
			releaseID, environment, at).Scan(&id); err != nil {
			return fmt.Errorf("crear despliegue: %w", err)
		}
		return saveOrigin(ctx, tx, o, "deploy", externalID, id, map[string]any{"environment": environment, "at": at.UTC()})
	})
	return act, err
}

// ImportedIssue es un issue tal como llega del otro servicio.
type ImportedIssue struct {
	ExternalID string `json:"-"`
	// Fingerprint es la huella que le daría la ingesta a su último evento;
	// vacía si no se puede sacar.
	Fingerprint string `json:"-"`
	Title       string `json:"title"`
	Culprit     string `json:"culprit"`
	Type        string `json:"type"`
	Level       string `json:"level"`
	// Status es open, resolved, resolved_next o silenced. FixedIn es la
	// release en la que se resolvió y ResolvedIn, con resolved_next, la del
	// último evento.
	Status     string    `json:"status"`
	FixedIn    string    `json:"fixed_in,omitempty"`
	ResolvedIn string    `json:"resolved_in,omitempty"`
	Count      int64     `json:"count"`
	UserCount  int64     `json:"user_count"`
	FirstSeen  time.Time `json:"first_seen"`
	LastSeen   time.Time `json:"last_seen"`
	// FirstRelease es la release en la que se vio por primera vez.
	FirstRelease string `json:"first_release,omitempty"`
}

func (i ImportedIssue) same(o ImportedIssue) bool {
	return i.Title == o.Title && i.Culprit == o.Culprit && i.Type == o.Type && i.Level == o.Level &&
		i.Status == o.Status && i.FixedIn == o.FixedIn && i.ResolvedIn == o.ResolvedIn &&
		i.Count == o.Count && i.UserCount == o.UserCount && i.FirstSeen.Equal(o.FirstSeen) && i.LastSeen.Equal(o.LastSeen) &&
		i.FirstRelease == o.FirstRelease
}

// ImportIssueResult dice qué se hizo con un issue y si los eventos nuevos de
// la ingesta irán a él.
type ImportIssueResult struct {
	ID     int64
	Action ImportAction
	// Grouped dice si su huella es la de la ingesta. Si no, NotGrouped dice por qué.
	Grouped    bool
	NotGrouped string
}

// importFingerprint es la huella de un issue importado que no puede llevar
// la de la ingesta: única y distinta de cualquiera que dé la ingesta.
func importFingerprint(o Origin, externalID string) string {
	h := sha256.New()
	for _, p := range []string{"import", o.Source, o.Server, o.Org, externalID} {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ImportIssue crea el issue o actualiza el que se importó antes con lo que ha
// cambiado allí desde entonces: el título, el nivel o el estado solo si
// cambiaron allí, para no pisar lo cambiado aquí; el contador suma lo que
// creció allí, porque aquí puede llevar además los eventos de la ingesta.
func (s *Store) ImportIssue(ctx context.Context, o Origin, projectID int64, in ImportedIssue, dry bool) (ImportIssueResult, error) {
	in.FirstSeen, in.LastSeen = in.FirstSeen.UTC(), in.LastSeen.UTC()
	if in.Title == "" {
		in.Title = "(sin mensaje)"
	}
	if r := []rune(in.Title); len(r) > maxImportedTitle {
		in.Title = string(r[:maxImportedTitle-1]) + "…"
	}
	if in.Level == "" {
		in.Level = "error"
	}
	switch in.Status {
	case "resolved":
		in.ResolvedIn = ""
	case "resolved_next":
		in.FixedIn = ""
		if in.ResolvedIn == "" {
			in.Status = "resolved"
		}
	default:
		in.FixedIn, in.ResolvedIn = "", ""
	}
	if in.Count < 0 {
		in.Count = 0
	}
	own := importFingerprint(o, in.ExternalID)
	var res ImportIssueResult
	err := s.importTx(ctx, dry, func(tx db.Tx) error {
		res = ImportIssueResult{}
		prev, err := findOrigin(ctx, tx, o, "issue", in.ExternalID)
		if err != nil {
			return err
		}
		fp, current := own, ""
		if prev != nil {
			res.ID = prev.localID
			lock := tx.Dialect().ForUpdate()
			if dry {
				lock = ""
			}
			if err := tx.QueryRow(ctx, `SELECT fingerprint FROM issues WHERE id = $1`+lock, res.ID).Scan(&current); err != nil {
				return fmt.Errorf("leer issue: %w", err)
			}
			fp = current
		}
		switch in.Fingerprint {
		case "":
			res.NotGrouped = "no tiene ningún evento del que sacar la huella"
		case current:
			res.Grouped = true
		default:
			var other int64
			err := tx.QueryRow(ctx, `SELECT id FROM issues WHERE project_id = $1 AND fingerprint = $2`,
				projectID, in.Fingerprint).Scan(&other)
			switch {
			case errors.Is(err, db.ErrNoRows):
				fp, res.Grouped = in.Fingerprint, true
			case err != nil:
				return fmt.Errorf("buscar huella: %w", err)
			default:
				res.NotGrouped = fmt.Sprintf("su huella ya es la del issue %d", other)
			}
		}
		if prev == nil {
			return createImportedIssue(ctx, tx, o, projectID, in, fp, dry, &res)
		}
		var old ImportedIssue
		_ = json.Unmarshal(prev.imported, &old)
		return updateImportedIssue(ctx, tx, o, in, old, fp, dry, &res)
	})
	return res, err
}

func createImportedIssue(ctx context.Context, tx db.Tx, o Origin, projectID int64, in ImportedIssue, fp string,
	dry bool, res *ImportIssueResult) error {
	res.Action = ImportCreated
	if dry {
		return nil
	}
	err := tx.QueryRow(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, culprit, type, level, status, fixed_in_release,
			resolved_in_release, resolved_at, first_seen, last_seen, event_count, user_count, first_release)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, CASE WHEN $7 IN ('resolved', 'resolved_next') THEN `+tx.Dialect().Typed("$11", "timestamptz")+` END,
			$10, $11, $12, $13, $14)
		RETURNING id`,
		projectID, fp, in.Title, in.Culprit, in.Type, in.Level, in.Status, nullIfEmpty(in.FixedIn), nullIfEmpty(in.ResolvedIn),
		in.FirstSeen, in.LastSeen, in.Count, in.UserCount, nullIfEmpty(in.FirstRelease)).Scan(&res.ID)
	if err != nil {
		return fmt.Errorf("crear issue: %w", err)
	}
	if in.Status != "open" {
		if err := recordStatusChange(ctx, tx, res.ID, nil, SourceImport, "open", in.Status, statusRelease(in), nil); err != nil {
			return err
		}
	}
	return saveOrigin(ctx, tx, o, "issue", in.ExternalID, res.ID, in)
}

func updateImportedIssue(ctx context.Context, tx db.Tx, o Origin, in, old ImportedIssue, fp string,
	dry bool, res *ImportIssueResult) error {
	type row struct {
		fingerprint, title, culprit, typ, level, status, fixedIn, resolvedIn string
		firstRelease                                                         string
		count, users                                                         int64
		firstSeen, lastSeen                                                  time.Time
	}
	var cur row
	err := tx.QueryRow(ctx, `
		SELECT fingerprint, title, culprit, type, level, status, COALESCE(fixed_in_release, ''),
			COALESCE(resolved_in_release, ''), event_count, user_count, first_seen, last_seen,
			COALESCE(first_release, '')
		FROM issues WHERE id = $1`, res.ID).Scan(&cur.fingerprint, &cur.title, &cur.culprit, &cur.typ, &cur.level,
		&cur.status, &cur.fixedIn, &cur.resolvedIn, &cur.count, &cur.users, &cur.firstSeen, &cur.lastSeen, &cur.firstRelease)
	if err != nil {
		return fmt.Errorf("leer issue: %w", err)
	}
	next := cur
	if next.firstRelease == "" {
		next.firstRelease = in.FirstRelease
	}
	next.fingerprint = fp
	pick := func(dst *string, was, now string) {
		if was != now {
			*dst = now
		}
	}
	pick(&next.title, old.Title, in.Title)
	pick(&next.culprit, old.Culprit, in.Culprit)
	pick(&next.typ, old.Type, in.Type)
	pick(&next.level, old.Level, in.Level)
	if old.Status != in.Status || old.FixedIn != in.FixedIn || old.ResolvedIn != in.ResolvedIn {
		next.status, next.fixedIn, next.resolvedIn = in.Status, in.FixedIn, in.ResolvedIn
	}
	next.count = max(0, cur.count+in.Count-old.Count)
	next.users = max(cur.users, in.UserCount)
	if in.FirstSeen.Before(cur.firstSeen) {
		next.firstSeen = in.FirstSeen
	}
	if in.LastSeen.After(cur.lastSeen) {
		next.lastSeen = in.LastSeen
	}
	if next == cur && old.same(in) {
		return nil
	}
	res.Action = ImportUpdated
	if dry {
		return nil
	}
	_, err = tx.Exec(ctx, `
		UPDATE issues SET fingerprint = $2, title = $3, culprit = $4, type = $5, level = $6, status = $7,
			fixed_in_release = $8, resolved_in_release = $9, event_count = $10, user_count = $11,
			first_seen = $12, last_seen = $13, first_release = $14,
			resolved_at = CASE
				WHEN $7 NOT IN ('resolved', 'resolved_next') THEN NULL
				WHEN status IN ('resolved', 'resolved_next') THEN COALESCE(resolved_at, now())
				ELSE now() END
		WHERE id = $1`, res.ID, next.fingerprint, next.title, next.culprit, next.typ, next.level, next.status,
		nullIfEmpty(next.fixedIn), nullIfEmpty(next.resolvedIn), next.count, next.users, next.firstSeen, next.lastSeen,
		nullIfEmpty(next.firstRelease))
	if err != nil {
		return fmt.Errorf("actualizar issue: %w", err)
	}
	if next.status != cur.status || next.fixedIn != cur.fixedIn || next.resolvedIn != cur.resolvedIn {
		if StatusCategory(cur.status) != StatusCategory(next.status) {
			if err := enqueueSync(ctx, tx, res.ID, next.status, SourceImport); err != nil {
				return err
			}
		}
		st := ImportedIssue{Status: next.status, FixedIn: next.fixedIn, ResolvedIn: next.resolvedIn}
		if err := recordStatusChange(ctx, tx, res.ID, nil, SourceImport, cur.status, next.status, statusRelease(st), nil); err != nil {
			return err
		}
	}
	return saveOrigin(ctx, tx, o, "issue", in.ExternalID, res.ID, in)
}

func statusRelease(in ImportedIssue) *string {
	switch {
	case in.Status == "resolved" && in.FixedIn != "":
		return &in.FixedIn
	case in.Status == "resolved_next":
		return &in.ResolvedIn
	}
	return nil
}

// HasEvent dice si el proyecto ya tiene guardado el evento.
func (s *Store) HasEvent(ctx context.Context, projectID int64, eventID string) (bool, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE project_id = $1 AND event_id = $2)`,
		projectID, eventID).Scan(&ok)
	if err != nil {
		return false, fmt.Errorf("buscar evento: %w", err)
	}
	return ok, nil
}

// SettleImportedUsers deja el número de usuarios de un issue importado en el
// mayor entre el que trae el otro servicio y los distintos de sus eventos
// guardados, que la ingesta suma uno a uno al guardar cada evento importado.
func (s *Store) SettleImportedUsers(ctx context.Context, issueID, users int64) error {
	n := s.pool.Dialect().Greatest("$2", "(SELECT count(*) FROM issue_users WHERE issue_id = $1)")
	_, err := s.pool.Exec(ctx, `
		UPDATE issues SET user_count = `+n+`
		WHERE id = $1 AND user_count IS DISTINCT FROM `+n,
		issueID, users)
	if err != nil {
		return fmt.Errorf("contar usuarios: %w", err)
	}
	return nil
}
