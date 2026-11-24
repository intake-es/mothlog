// Package cron calcula cuándo toca la siguiente ejecución de un horario: un
// crontab de cinco campos en una zona horaria, o cada N minutos, horas, días,
// semanas, meses o años.
package cron

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	TypeCrontab  = "crontab"
	TypeInterval = "interval"
)

// Units son las unidades de un intervalo, como las de los SDK.
var Units = []string{"minute", "hour", "day", "week", "month", "year"}

// MaxInterval es el mayor número de unidades de un intervalo.
const MaxInterval = 10_000

// Schedule es el horario como lo mandan los SDK en monitor_config:
// {"type": "crontab", "value": "0 3 * * *"} o {"type": "interval", "value": 2, "unit": "hour"}.
type Schedule struct {
	Type    string
	Crontab string
	Every   int
	Unit    string
}

type scheduleJSON struct {
	Type  string          `json:"type"`
	Value json.RawMessage `json:"value"`
	Unit  string          `json:"unit,omitempty"`
}

func (s Schedule) MarshalJSON() ([]byte, error) {
	out := scheduleJSON{Type: s.Type, Unit: s.Unit}
	var err error
	if s.Type == TypeInterval {
		out.Value, err = json.Marshal(s.Every)
	} else {
		out.Value, err = json.Marshal(s.Crontab)
	}
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (s *Schedule) UnmarshalJSON(b []byte) error {
	var in scheduleJSON
	if err := json.Unmarshal(b, &in); err != nil {
		return err
	}
	*s = Schedule{Type: in.Type, Unit: in.Unit}
	if len(in.Value) == 0 || string(in.Value) == "null" {
		return nil
	}
	if in.Type == TypeInterval {
		// Algunos SDK mandan el número como texto.
		var f float64
		if err := json.Unmarshal(in.Value, &f); err != nil {
			var str string
			if json.Unmarshal(in.Value, &str) != nil {
				return errors.New("schedule.value of an interval must be a number")
			}
			if f, err = strconv.ParseFloat(strings.TrimSpace(str), 64); err != nil {
				return errors.New("schedule.value of an interval must be a number")
			}
		}
		if f != float64(int(f)) {
			return errors.New("schedule.value of an interval must be an integer")
		}
		s.Every = int(f)
		return nil
	}
	return json.Unmarshal(in.Value, &s.Crontab)
}

// Normalize limpia el horario y comprueba que es válido.
func (s *Schedule) Normalize() error {
	s.Type = strings.TrimSpace(s.Type)
	switch s.Type {
	case TypeCrontab:
		s.Crontab = strings.Join(strings.Fields(s.Crontab), " ")
		s.Every, s.Unit = 0, ""
		_, err := parseCrontab(s.Crontab)
		return err
	case TypeInterval:
		s.Crontab = ""
		s.Unit = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s.Unit)), "s")
		if s.Every < 1 || s.Every > MaxInterval {
			return fmt.Errorf("the interval goes from 1 to %d", MaxInterval)
		}
		for _, u := range Units {
			if s.Unit == u {
				return nil
			}
		}
		return fmt.Errorf("unit %q does not exist; they are %s", s.Unit, strings.Join(Units, ", "))
	}
	return fmt.Errorf("schedule type %q does not exist; they are crontab and interval", s.Type)
}

// String es el horario para leerlo: «0 3 * * *» o «every 2 hour».
func (s Schedule) String() string {
	if s.Type == TypeInterval {
		return fmt.Sprintf("every %d %s", s.Every, s.Unit)
	}
	return s.Crontab
}

// Next es la primera ejecución estrictamente posterior a t, al minuto, en la
// zona horaria loc. Un horario no válido o sin ejecuciones en cinco años da el
// tiempo cero.
func (s Schedule) Next(t time.Time, loc *time.Location) time.Time {
	if loc == nil {
		loc = time.UTC
	}
	t = t.In(loc)
	if s.Type == TypeInterval {
		t = t.Truncate(time.Minute)
		switch s.Unit {
		case "minute":
			return t.Add(time.Duration(s.Every) * time.Minute)
		case "hour":
			return t.Add(time.Duration(s.Every) * time.Hour)
		case "day":
			return t.AddDate(0, 0, s.Every)
		case "week":
			return t.AddDate(0, 0, 7*s.Every)
		case "month":
			return t.AddDate(0, s.Every, 0)
		case "year":
			return t.AddDate(s.Every, 0, 0)
		}
		return time.Time{}
	}
	c, err := parseCrontab(s.Crontab)
	if err != nil {
		return time.Time{}
	}
	return c.next(t, loc)
}

// LoadLocation es time.LoadLocation con «» como UTC.
func LoadLocation(name string) (*time.Location, error) {
	if strings.TrimSpace(name) == "" {
		return time.UTC, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("time zone %q does not exist", name)
	}
	return loc, nil
}

