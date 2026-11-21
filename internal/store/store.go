// Package store guarda proyectos, issues y eventos en Postgres.
package store

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/intake-es/intake-errors/migrations"
)

var ErrNotFound = errors.New("no encontrado")

type Store struct {
	pool *pgxpool.Pool
}

type Project struct {
	ID        int64
	Name      string
	PublicKey string
	CreatedAt time.Time
	// LinearLabelIDs son las etiquetas de Linear propias del proyecto.
	LinearLabelIDs []string
}

// Open conecta con Postgres y aplica las migraciones pendientes.
func Open(ctx context.Context, url string, log *slog.Logger) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("conectar con postgres: %w", err)
	}
	if err := migrate(ctx, pool, log); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func migrate(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	// El cerrojo de sesión evita que dos réplicas migren a la vez.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS,
		goose.WithSessionLocker(locker), goose.WithSlog(log))
	if err != nil {
		return fmt.Errorf("preparar migraciones: %w", err)
	}
	res, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("aplicar migraciones: %w", err)
	}
	for _, r := range res {
		log.Info("migración aplicada", "fichero", r.Source.Path, "duración", r.Duration)
	}
	return nil
}

func (s *Store) Close() { s.pool.Close() }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) CreateProject(ctx context.Context, name, publicKey string) (Project, error) {
	p := Project{Name: name, PublicKey: publicKey, LinearLabelIDs: []string{}}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO projects (name, public_key) VALUES ($1, $2) RETURNING id, created_at`,
		name, publicKey).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("crear proyecto: %w", err)
	}
	return p, nil
}

func (s *Store) ProjectByPublicKey(ctx context.Context, publicKey string) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE public_key = $1`,
		publicKey).Scan(p.fields()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("buscar proyecto: %w", err)
	}
	return p, nil
}
