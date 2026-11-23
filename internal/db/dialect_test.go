package db

import "testing"

// El dialecto de Postgres escribe lo mismo que había escrito a mano en cada consulta.
func TestPostgresDialect(t *testing.T) {
	d := PostgresDialect
	for _, c := range []struct{ got, want string }{
		{d.ForUpdate(), " FOR UPDATE"},
		{d.ForUpdate("c", "m"), " FOR UPDATE OF c, m"},
		{d.ForUpdateSkipLocked(), " FOR UPDATE SKIP LOCKED"},
		{d.ForUpdateSkipLocked("m"), " FOR UPDATE OF m SKIP LOCKED"},
		{d.InList("o.channel", "$2"), "o.channel = ANY($2)"},
		{d.Unnest("k", 1, "project_id bigint", "event_id text"), "unnest($1::bigint[], $2::text[]) AS k(project_id, event_id)"},
		{d.Unnest("h", 6, "issue_id bigint", "user_hash bytea"), "unnest($6::bigint[], $7::bytea[]) AS h(issue_id, user_hash)"},
		{d.UnnestNumbered("e", "n", 1, "id bigint", "at timestamptz"), "unnest($1::bigint[], $2::timestamptz[]) WITH ORDINALITY AS e(id, at, n)"},
		{d.Greatest("a", "$2"), "GREATEST(a, $2)"},
		{d.Least("a", "b"), "LEAST(a, b)"},
		{d.ILike("t.value", "$3"), "t.value ILIKE $3"},
		{d.SecondsBetween("now()", "min(created_at)"), "EXTRACT(EPOCH FROM now() - min(created_at))"},
		{d.AddSeconds("$1", "m.interval_seconds"), "$1 + make_interval(secs => m.interval_seconds)"},
		{d.SubSeconds("now()", "$3"), "now() - make_interval(secs => $3)"},
		{d.AddMinutes("m.next_checkin_at", "m.checkin_margin"), "m.next_checkin_at + make_interval(mins => m.checkin_margin)"},
		{d.JSONText("conditions", "new"), "conditions->>'new'"},
		{d.JSONType("conditions", "users"), "jsonb_typeof(conditions->'users')"},
	} {
		if c.got != c.want {
			t.Errorf("%q; quiero %q", c.got, c.want)
		}
	}
}
