package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Pool da acceso al pool para quien necesita sus propias transacciones.
func (s *Store) Pool() *pgxpool.Pool { return s.pool }

const projectColumns = `id, name, public_key, created_at, linear_label_ids`

func (p *Project) fields() []any {
	return []any{&p.ID, &p.Name, &p.PublicKey, &p.CreatedAt, &p.LinearLabelIDs}
}

func (s *Store) ProjectByID(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE id = $1`,
		id).Scan(p.fields()...)
	if errors.Is(err, pgx.ErrNoRows) {
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
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Project, error) {
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
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Project, error) {
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
