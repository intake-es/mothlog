package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// SourceMapFile es un .map subido para un release; File es el nombre del .js
// al que corresponde.
type SourceMapFile struct {
	File string
	Map  []byte
}

// PutSourceMaps guarda los mapas comprimidos, sustituyendo los que ya hubiera
// con el mismo fichero. Si el proyecto no existe devuelve ErrNotFound.
func (s *Store) PutSourceMaps(ctx context.Context, projectID int64, release string, files []SourceMapFile) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("guardar source maps: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, f := range files {
		gz, err := gzipBytes(f.Map)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO sourcemaps (project_id, release, file, map_gzip, size)
			VALUES ($1, $2, $3, $4, $5)
			ON CONFLICT (project_id, release, file)
			DO UPDATE SET map_gzip = EXCLUDED.map_gzip, size = EXCLUDED.size, uploaded_at = now()`,
			projectID, release, f.File, gz, len(f.Map))
		var pe *pgconn.PgError
		if errors.As(err, &pe) && pe.Code == "23503" {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("guardar source map %s: %w", f.File, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("guardar source maps: %w", err)
	}
	return nil
}

// SourceMap devuelve el mapa descomprimido o ErrNotFound.
func (s *Store) SourceMap(ctx context.Context, projectID int64, release, file string) ([]byte, error) {
	var gz []byte
	err := s.pool.QueryRow(ctx,
		`SELECT map_gzip FROM sourcemaps WHERE project_id = $1 AND release = $2 AND file = $3`,
		projectID, release, file).Scan(&gz)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("leer source map: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("leer source map: %w", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("leer source map: %w", err)
	}
	return b, nil
}

// DeleteSourceMapsBefore borra los mapas subidos antes de t.
func (s *Store) DeleteSourceMapsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sourcemaps WHERE uploaded_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("borrar source maps: %w", err)
	}
	return tag.RowsAffected(), nil
}

func gzipBytes(b []byte) ([]byte, error) {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	if _, err := zw.Write(b); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
