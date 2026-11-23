// Package db es la base de datos tal como la usa el resto del servicio:
// consultas, filas, lotes y transacciones, sin atarse a un motor. La
// implementación de hoy es la de Postgres (postgres.go); el SQL que cambia de
// un motor a otro pasa por Dialect.
package db

import (
	"context"
	"errors"
	"time"
)

var (
	// ErrNoRows es lo que devuelve Row.Scan cuando la consulta no trae filas.
	ErrNoRows = errors.New("no rows in result set")
	// ErrTxClosed es lo que devuelven Commit y Rollback con la transacción ya acabada.
	ErrTxClosed = errors.New("tx is closed")
)

// Row es una fila de QueryRow; los errores de la consulta salen al leerla.
type Row interface {
	Scan(dest ...any) error
}

// CollectableRow es lo que CollectRows deja usar de cada fila.
type CollectableRow interface {
	Scan(dest ...any) error
}

// Rows son las filas de Query; hay que cerrarlas, o leerlas hasta el final.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	// Values da la fila actual con los tipos de Go que elige el motor.
	Values() ([]any, error)
	Err() error
	Close()
}

// Result es lo que devuelve Exec.
type Result struct{ rowsAffected int64 }

func NewResult(rowsAffected int64) Result { return Result{rowsAffected: rowsAffected} }

func (r Result) RowsAffected() int64 { return r.rowsAffected }

// Querier es lo que comparten el pool y una transacción.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (Result, error)
	Query(ctx context.Context, sql string, args ...any) (Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) Row
	// SendBatch manda las consultas del lote juntas y espera a todas.
	SendBatch(ctx context.Context, b *Batch) error
	Dialect() Dialect
}

// Beginner abre transacciones: en el pool, una nueva; en una transacción,
// un savepoint.
type Beginner interface {
	Querier
	Begin(ctx context.Context) (Tx, error)
}

type Tx interface {
	Beginner
	Commit(ctx context.Context) error
	Rollback(ctx context.Context) error
}

type TxOptions struct {
	ReadOnly bool
}

// DB es el pool de conexiones.
type DB interface {
	Beginner
	BeginTx(ctx context.Context, opts TxOptions) (Tx, error)
	Ping(ctx context.Context) error
	Stats() Stats
	Close()
}

// Stats es el estado del pool, para /metrics.
type Stats struct {
	InUse, Idle, Constructing, Max int64
	// Waits son las veces que pedir una conexión tuvo que esperar, y
	// WaitTime lo que esperaron en total.
	Waits    int64
	WaitTime time.Duration
}

// BeginFunc corre fn en una transacción de b: la confirma si fn no falla y
// la deshace si falla.
func BeginFunc(ctx context.Context, b Beginner, fn func(Tx) error) (err error) {
	tx, err := b.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() {
		if rbErr := tx.Rollback(ctx); rbErr != nil && !errors.Is(rbErr, ErrTxClosed) {
			err = rbErr
		}
	}()
	if fErr := fn(tx); fErr != nil {
		_ = tx.Rollback(ctx)
		return fErr
	}
	return tx.Commit(ctx)
}

// CollectRows lee todas las filas con fn y las cierra. Sin filas, da un
// slice vacío, no nil.
func CollectRows[T any](rows Rows, fn func(CollectableRow) (T, error)) ([]T, error) {
	defer rows.Close()
	out := []T{}
	for rows.Next() {
		v, err := fn(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// RowTo lee una fila de una sola columna.
func RowTo[T any](row CollectableRow) (T, error) {
	var v T
	err := row.Scan(&v)
	return v, err
}

// ForEachRow lee cada fila en scans, llama a fn y cierra las filas.
func ForEachRow(rows Rows, scans []any, fn func() error) error {
	defer rows.Close()
	for rows.Next() {
		if err := rows.Scan(scans...); err != nil {
			return err
		}
		if err := fn(); err != nil {
			return err
		}
	}
	return rows.Err()
}

// Batch son consultas que se mandan juntas con SendBatch, en su orden. Un
// error en una para el lote y es el que devuelve SendBatch.
type Batch struct {
	Queued []*QueuedQuery
}

type QueuedQuery struct {
	SQL  string
	Args []any
	// Como mucho uno de los dos: lo que se hace con el resultado.
	OnExec  func(Result) error
	OnQuery func(Rows) error
}

func (b *Batch) Queue(sql string, args ...any) *QueuedQuery {
	q := &QueuedQuery{SQL: sql, Args: args}
	b.Queued = append(b.Queued, q)
	return q
}

func (b *Batch) Len() int { return len(b.Queued) }

// Exec hace que fn reciba el resultado de la consulta.
func (q *QueuedQuery) Exec(fn func(Result) error) { q.OnExec, q.OnQuery = fn, nil }

// Query hace que fn lea las filas de la consulta; no hace falta cerrarlas.
func (q *QueuedQuery) Query(fn func(Rows) error) { q.OnQuery, q.OnExec = fn, nil }
