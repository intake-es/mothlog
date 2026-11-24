package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

// SQLite es DB sobre un fichero de SQLite: una sola conexión escribe, con
// BEGIN IMMEDIATE, y varias leen. Las fechas se guardan como microsegundos
// desde 1970 en UTC y las listas, los structs y los mapas como texto JSON;
// la conversión la hacen los argumentos y Scan (sqlite_convert.go).
type SQLite struct {
	path  string
	write *sql.DB
	read  *sql.DB
	lock  *os.File
	sp    atomic.Int64
}

// IsSQLiteURL dice si url pide SQLite: sqlite:///ruta/absoluta.db o
// sqlite://ruta/relativa.db.
func IsSQLiteURL(url string) bool { return strings.HasPrefix(url, "sqlite:") }

// SQLitePath es la ruta del fichero de una URL sqlite:.
func SQLitePath(raw string) (string, error) {
	rest, ok := strings.CutPrefix(raw, "sqlite:")
	if !ok {
		return "", fmt.Errorf("%q does not start with sqlite://", raw)
	}
	rest = strings.TrimPrefix(rest, "//")
	if i := strings.IndexByte(rest, '?'); i >= 0 {
		rest = rest[:i]
	}
	p, err := url.PathUnescape(rest)
	if err != nil {
		return "", fmt.Errorf("path of %q: %w", raw, err)
	}
	if p == "" {
		return "", fmt.Errorf("missing the file path in %q, like sqlite:///var/lib/mothlog/mothlog.db", raw)
	}
	return p, nil
}

// SQLiteOptions son las opciones de OpenSQLite.
type SQLiteOptions struct {
	// Exclusive coge el cerrojo del fichero: si otro proceso lo tiene, falla.
	Exclusive bool
}

// ErrLocked es el error de OpenSQLite cuando otra instancia tiene el fichero.
var ErrLocked = errors.New("another instance has the database open")

// OpenSQLite abre (o crea) el fichero de la URL.
func OpenSQLite(ctx context.Context, rawURL string, opts SQLiteOptions) (*SQLite, error) {
	path, err := SQLitePath(rawURL)
	if err != nil {
		return nil, err
	}
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	s := &SQLite{path: path}
	if opts.Exclusive {
		if s.lock, err = lockFile(path + ".lock"); err != nil {
			return nil, err
		}
	}
	base := "file:" + path + "?_pragma=busy_timeout(10000)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	s.write, err = sql.Open("sqlite", base+"&_pragma=journal_mode(WAL)&_txlock=immediate")
	if err != nil {
		s.Close()
		return nil, err
	}
	s.write.SetMaxOpenConns(1)
	s.write.SetMaxIdleConns(1)
	s.write.SetConnMaxIdleTime(0)
	// La primera conexión deja el fichero en WAL antes de que lean las demás.
	if err := s.write.PingContext(ctx); err != nil {
		s.Close()
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	s.read, err = sql.Open("sqlite", base+"&_pragma=query_only(1)")
	if err != nil {
		s.Close()
		return nil, err
	}
	n := max(4, runtime.GOMAXPROCS(0))
	s.read.SetMaxOpenConns(n)
	s.read.SetMaxIdleConns(n)
	return s, nil
}

// Path es la ruta absoluta del fichero.
func (s *SQLite) Path() string { return s.path }

// WriteDB es la conexión de escritura como *sql.DB, para goose.
func (s *SQLite) WriteDB() *sql.DB { return s.write }

func (s *SQLite) Exec(ctx context.Context, q string, args ...any) (Result, error) {
	return execOn(ctx, s.write, q, args)
}

func (s *SQLite) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	return queryOn(ctx, s.pick(q), q, args)
}

func (s *SQLite) QueryRow(ctx context.Context, q string, args ...any) Row {
	return queryRowOn(ctx, s.pick(q), q, args)
}

// pick da la conexión de escritura salvo a las consultas que solo leen.
func (s *SQLite) pick(q string) *sql.DB {
	if readOnlySQL(q) {
		return s.read
	}
	return s.write
}

// SendBatch corre el lote en una transacción, como hace Postgres con un
// lote fuera de una.
func (s *SQLite) SendBatch(ctx context.Context, b *Batch) error {
	return BeginFunc(ctx, s, func(tx Tx) error { return tx.SendBatch(ctx, b) })
}

func (s *SQLite) Dialect() Dialect { return SQLiteDialect }

func (s *SQLite) Begin(ctx context.Context) (Tx, error) { return s.BeginTx(ctx, TxOptions{}) }

func (s *SQLite) BeginTx(ctx context.Context, opts TxOptions) (Tx, error) {
	conn := s.write
	if opts.ReadOnly {
		conn = s.read
	}
	tx, err := conn.BeginTx(ctx, &sql.TxOptions{ReadOnly: opts.ReadOnly})
	if err != nil {
		return nil, err
	}
	return &sqliteTx{db: s, tx: tx, st: &txState{}}, nil
}

