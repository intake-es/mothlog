package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/intake-es/intake-errors/internal/db"
)

// Pool da acceso al pool para quien necesita sus propias transacciones.
func (s *Store) Pool() db.DB { return s.pool }

const projectColumns = `id, name, public_key, created_at, linear_label_ids`

func (p *Project) fields() []any {
	return []any{&p.ID, &p.Name, &p.PublicKey, &p.CreatedAt, &p.LinearLabelIDs}
}

func (s *Store) ProjectByID(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`,
		id).Scan(p.fields()...)
	if errors.Is(err, db.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("buscar proyecto: %w", err)
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectColumns+` FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listar proyectos: %w", err)
	}
	ps, err := db.CollectRows(rows, func(r db.CollectableRow) (Project, error) {
		var p Project
		err := r.Scan(p.fields()...)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("listar proyectos: %w", err)
	}
	return ps, nil
}

// ProjectByName busca el proyecto con ese nombre; si hay más de uno, es
// ErrInvalid, porque no se sabe a cuál se refiere.
func (s *Store) ProjectByName(ctx context.Context, name string) (Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+projectColumns+` FROM projects WHERE name = $1 ORDER BY id`, name)
	if err != nil {
		return Project{}, fmt.Errorf("buscar proyecto: %w", err)
	}
	ps, err := db.CollectRows(rows, func(r db.CollectableRow) (Project, error) {
		var p Project
		err := r.Scan(p.fields()...)
		return p, err
	})
	if err != nil {
		return Project{}, fmt.Errorf("buscar proyecto: %w", err)
	}
	switch len(ps) {
	case 0:
		return Project{}, ErrNotFound
	case 1:
		return ps[0], nil
	default:
		return Project{}, fmt.Errorf("%w: hay %d proyectos llamados %q", ErrInvalid, len(ps), name)
	}
}

// SetProjectLabels sustituye las etiquetas de Linear propias del proyecto con
// ese nombre; ids vacío las quita.
func (s *Store) SetProjectLabels(ctx context.Context, name string, ids []string) error {
	p, err := s.ProjectByName(ctx, name)
	if err != nil {
		return err
	}
	if ids == nil {
		ids = []string{}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE projects SET linear_label_ids = $2 WHERE id = $1`, p.ID, ids); err != nil {
		return fmt.Errorf("cambiar etiquetas: %w", err)
	}
	return nil
}

// DefaultAlerts dice a qué canales por defecto avisa un proyecto sin reglas.
type DefaultAlerts struct {
	Slack  bool `json:"slack"`
	Linear bool `json:"linear"`
}

// DefaultAlertsOf lee los canales por defecto del proyecto dentro de q.
func DefaultAlertsOf(ctx context.Context, q Querier, projectID int64) (DefaultAlerts, error) {
	var d DefaultAlerts
	err := q.QueryRow(ctx, `SELECT default_slack, default_linear FROM projects WHERE id = $1`, projectID).Scan(&d.Slack, &d.Linear)
	if errors.Is(err, db.ErrNoRows) {
		return d, ErrNotFound
	}
	if err != nil {
		return d, fmt.Errorf("leer avisos por defecto: %w", err)
	}
	return d, nil
}

func (s *Store) DefaultAlerts(ctx context.Context, projectID int64) (DefaultAlerts, error) {
	return DefaultAlertsOf(ctx, s.pool, projectID)
}

// SetDefaultAlerts guarda los canales por defecto del proyecto.
func (s *Store) SetDefaultAlerts(ctx context.Context, projectID int64, d DefaultAlerts) (DefaultAlerts, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE projects SET default_slack = $2, default_linear = $3 WHERE id = $1`,
		projectID, d.Slack, d.Linear)
	if err != nil {
		return d, fmt.Errorf("guardar avisos por defecto: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return d, ErrNotFound
	}
	return d, nil
}

// HasIssueRules dice si el proyecto tiene reglas de errores, encendidas o no:
// con ellas, los canales por defecto no cuentan.
func (s *Store) HasIssueRules(ctx context.Context, projectID int64) (bool, error) {
	var has bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM alert_rules WHERE project_id = $1 AND `+IssueRulesSQL(s.pool.Dialect())+`)`,
		projectID).Scan(&has)
	if err != nil {
		return false, fmt.Errorf("leer reglas: %w", err)
	}
	return has, nil
}
