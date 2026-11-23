package db

import (
	"fmt"
	"strings"
)

// Dialect da los trozos de SQL que no se escriben igual en todos los motores.
// Lo que vale en todos va escrito tal cual en cada consulta. docs/sqlite.md
// dice qué queda propio de Postgres y cómo será con SQLite.
type Dialect interface {
	Name() string
	// ForUpdate bloquea las filas leídas, de las tablas of o de todas, hasta
	// que acabe la transacción. Va al final del SELECT, con su espacio delante.
	ForUpdate(of ...string) string
	// ForUpdateSkipLocked es ForUpdate saltándose las filas que ya tiene
	// bloqueadas otra transacción.
	ForUpdateSkipLocked(of ...string) string
	// InList es expr comparado con cada elemento de la lista que va en el
	// parámetro param, un slice de Go.
	InList(expr, param string) string
	// InBytes es InList con una lista de []byte.
	InBytes(expr, param string) string
	// Unnest da como tabla alias las listas de los parámetros $first,
	// $first+1…, una por columna; cols son «nombre tipo», con el tipo de un
	// elemento. Las listas van juntas por posición.
	Unnest(alias string, first int, cols ...string) string
	// UnnestNumbered es Unnest con una columna más, num, con la posición de
	// cada fila desde 1.
	UnnestNumbered(alias, num string, first int, cols ...string) string
	// Greatest y Least son el mayor y el menor de los valores que no son NULL.
	Greatest(args ...string) string
	Least(args ...string) string
	// ILike es expr LIKE pattern sin distinguir mayúsculas.
	ILike(expr, pattern string) string
	// SecondsBetween son los segundos, con decimales, de from a to.
	SecondsBetween(to, from string) string
	// AddSeconds y AddMinutes suman n a la fecha ts; SubSeconds la resta.
	AddSeconds(ts, n string) string
	SubSeconds(ts, n string) string
	AddMinutes(ts, n string) string
	// JSONText es el valor de la clave key del objeto JSON doc como texto;
	// JSONType, su tipo («object», «array», «string»…).
	JSONText(doc, key string) string
	JSONType(doc, key string) string
	// Collate es el texto expr de un ORDER BY, ordenado como lo ordena
	// Postgres con la colación en_US.UTF-8.
	Collate(expr string) string
	// Typed es expr, un parámetro, con el tipo typ de Postgres: Postgres lo
	// necesita para saber el tipo del parámetro; SQLite no.
	Typed(expr, typ string) string
}

// PostgresDialect es el dialecto de Postgres 16.
var PostgresDialect Dialect = postgresDialect{}

type postgresDialect struct{}

func (postgresDialect) Name() string { return "postgres" }

func (postgresDialect) ForUpdate(of ...string) string {
	if len(of) == 0 {
		return " FOR UPDATE"
	}
	return " FOR UPDATE OF " + strings.Join(of, ", ")
}

func (d postgresDialect) ForUpdateSkipLocked(of ...string) string {
	return d.ForUpdate(of...) + " SKIP LOCKED"
}

func (postgresDialect) InList(expr, param string) string {
	return expr + " = ANY(" + param + ")"
}

func (d postgresDialect) InBytes(expr, param string) string { return d.InList(expr, param) }

func (postgresDialect) Unnest(alias string, first int, cols ...string) string {
	params, names := unnestCols(first, cols)
	return "unnest(" + strings.Join(params, ", ") + ") AS " + alias + "(" + strings.Join(names, ", ") + ")"
}

func (postgresDialect) UnnestNumbered(alias, num string, first int, cols ...string) string {
	params, names := unnestCols(first, cols)
	return "unnest(" + strings.Join(params, ", ") + ") WITH ORDINALITY AS " + alias +
		"(" + strings.Join(append(names, num), ", ") + ")"
}

func unnestCols(first int, cols []string) (params, names []string) {
	for i, c := range cols {
		name, typ, ok := strings.Cut(c, " ")
		if !ok {
			panic("db: columna de Unnest sin tipo: " + c)
		}
		names = append(names, name)
		params = append(params, fmt.Sprintf("$%d::%s[]", first+i, typ))
	}
	return params, names
}

func (postgresDialect) Greatest(args ...string) string {
	return "GREATEST(" + strings.Join(args, ", ") + ")"
}

func (postgresDialect) Least(args ...string) string { return "LEAST(" + strings.Join(args, ", ") + ")" }

func (postgresDialect) ILike(expr, pattern string) string { return expr + " ILIKE " + pattern }

func (postgresDialect) SecondsBetween(to, from string) string {
	return "EXTRACT(EPOCH FROM " + to + " - " + from + ")"
}

func (postgresDialect) AddSeconds(ts, n string) string {
	return ts + " + make_interval(secs => " + n + ")"
}

func (postgresDialect) SubSeconds(ts, n string) string {
	return ts + " - make_interval(secs => " + n + ")"
}

func (postgresDialect) AddMinutes(ts, n string) string {
	return ts + " + make_interval(mins => " + n + ")"
}

func (postgresDialect) JSONText(doc, key string) string { return doc + "->>'" + key + "'" }

func (postgresDialect) JSONType(doc, key string) string {
	return "jsonb_typeof(" + doc + "->'" + key + "')"
}

func (postgresDialect) Collate(expr string) string { return expr }

func (postgresDialect) Typed(expr, typ string) string { return expr + "::" + typ }

// ByEngine da la consulta de Postgres o la de SQLite según el motor de d,
// para las que no se escriben igual más allá de los trozos de Dialect.
func ByEngine(d Dialect, postgres, sqlite string) string {
	if d.Name() == "sqlite" {
		return sqlite
	}
	return postgres
}
