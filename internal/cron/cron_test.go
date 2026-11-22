package cron

import (
	"encoding/json"
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := LoadLocation(name)
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

func TestNextCrontab(t *testing.T) {
	madrid := mustLoc(t, "Europe/Madrid")
	cases := []struct {
		expr string
		loc  *time.Location
		from string
		want string
	}{
		{"* * * * *", time.UTC, "2026-11-22T10:00:30Z", "2026-11-22T10:01:00Z"},
		{"*/15 * * * *", time.UTC, "2026-11-22T10:15:00Z", "2026-11-22T10:30:00Z"},
		{"0 3 * * *", time.UTC, "2026-11-22T10:00:00Z", "2026-11-23T03:00:00Z"},
		// Las 3:00 de Madrid en invierno son las 2:00 UTC.
		{"0 3 * * *", madrid, "2026-11-22T10:00:00Z", "2026-11-23T02:00:00Z"},
		{"30 9 * * mon-fri", time.UTC, "2026-11-20T10:00:00Z", "2026-11-23T09:30:00Z"},
		{"0 0 1 jan *", time.UTC, "2026-11-22T10:00:00Z", "2027-01-01T00:00:00Z"},
		{"0 12 * * 7", time.UTC, "2026-11-22T13:00:00Z", "2026-11-29T12:00:00Z"},
		// Con el día del mes y el de la semana restringidos basta uno de los dos.
		{"0 0 1 * fri", time.UTC, "2026-11-22T00:00:00Z", "2026-11-27T00:00:00Z"},
		{"@hourly", time.UTC, "2026-11-22T10:59:59Z", "2026-11-22T11:00:00Z"},
		{"0 0 29 2 *", time.UTC, "2026-03-01T00:00:00Z", "2028-02-29T00:00:00Z"},
		// El 29 de marzo de 2026 Madrid salta de 2:00 a 3:00: las 2:30 no existen.
		{"30 * * * *", madrid, "2026-03-29T00:45:00Z", "2026-03-29T01:30:00Z"},
		// El 25 de octubre de 2026 las 2:00-3:00 de Madrid pasan dos veces.
		{"15 * * * *", madrid, "2026-10-25T00:20:00Z", "2026-10-25T01:15:00Z"},
		{"15 * * * *", madrid, "2026-10-25T01:20:00Z", "2026-10-25T02:15:00Z"},
	}
	for _, c := range cases {
		from, _ := time.Parse(time.RFC3339, c.from)
		want, _ := time.Parse(time.RFC3339, c.want)
		s := Schedule{Type: TypeCrontab, Crontab: c.expr}
		if err := s.Normalize(); err != nil {
			t.Fatalf("%s: %v", c.expr, err)
		}
		if got := s.Next(from, c.loc); !got.Equal(want) {
			t.Errorf("%s en %s desde %s: %s; quería %s", c.expr, c.loc, c.from, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

func TestNextInterval(t *testing.T) {
	from, _ := time.Parse(time.RFC3339, "2026-01-31T10:20:45Z")
	cases := []struct {
		every int
		unit  string
		want  string
	}{
		{5, "minute", "2026-01-31T10:25:00Z"},
		{2, "hours", "2026-01-31T12:20:00Z"},
		{1, "day", "2026-02-01T10:20:00Z"},
		{1, "week", "2026-02-07T10:20:00Z"},
		{1, "month", "2026-03-03T10:20:00Z"},
		{1, "year", "2027-01-31T10:20:00Z"},
	}
	for _, c := range cases {
		s := Schedule{Type: TypeInterval, Every: c.every, Unit: c.unit}
		if err := s.Normalize(); err != nil {
			t.Fatal(err)
		}
		want, _ := time.Parse(time.RFC3339, c.want)
		if got := s.Next(from, time.UTC); !got.Equal(want) {
			t.Errorf("cada %d %s: %s; quería %s", c.every, c.unit, got.UTC().Format(time.RFC3339), c.want)
		}
	}
}

func TestNormalizeRejects(t *testing.T) {
	for _, s := range []Schedule{
		{Type: TypeCrontab, Crontab: "* * * *"},
		{Type: TypeCrontab, Crontab: "60 * * * *"},
		{Type: TypeCrontab, Crontab: "* * * * mon-xyz"},
		{Type: TypeCrontab, Crontab: "*/0 * * * *"},
		{Type: TypeCrontab, Crontab: "5-1 * * * *"},
		{Type: TypeInterval, Every: 0, Unit: "minute"},
		{Type: TypeInterval, Every: 1, Unit: "fortnight"},
		{Type: "rrule"},
	} {
		if err := s.Normalize(); err == nil {
			t.Errorf("%+v: quería un error", s)
		}
	}
}

func TestScheduleJSON(t *testing.T) {
	for _, in := range []string{
		`{"type":"crontab","value":"0 3 * * *"}`,
		`{"type":"interval","value":2,"unit":"hour"}`,
	} {
		var s Schedule
		if err := json.Unmarshal([]byte(in), &s); err != nil {
			t.Fatal(err)
		}
		out, _ := json.Marshal(s)
		if string(out) != in {
			t.Errorf("%s: vuelve como %s", in, out)
		}
	}
	var s Schedule
	if err := json.Unmarshal([]byte(`{"type":"interval","value":"10","unit":"minute"}`), &s); err != nil || s.Every != 10 {
		t.Fatalf("intervalo como texto: %+v %v", s, err)
	}
}
