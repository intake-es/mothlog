package store

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

// SourceMapFile es un .map subido para un release; File es el nombre del .js
// al que corresponde. Con DebugID se guarda por él y no sustituye a los que
// solo tienen nombre.
type SourceMapFile struct {
	File    string
	Dist    string
	URL     string
	DebugID string
	Map     []byte
}

// PutSourceMaps guarda los mapas comprimidos, sustituyendo los que ya hubiera
// con el mismo fichero (o el mismo debug ID). Si el proyecto no existe
// devuelve ErrNotFound.
func (s *Store) PutSourceMaps(ctx context.Context, projectID int64, release string, files []SourceMapFile) error {
	return db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		return putSourceMaps(ctx, tx, projectID, release, files)
	})
}

func putSourceMaps(ctx context.Context, tx db.Tx, projectID int64, release string, files []SourceMapFile) error {
	for _, f := range files {
		gz, err := gzipBytes(f.Map)
		if err != nil {
			return err
		}
		q := `
			INSERT INTO sourcemaps (project_id, release, dist, file, url, map_gzip, size)
			VALUES ($1, $2, $3, $4, $5, $6, $7)
			ON CONFLICT (project_id, release, dist, file) WHERE debug_id IS NULL
			DO UPDATE SET url = EXCLUDED.url, map_gzip = EXCLUDED.map_gzip, size = EXCLUDED.size, uploaded_at = now()`
		args := []any{projectID, release, f.Dist, f.File, f.URL, gz, len(f.Map)}
		if f.DebugID != "" {
			q = `
			INSERT INTO sourcemaps (project_id, release, dist, file, url, map_gzip, size, debug_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
			ON CONFLICT (project_id, debug_id) WHERE debug_id IS NOT NULL
			DO UPDATE SET release = EXCLUDED.release, dist = EXCLUDED.dist, file = EXCLUDED.file, url = EXCLUDED.url,
				map_gzip = EXCLUDED.map_gzip, size = EXCLUDED.size, uploaded_at = now()`
			args = append(args, f.DebugID)
		}
		_, err = tx.Exec(ctx, q, args...)
		if db.IsForeignKeyViolation(err) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("save source map %s: %w", f.File, err)
		}
	}
	return nil
}

// SourceMap devuelve el mapa descomprimido del fichero en ese release, o
// ErrNotFound. Prefiere el de la misma dist y, si no, uno sin dist; entre
// varios, el más nuevo.
func (s *Store) SourceMap(ctx context.Context, projectID int64, release, dist, file string) ([]byte, error) {
	return s.sourceMap(ctx, `
		SELECT map_gzip FROM sourcemaps
		WHERE project_id = $1 AND release = $2 AND file = $3 AND dist IN ($4, '')
		ORDER BY dist = $4 DESC, uploaded_at DESC, id DESC LIMIT 1`,
		projectID, release, file, dist)
}

// SourceMapByDebugID devuelve el mapa con ese debug ID, o ErrNotFound.
func (s *Store) SourceMapByDebugID(ctx context.Context, projectID int64, debugID string) ([]byte, error) {
	return s.sourceMap(ctx, `SELECT map_gzip FROM sourcemaps WHERE project_id = $1 AND debug_id = $2`, projectID, debugID)
}

func (s *Store) sourceMap(ctx context.Context, q string, args ...any) ([]byte, error) {
	var gz []byte
	err := s.pool.QueryRow(ctx, q, args...).Scan(&gz)
	if errors.Is(err, db.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read source map: %w", err)
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		return nil, fmt.Errorf("read source map: %w", err)
	}
	b, err := io.ReadAll(zr)
	if err != nil {
		return nil, fmt.Errorf("read source map: %w", err)
	}
	return b, nil
}

// DeleteSourceMapsBefore borra los mapas subidos y los bundles ensamblados antes de t.
func (s *Store) DeleteSourceMapsBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sourcemaps WHERE uploaded_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete source maps: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM artifact_bundles WHERE assembled_at < $1`, t); err != nil {
		return 0, fmt.Errorf("delete bundles: %w", err)
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