func (s *SQLite) Ping(ctx context.Context) error {
	if err := s.write.PingContext(ctx); err != nil {
		return err
	}
	return s.read.PingContext(ctx)
}

func (s *SQLite) Close() {
	if s.read != nil {
		_ = s.read.Close()
	}
	if s.write != nil {
		_ = s.write.Close()
	}
	if s.lock != nil {
		_ = s.lock.Close()
	}
}

func (s *SQLite) Stats() Stats {
	var out Stats
	for _, d := range []*sql.DB{s.write, s.read} {
		st := d.Stats()
		out.InUse += int64(st.InUse)
		out.Idle += int64(st.Idle)
		out.Max += int64(st.MaxOpenConnections)
		out.Waits += st.WaitCount
		out.WaitTime += st.WaitDuration
	}
	return out
}

// sqlConn es lo que comparten *sql.DB y *sql.Tx.
type sqlConn interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func execOn(ctx context.Context, c sqlConn, q string, args []any) (Result, error) {
	a, err := sqliteArgs(args)
	if err != nil {
		return Result{}, err
	}
	res, err := c.ExecContext(ctx, q, a...)
	if err != nil {
		return Result{}, err
	}
	n, err := res.RowsAffected()
	return Result{rowsAffected: n}, err
}

func queryOn(ctx context.Context, c sqlConn, q string, args []any) (Rows, error) {
	a, err := sqliteArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := c.QueryContext(ctx, q, a...)
	if err != nil {
		return nil, err
	}
	return &sqliteRows{rows: rows}, nil
}

func queryRowOn(ctx context.Context, c sqlConn, q string, args []any) Row {
	a, err := sqliteArgs(args)
	if err != nil {
		return errRow{err}
	}
	return sqliteRow{c.QueryRowContext(ctx, q, a...)}
}

type errRow struct{ err error }

func (r errRow) Scan(...any) error { return r.err }

type sqliteRow struct{ row *sql.Row }

func (r sqliteRow) Scan(dest ...any) error {
	err := r.row.Scan(scanTargets(dest)...)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNoRows
	}
	return err
}

type sqliteRows struct {
	rows  *sql.Rows
	err   error
	onErr func(error) error
}

func (r *sqliteRows) Next() bool { return r.err == nil && r.rows.Next() }

func (r *sqliteRows) Scan(dest ...any) error {
	err := r.rows.Scan(scanTargets(dest)...)
	if err != nil && r.err == nil {
		r.err = err
	}
	return err
}

func (r *sqliteRows) Values() ([]any, error) {
	cols, err := r.rows.Columns()
	if err != nil {
		return nil, err
	}
	vals := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := r.Scan(ptrs...); err != nil {
		return nil, err
	}
	return vals, nil
}

func (r *sqliteRows) Err() error {
	if r.err != nil {
		return r.err
	}
	err := r.rows.Err()
	if err != nil && r.onErr != nil {
		_ = r.onErr(err)
	}
	return err
}

func (r *sqliteRows) Close() { _ = r.rows.Close() }

// sqliteTx es una transacción o, con sp, un savepoint dentro de una. Como en
// Postgres, un error de la base deja la transacción abortada: lo que venga
// después falla y Commit la deshace, hasta volver a un savepoint anterior.
type sqliteTx struct {
	db   *SQLite
	tx   *sql.Tx
	sp   string
	st   *txState
	done bool
}

type txState struct{ aborted error }

// errTxAborted es el error de lo que se pide a una transacción abortada.
type errTxAborted struct{ cause error }

func (e errTxAborted) Error() string {
	return "the transaction was aborted by an earlier error (" + e.cause.Error() + ")"
}

func (t *sqliteTx) check() error {
	if t.done {
		return ErrTxClosed
	}
	if t.st.aborted != nil {
		return errTxAborted{t.st.aborted}
	}
	return nil
}

// fail apunta que err, si es de la base, aborta la transacción.
func (t *sqliteTx) fail(err error) error {
	var se *sqlite.Error
	if t.st.aborted == nil && (errors.As(err, &se) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)) {
		t.st.aborted = err
	}
	return err
}

func (t *sqliteTx) Exec(ctx context.Context, q string, args ...any) (Result, error) {
	if err := t.check(); err != nil {
		return Result{}, err
	}
	res, err := execOn(ctx, t.tx, q, args)
	return res, t.fail(err)
}

func (t *sqliteTx) Query(ctx context.Context, q string, args ...any) (Rows, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	rows, err := queryOn(ctx, t.tx, q, args)
	if err != nil {
		return nil, t.fail(err)
	}
	rows.(*sqliteRows).onErr = t.fail
	return rows, nil
}

