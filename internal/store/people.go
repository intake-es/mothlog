package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// SessionsValidSince es desde cuándo valen las sesiones de login: las
// emitidas hasta ese momento están revocadas. ErrNotFound si no hay nadie
// con ese login.
func (s *Store) SessionsValidSince(ctx context.Context, login string) (time.Time, error) {
	var since time.Time
	err := s.pool.QueryRow(ctx, `SELECT sessions_valid_since FROM people WHERE login = $1`, login).Scan(&since)
	if errors.Is(err, db.ErrNoRows) {
		return time.Time{}, ErrNotFound
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("leer sesiones: %w", err)
	}
	return since, nil
}

// RevokeSessions invalida las sesiones de login emitidas hasta at. Login se
// compara sin mirar mayúsculas, como al asignar; devuelve el login guardado.
func (s *Store) RevokeSessions(ctx context.Context, login string, at time.Time) (string, error) {
	var got string
	err := s.pool.QueryRow(ctx, `
		UPDATE people SET sessions_valid_since = $2 WHERE lower(login) = lower($1) RETURNING login`,
		login, at.UTC().Truncate(time.Microsecond)).Scan(&got)
	if errors.Is(err, db.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("revocar sesiones: %w", err)
	}
	return got, nil
}

// RenamePerson cambia el login de una persona a base o, si ya es de otra sin
// mirar mayúsculas, a base~2, base~3... Lo que apunta a ella (asignados,
// comentarios, historial) la sigue, y sus sesiones dejan de valer porque
// van firmadas con el login de antes. Devuelve el login nuevo.
func (s *Store) RenamePerson(ctx context.Context, from, base string) (string, error) {
	var to string
	err := db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		to = ""
		for n := 1; n <= maxLoginSuffix; n++ {
			candidate := base
			if n > 1 {
				candidate = fmt.Sprintf("%s~%d", base, n)
			}
			var taken bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM people WHERE lower(login) = lower($1))`, candidate).Scan(&taken); err != nil {
				return err
			}
			if !taken {
				to = candidate
				break
			}
		}
		if to == "" {
			return fmt.Errorf("%q y sus %d variantes ya son de otras personas", base, maxLoginSuffix)
		}
		tag, err := tx.Exec(ctx, `UPDATE people SET login = $2 WHERE login = $1`, from, to)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		for _, q := range []string{
			`UPDATE issues SET assignee = $2 WHERE assignee_kind = 'user' AND assignee = $1`,
			`UPDATE issue_comments SET author = $2 WHERE author_kind = 'user' AND author = $1`,
			`UPDATE issue_status_changes SET author = $2 WHERE author_kind = 'user' AND author = $1`,
			`UPDATE issue_assignment_changes SET author = $2 WHERE author_kind = 'user' AND author = $1`,
			`UPDATE issue_assignment_changes SET from_name = $2 WHERE from_kind = 'user' AND from_name = $1`,
			`UPDATE issue_assignment_changes SET to_name = $2 WHERE to_kind = 'user' AND to_name = $1`,
			`UPDATE issue_links SET author = $2 WHERE author_kind = 'user' AND author = $1`,
		} {
			if _, err := tx.Exec(ctx, q, from, to); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, ErrNotFound) {
		return "", err
	}
	if err != nil {
		return "", fmt.Errorf("renombrar persona: %w", err)
	}
	return to, nil
}
