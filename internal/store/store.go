// Package store guarda proyectos, issues y eventos en Postgres o en SQLite.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/migrations"
	sqlitemigrations "github.com/intake-es/mothlog/migrations/sqlite"
)

var ErrNotFound = errors.New("not found")

type Store struct {
	pool    db.DB
	hashKey atomic.Pointer[[]byte]
	// lang es el idioma del resumen de los proyectos que no lo han configurado.
	lang string
}

type Project struct {
	ID        int64
	Name      string
	PublicKey string
	CreatedAt time.Time
	// LinearLabelIDs son las etiquetas de Linear propias del proyecto.
	LinearLabelIDs []string
}

// Open conecta con la base de url y aplica las migraciones pendientes:
// postgres://… es Postgres y sqlite:///ruta/mothlog.db, un fichero de SQLite.
// Con SQLite coge el cerrojo del fichero, para que no corran dos instancias
// del servicio sobre él.
func Open(ctx context.Context, url string, log *slog.Logger) (*Store, error) {
	return open(ctx, url, log, true)
}

// OpenForCommand es Open para las órdenes de la línea de comandos, que
// pueden correr con el servicio arrancado: con SQLite no coge el cerrojo.
func OpenForCommand(ctx context.Context, url string, log *slog.Logger) (*Store, error) {
	return open(ctx, url, log, false)
}

func open(ctx context.Context, url string, log *slog.Logger, exclusive bool) (*Store, error) {
	if db.IsSQLiteURL(url) {
		lite, err := db.OpenSQLite(ctx, url, db.SQLiteOptions{Exclusive: exclusive})
		if err != nil {
			return nil, err
		}
		if err := migrateSQLite(ctx, lite.WriteDB(), log); err != nil {
			lite.Close()
			return nil, err
		}
		return &Store{pool: lite}, nil
	}
	pool, err := db.OpenPostgres(ctx, url)
	if err != nil {
		return nil, fmt.Errorf("connect to postgres: %w", err)
	}
	if err := migrate(ctx, pool.SQLDB(), log); err != nil {
		pool.Close()
		return nil, err
	}
	return &Store{pool: pool}, nil
}

func migrate(ctx context.Context, sqlDB *sql.DB, log *slog.Logger) error {
	defer func() { _ = sqlDB.Close() }()
	// El cerrojo de sesión evita que dos réplicas migren a la vez.
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return err
	}
	p, err := goose.NewProvider(goose.DialectPostgres, sqlDB, migrations.FS,
		goose.WithSessionLocker(locker), goose.WithSlog(log),
		// Las migraciones de ramas paralelas pueden llegar en otro orden que su número.
		goose.WithAllowOutofOrder(true))
	if err != nil {
		return fmt.Errorf("prepare migrations: %w", err)
	}
	return up(ctx, p, log)
}

// migrateSQLite no necesita cerrojo: con SQLite hay una sola instancia.
func migrateSQLite(ctx context.Context, sqlDB *sql.DB, log *slog.Logger) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, sqlDB, sqlitemigrations.FS,
		goose.WithSlog(log), goose.WithAllowOutofOrder(true))
	if err != nil {
		return fmt.Errorf("prepare migrations: %w", err)
	}
	return up(ctx, p, log)
}

func up(ctx context.Context, p *goose.Provider, log *slog.Logger) error {
	res, err := p.Up(ctx)
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	for _, r := range res {
		log.Info("migration applied", "file", r.Source.Path, "duration", r.Duration)
	}
	return nil
}

func (s *Store) Close() { s.pool.Close() }

// sqlite dice si la base es SQLite.
func (s *Store) sqlite() bool { return s.pool.Dialect().Name() == "sqlite" }

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) CreateProject(ctx context.Context, name, publicKey string) (Project, error) {
	p := Project{Name: name, PublicKey: publicKey, LinearLabelIDs: []string{}}
	err := s.pool.QueryRow(ctx,
		`INSERT INTO projects (name, public_key) VALUES ($1, $2) RETURNING id, created_at`,
		name, publicKey).Scan(&p.ID, &p.CreatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("create project: %w", err)
	}
	return p, nil
}

func (s *Store) ProjectByPublicKey(ctx context.Context, publicKey string) (Project, error) {
	var p Project
	err := s.pool.QueryRow(ctx,
		`SELECT `+projectColumns+` FROM projects WHERE public_key = $1`,
		publicKey).Scan(p.fields()...)
	if errors.Is(err, db.ErrNoRows) {
		return Project{}, ErrNotFound
	}
	if err != nil {
		return Project{}, fmt.Errorf("find project: %w", err)
	}
	return p, nil
}
