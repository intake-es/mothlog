package db

import (
	"fmt"
	"strings"
)

// SQLiteDialect es el dialecto de SQLite. Las fechas son enteros de
// microsegundos desde 1970 y las listas de los parámetros, texto JSON.
var SQLiteDialect Dialect = sqliteDialect{}

type sqliteDialect struct{}

func (sqliteDialect) Name() string { return "sqlite" }

// Con BEGIN IMMEDIATE la transacción ya tiene el cerrojo de escritura, y hay
// una sola instancia: no hay filas que bloquear ni de las que saltarse.
func (sqliteDialect) ForUpdate(...string) string { return "" }

func (sqliteDialect) ForUpdateSkipLocked(...string) string { return "" }

func (sqliteDialect) InList(expr, param string) string {
	return expr + " IN (SELECT value FROM json_each(" + param + "))"
}

func (sqliteDialect) InBytes(expr, param string) string {
	return expr + " IN (SELECT unhex(value) FROM json_each(" + param + "))"
}

func (d sqliteDialect) Unnest(alias string, first int, cols ...string) string {
	return d.unnest(alias, "", first, cols)
}

func (d sqliteDialect) UnnestNumbered(alias, num string, first int, cols ...string) string {
	return d.unnest(alias, num, first, cols)
}

// unnest recorre la primera lista con json_each y toma de las demás el
// elemento de la misma posición.
func (sqliteDialect) unnest(alias, num string, first int, cols []string) string {
	var sel []string
	for i, c := range cols {
		name, typ, ok := strings.Cut(c, " ")
		if !ok {
			panic("db: columna de Unnest sin tipo: " + c)
		}
		v := "j.value"
		if i > 0 {
			v = fmt.Sprintf("($%d ->> j.key)", first+i)
		}
		if typ == "bytea" {
			v = "unhex(" + v + ")"
		}
		sel = append(sel, v+" AS "+name)
	}
	if num != "" {
		sel = append(sel, "j.key + 1 AS "+num)
	}
	return fmt.Sprintf("(SELECT %s FROM json_each($%d) j) AS %s", strings.Join(sel, ", "), first, alias)
}

// nullSafe da cada argumento con COALESCE de los demás: max y min de SQLite
// dan NULL si alguno lo es, y GREATEST y LEAST de Postgres los ignoran.
func nullSafe(args []string) string {
	if len(args) < 2 {
		return strings.Join(args, ", ")
	}
	out := make([]string, len(args))
	for i, a := range args {
		rest := []string{a}
		for j, b := range args {
			if j != i {
				rest = append(rest, b)
			}
		}
		out[i] = "COALESCE(" + strings.Join(rest, ", ") + ")"
	}
	return strings.Join(out, ", ")
}

func (sqliteDialect) Greatest(args ...string) string { return "max(" + nullSafe(args) + ")" }

func (sqliteDialect) Least(args ...string) string { return "min(" + nullSafe(args) + ")" }

// ILike usa el lower con Unicode que se registra en el driver.
func (sqliteDialect) ILike(expr, pattern string) string {
	return "lower(" + expr + ") LIKE lower(" + pattern + `) ESCAPE '\'`
}

func (sqliteDialect) SecondsBetween(to, from string) string {
	return "(CAST((" + to + ") - (" + from + ") AS REAL) / 1000000)"
}

func (sqliteDialect) AddSeconds(ts, n string) string {
	return "((" + ts + ") + CAST(round((" + n + ") * 1000000) AS INTEGER))"
}

func (sqliteDialect) SubSeconds(ts, n string) string {
	return "((" + ts + ") - CAST(round((" + n + ") * 1000000) AS INTEGER))"
}

func (sqliteDialect) AddMinutes(ts, n string) string {
	return "((" + ts + ") + CAST(round((" + n + ") * 60000000) AS INTEGER))"
}

func (sqliteDialect) JSONText(doc, key string) string {
	return "json_extract(" + doc + ", '$." + key + "')"
}

func (sqliteDialect) JSONType(doc, key string) string {
	return "json_type(" + doc + ", '$." + key + "')"
}

// Collate usa la colación en_us que se registra en el driver.
func (sqliteDialect) Collate(expr string) string { return expr + " COLLATE en_us" }

func (sqliteDialect) Typed(expr, _ string) string { return expr }
