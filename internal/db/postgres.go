package db

import (
	"context"
	"database/sql"
	"errors"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// Postgres es DB sobre un pool de pgx.
type Postgres struct {
	pool *pgxpool.Pool
}

// OpenPostgres prepara el pool; no conecta hasta la primera consulta.
func OpenPostgres(ctx context.Context, url string) (*Postgres, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	return &Postgres{pool: pool}, nil
}

// NewPostgres usa un pool de pgx ya abierto; cerrarlo cierra el pool.
func NewPostgres(pool *pgxpool.Pool) *Postgres { return &Postgres{pool: pool} }

// SQLDB es el mismo pool como *sql.DB, para goose; hay que cerrarlo.
func (p *Postgres) SQLDB() *sql.DB { return stdlib.OpenDBFromPool(p.pool) }

func (p *Postgres) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	tag, err := p.pool.Exec(ctx, sql, args...)
	return Result{rowsAffected: tag.RowsAffected()}, err
}

func (p *Postgres) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return p.pool.Query(ctx, sql, args...)
}

func (p *Postgres) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return pgRow{p.pool.QueryRow(ctx, sql, args...)}
}

func (p *Postgres) SendBatch(ctx context.Context, b *Batch) error {
	return p.pool.SendBatch(ctx, pgBatch(b)).Close()
}

func (p *Postgres) Dialect() Dialect { return PostgresDialect }

func (p *Postgres) Begin(ctx context.Context) (Tx, error) {
	tx, err := p.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgTx{tx}, nil
}

func (p *Postgres) BeginTx(ctx context.Context, opts TxOptions) (Tx, error) {
	var o pgx.TxOptions
	if opts.ReadOnly {
		o.AccessMode = pgx.ReadOnly
	}
	tx, err := p.pool.BeginTx(ctx, o)
	if err != nil {
		return nil, err
	}
	return pgTx{tx}, nil
}

func (p *Postgres) Ping(ctx context.Context) error { return p.pool.Ping(ctx) }

func (p *Postgres) Close() { p.pool.Close() }

func (p *Postgres) Stats() Stats {
	st := p.pool.Stat()
	return Stats{
		InUse: int64(st.AcquiredConns()), Idle: int64(st.IdleConns()), Constructing: int64(st.ConstructingConns()),
		Max: int64(st.MaxConns()), Waits: st.EmptyAcquireCount(), WaitTime: st.EmptyAcquireWaitTime(),
	}
}

type pgTx struct{ tx pgx.Tx }

func (t pgTx) Exec(ctx context.Context, sql string, args ...any) (Result, error) {
	tag, err := t.tx.Exec(ctx, sql, args...)
	return Result{rowsAffected: tag.RowsAffected()}, err
}

func (t pgTx) Query(ctx context.Context, sql string, args ...any) (Rows, error) {
	return t.tx.Query(ctx, sql, args...)
}

func (t pgTx) QueryRow(ctx context.Context, sql string, args ...any) Row {
	return pgRow{t.tx.QueryRow(ctx, sql, args...)}
}

func (t pgTx) SendBatch(ctx context.Context, b *Batch) error {
	return t.tx.SendBatch(ctx, pgBatch(b)).Close()
}

func (t pgTx) Dialect() Dialect { return PostgresDialect }

// Begin abre un savepoint.
func (t pgTx) Begin(ctx context.Context) (Tx, error) {
	tx, err := t.tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	return pgTx{tx}, nil
}

func (t pgTx) Commit(ctx context.Context) error { return txErr(t.tx.Commit(ctx)) }

func (t pgTx) Rollback(ctx context.Context) error { return txErr(t.tx.Rollback(ctx)) }

func txErr(err error) error {
	if errors.Is(err, pgx.ErrTxClosed) {
		return ErrTxClosed
	}
	return err
}

type pgRow struct{ row pgx.Row }

func (r pgRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

func pgBatch(b *Batch) *pgx.Batch {
	pb := &pgx.Batch{}
	for _, q := range b.Queued {
		qq := pb.Queue(q.SQL, q.Args...)
		switch {
		case q.OnExec != nil:
			fn := q.OnExec
			qq.Exec(func(tag pgconn.CommandTag) error { return fn(Result{rowsAffected: tag.RowsAffected()}) })
		case q.OnQuery != nil:
			fn := q.OnQuery
			qq.Query(func(rows pgx.Rows) error { return fn(rows) })
		}
	}
	return pb
}

// IsUniqueViolation dice si err es por una restricción UNIQUE.
func IsUniqueViolation(err error) bool { return pgCode(err) == "23505" }

// IsForeignKeyViolation dice si err es por una clave ajena que no existe.
func IsForeignKeyViolation(err error) bool { return pgCode(err) == "23503" }

func pgCode(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}
