// Package sqlite tiene las migraciones de SQLite, aparte de las de Postgres.
package sqlite

import "embed"

//go:embed *.sql
var FS embed.FS