type crontab struct {
	minute, hour, dom, month, dow uint64
	// Con los dos días restringidos basta con que se cumpla uno, como en cron.
	domStar, dowStar bool
}

var macros = map[string]string{
	"@yearly": "0 0 1 1 *", "@annually": "0 0 1 1 *", "@monthly": "0 0 1 * *",
	"@weekly": "0 0 * * 0", "@daily": "0 0 * * *", "@midnight": "0 0 * * *", "@hourly": "0 * * * *",
}

var monthNames = map[string]int{"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "oct": 10, "nov": 11, "dec": 12}

var dowNames = map[string]int{"sun": 0, "mon": 1, "tue": 2, "wed": 3, "thu": 4, "fri": 5, "sat": 6}

func parseCrontab(expr string) (*crontab, error) {
	expr = strings.TrimSpace(expr)
	if m, ok := macros[strings.ToLower(expr)]; ok {
		expr = m
	}
	f := strings.Fields(expr)
	if len(f) != 5 {
		return nil, fmt.Errorf("crontab %q must have five fields: minute, hour, day, month and day of week", expr)
	}
	var c crontab
	var err error
	if c.minute, err = parseField(f[0], 0, 59, nil); err != nil {
		return nil, fmt.Errorf("minute: %w", err)
	}
	if c.hour, err = parseField(f[1], 0, 23, nil); err != nil {
		return nil, fmt.Errorf("hour: %w", err)
	}
	if c.dom, err = parseField(f[2], 1, 31, nil); err != nil {
		return nil, fmt.Errorf("day of month: %w", err)
	}
	if c.month, err = parseField(f[3], 1, 12, monthNames); err != nil {
		return nil, fmt.Errorf("month: %w", err)
	}
	if c.dow, err = parseField(f[4], 0, 7, dowNames); err != nil {
		return nil, fmt.Errorf("day of week: %w", err)
	}
	// El 7 también es domingo.
	if c.dow&(1<<7) != 0 {
		c.dow |= 1
	}
	c.domStar = strings.HasPrefix(f[2], "*")
	c.dowStar = strings.HasPrefix(f[4], "*")
	return &c, nil
}

func parseField(s string, lo, hi int, names map[string]int) (uint64, error) {
	var bits uint64
	for part := range strings.SplitSeq(s, ",") {
		rng, stepS, hasStep := strings.Cut(part, "/")
		step := 1
		if hasStep {
			n, err := strconv.Atoi(stepS)
			if err != nil || n < 1 || n > hi-lo+1 {
				return 0, fmt.Errorf("invalid step in %q", part)
			}
			step = n
		}
		var from, to int
		switch {
		case rng == "*":
			from, to = lo, hi
		case strings.Contains(rng, "-"):
			a, b, _ := strings.Cut(rng, "-")
			var err error
			if from, err = value(a, names); err != nil {
				return 0, err
			}
			if to, err = value(b, names); err != nil {
				return 0, err
			}
		default:
			v, err := value(rng, names)
			if err != nil {
				return 0, err
			}
			from, to = v, v
			if hasStep {
				to = hi
			}
		}
		if from < lo || to > hi || from > to {
			return 0, fmt.Errorf("%q is outside %d-%d", part, lo, hi)
		}
		for v := from; v <= to; v += step {
			bits |= 1 << v
		}
	}
	return bits, nil
}

func value(s string, names map[string]int) (int, error) {
	if v, ok := names[strings.ToLower(s)]; ok {
		return v, nil
	}
	v, err := strconv.Atoi(s)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", s)
	}
	return v, nil
}

func (c *crontab) dayMatches(t time.Time) bool {
	dom := c.dom&(1<<t.Day()) != 0
	dow := c.dow&(1<<t.Weekday()) != 0
	if c.domStar || c.dowStar {
		return dom && dow
	}
	return dom || dow
}

func (c *crontab) next(t time.Time, loc *time.Location) time.Time {
	t = t.Truncate(time.Minute).Add(time.Minute)
	limit := t.AddDate(5, 0, 0)
	for t.Before(limit) {
		y, m, d := t.Date()
		switch {
		case c.month&(1<<m) == 0:
			t = time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
		case !c.dayMatches(t):
			t = time.Date(y, m, d+1, 0, 0, 0, 0, loc)
		case c.hour&(1<<t.Hour()) == 0:
			n := time.Date(y, m, d, t.Hour()+1, 0, 0, 0, loc)
			if !n.After(t) {
				// En la hora que se repite al cambiar al horario de invierno.
				n = t.Truncate(time.Hour).Add(time.Hour)
			}
			t = n
		case c.minute&(1<<t.Minute()) == 0:
			t = t.Add(time.Minute)
		default:
			return t
		}
	}
	return time.Time{}
}
