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

func (s *Store) ProjectByID(ctx context.Context, id int64) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT id, name, public_key, created_at FROM projects WHERE id = $1`,
		id).Scan(&p.ID, &p.Name, &p.PublicKey, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("buscar proyecto: %w", err)
	}
	return p, nil
}

func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, public_key, created_at FROM projects ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("listar proyectos: %w", err)
	}
	ps, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Project, error) {
		var p Project
		err := r.Scan(&p.ID, &p.Name, &p.PublicKey, &p.CreatedAt)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("listar proyectos: %w", err)
	}
	return ps, nil
}

// SetProjectExtraLabel activa o apaga la etiqueta extra en las issues de Linear
// del proyecto con ese nombre.
func (s *Store) SetProjectExtraLabel(ctx context.Context, name string, on bool) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE projects SET linear_extra_label = $2
		WHERE name = $1 AND (SELECT count(*) FROM projects WHERE name = $1) = 1`, name, on)
	if err != nil {
		return fmt.Errorf("cambiar la etiqueta extra: %w", err)
	}
	if tag.RowsAffected() == 0 {
		var n int
		if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM projects WHERE name = $1`, name).Scan(&n); err != nil {
			return fmt.Errorf("buscar proyecto: %w", err)
		}
		if n > 1 {
			return fmt.Errorf("%w: hay %d proyectos llamados %q", ErrInvalid, n, name)
		}
		return ErrNotFound
	}
	return nil
}
