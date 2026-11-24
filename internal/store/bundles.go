package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/intake-es/mothlog/internal/db"
)

// PutUploadChunks guarda trozos de una subida por trozos, por su sha1 en hexadecimal.
func (s *Store) PutUploadChunks(ctx context.Context, chunks map[string][]byte) error {
	b := &db.Batch{}
	for sum, data := range chunks {
		b.Queue(`
			INSERT INTO upload_chunks (sha1, data) VALUES ($1, $2)
			ON CONFLICT (sha1) DO UPDATE SET uploaded_at = now()`, sum, data)
	}
	if err := s.pool.SendBatch(ctx, b); err != nil {
		return fmt.Errorf("save chunks: %w", err)
	}
	return nil
}

// MissingUploadChunks devuelve, en su orden, los sha1 de sums que no están guardados.
func (s *Store) MissingUploadChunks(ctx context.Context, sums []string) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT sha1 FROM upload_chunks WHERE `+s.pool.Dialect().InList("sha1", "$1"), sums)
	if err != nil {
		return nil, fmt.Errorf("find chunks: %w", err)
	}
	have, err := db.CollectRows(rows, db.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("find chunks: %w", err)
	}
	found := make(map[string]bool, len(have))
	for _, h := range have {
		found[h] = true
	}
	missing := []string{}
	for _, sum := range sums {
		if !found[sum] {
			missing = append(missing, sum)
			found[sum] = true
		}
	}
	return missing, nil
}

// UploadChunk devuelve un trozo guardado, o ErrNotFound.
func (s *Store) UploadChunk(ctx context.Context, sum string) ([]byte, error) {
	var data []byte
	err := s.pool.QueryRow(ctx, `SELECT data FROM upload_chunks WHERE sha1 = $1`, sum).Scan(&data)
	if errors.Is(err, db.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read chunk: %w", err)
	}
	return data, nil
}

// DeleteUploadChunksBefore borra los trozos subidos antes de t.
func (s *Store) DeleteUploadChunksBefore(ctx context.Context, t time.Time) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM upload_chunks WHERE uploaded_at < $1`, t)
	if err != nil {
		return 0, fmt.Errorf("delete chunks: %w", err)
	}
	return tag.RowsAffected(), nil
}

// Bundle es un artifact bundle ya abierto: sus mapas y a qué proyectos van.
type Bundle struct {
	Checksum   string
	ProjectIDs []int64
	Release    string
	Dist       string
	Files      []SourceMapFile
}

// BundleAssembled dice si el bundle con ese checksum ya está guardado en todos esos proyectos.
func (s *Store) BundleAssembled(ctx context.Context, checksum string, projectIDs []int64) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM artifact_bundles WHERE checksum = $1 AND `+s.pool.Dialect().InList("project_id", "$2"),
		checksum, projectIDs).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("find bundle: %w", err)
	}
	return n == len(projectIDs), nil
}

// PutBundle guarda los mapas del bundle en cada proyecto y lo apunta como
// ensamblado, todo o nada. Si un proyecto no existe devuelve ErrNotFound.
func (s *Store) PutBundle(ctx context.Context, b Bundle) error {
	return db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		for _, p := range b.ProjectIDs {
			if err := putSourceMaps(ctx, tx, p, b.Release, b.Files); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				INSERT INTO artifact_bundles (project_id, checksum, release, dist, files)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (project_id, checksum) DO UPDATE SET assembled_at = now()`,
				p, b.Checksum, b.Release, b.Dist, len(b.Files))
			if db.IsForeignKeyViolation(err) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("save bundle: %w", err)
			}
		}
		return nil
	})
}