func (t *sqliteTx) QueryRow(ctx context.Context, q string, args ...any) Row {
	if err := t.check(); err != nil {
		return errRow{err}
	}
	return txRow{queryRowOn(ctx, t.tx, q, args), t}
}

type txRow struct {
	row Row
	tx  *sqliteTx
}

func (r txRow) Scan(dest ...any) error {
	err := r.row.Scan(dest...)
	if err != nil && !errors.Is(err, ErrNoRows) {
		return r.tx.fail(err)
	}
	return err
}

func (t *sqliteTx) SendBatch(ctx context.Context, b *Batch) error {
	for _, q := range b.Queued {
		switch {
		case q.OnQuery != nil:
			rows, err := t.Query(ctx, q.SQL, q.Args...)
			if err != nil {
				return err
			}
			err = q.OnQuery(rows)
			rows.Close()
			if err == nil {
				err = rows.Err()
			}
			if err != nil {
				return err
			}
		default:
			res, err := t.Exec(ctx, q.SQL, q.Args...)
			if err != nil {
				return err
			}
			if q.OnExec != nil {
				if err := q.OnExec(res); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func (t *sqliteTx) Dialect() Dialect { return SQLiteDialect }

// Begin abre un savepoint.
func (t *sqliteTx) Begin(ctx context.Context) (Tx, error) {
	if err := t.check(); err != nil {
		return nil, err
	}
	sp := "sp" + strconv.FormatInt(t.db.sp.Add(1), 10)
	if _, err := t.tx.ExecContext(ctx, "SAVEPOINT "+sp); err != nil {
		return nil, t.fail(err)
	}
	return &sqliteTx{db: t.db, tx: t.tx, sp: sp, st: t.st}, nil
}

func (t *sqliteTx) Commit(ctx context.Context) error {
	if t.done {
		return ErrTxClosed
	}
	if aborted := t.st.aborted; aborted != nil {
		if t.sp != "" {
			t.done = true
			return errTxAborted{aborted}
		}
		_ = t.Rollback(ctx)
		return fmt.Errorf("commit rolled back: %w", errTxAborted{aborted})
	}
	t.done = true
	if t.sp != "" {
		_, err := t.tx.ExecContext(ctx, "RELEASE "+t.sp)
		return t.fail(err)
	}
	return sqlTxErr(t.tx.Commit())
}

func (t *sqliteTx) Rollback(ctx context.Context) error {
	if t.done {
		return ErrTxClosed
	}
	t.done = true
	if t.sp != "" {
		// Volver al savepoint quita el error, como en Postgres.
		if _, err := t.tx.ExecContext(ctx, "ROLLBACK TO "+t.sp); err != nil {
			return sqlTxErr(err)
		}
		t.st.aborted = nil
		_, err := t.tx.ExecContext(ctx, "RELEASE "+t.sp)
		return sqlTxErr(err)
	}
	return sqlTxErr(t.tx.Rollback())
}

func sqlTxErr(err error) error {
	if errors.Is(err, sql.ErrTxDone) {
		return ErrTxClosed
	}
	return err
}

// readOnlySQL dice si q solo lee: empieza por SELECT o VALUES, o por WITH sin
// INSERT, UPDATE, DELETE ni REPLACE.
func readOnlySQL(q string) bool {
	words := sqlWords(q)
	if len(words) == 0 {
		return false
	}
	switch words[0] {
	case "SELECT", "VALUES":
		return true
	case "WITH":
		for _, w := range words {
			switch w {
			case "INSERT", "UPDATE", "DELETE", "REPLACE":
				return false
			}
		}
		return true
	}
	return false
}

// sqlWords son las palabras de q en mayúsculas, sin las de los literales y
// los comentarios.
func sqlWords(q string) []string {
	var out []string
	for i := 0; i < len(q); {
		c := q[i]
		switch {
		case c == '\'' || c == '"':
			j := i + 1
			for j < len(q) && q[j] != c {
				j++
			}
			i = j + 1
		case c == '-' && i+1 < len(q) && q[i+1] == '-':
			for i < len(q) && q[i] != '\n' {
				i++
			}
		case isWordByte(c):
			j := i
			for j < len(q) && isWordByte(q[j]) {
				j++
			}
			out = append(out, strings.ToUpper(q[i:j]))
			i = j
		default:
			i++
		}
	}
	return out
}

func isWordByte(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

// sqliteCode es el código extendido de un error de SQLite, o 0.
func sqliteCode(err error) int {
	var se *sqlite.Error
	if errors.As(err, &se) {
		return se.Code()
	}
	return 0
}

func isSQLiteUnique(err error) bool {
	c := sqliteCode(err)
	return c == sqlite3.SQLITE_CONSTRAINT_UNIQUE || c == sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY
}

func isSQLiteForeignKey(err error) bool {
	return sqliteCode(err) == sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY
}
