package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func madridCfg(freq string) DigestConfig {
	c := DefaultDigest(1)
	c.Frequency, c.Timezone, c.Hour = freq, "Europe/Madrid", 9
	return c
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestDigestDailyPeriods(t *testing.T) {
	c := madridCfg(DigestDaily)
	for _, tc := range []struct{ now, end, start, next string }{
		// En invierno, las 9 de Madrid son las 8 UTC.
		{"2026-11-22T08:00:00Z", "2026-11-22T08:00:00Z", "2026-11-21T08:00:00Z", "2026-11-23T08:00:00Z"},
		{"2026-11-22T07:59:59Z", "2026-11-21T08:00:00Z", "2026-11-20T08:00:00Z", "2026-11-22T08:00:00Z"},
		// En verano, las 7 UTC.
		{"2026-07-01T12:00:00Z", "2026-07-01T07:00:00Z", "2026-06-30T07:00:00Z", "2026-07-02T07:00:00Z"},
		// El cambio de hora de primavera (29 de marzo): el día dura 23 horas.
		{"2026-03-29T07:30:00Z", "2026-03-29T07:00:00Z", "2026-03-28T08:00:00Z", "2026-03-30T07:00:00Z"},
		// El de otoño (25 de octubre): 25 horas.
		{"2026-10-25T09:00:00Z", "2026-10-25T08:00:00Z", "2026-10-24T07:00:00Z", "2026-10-26T08:00:00Z"},
	} {
		now := mustParse(t, tc.now)
		end := c.PeriodEnd(now)
		if want := mustParse(t, tc.end); !end.Equal(want) {
			t.Errorf("%s: fin = %s, quiero %s", tc.now, end.UTC(), want)
		}
		if start, want := c.PeriodStart(end), mustParse(t, tc.start); !start.Equal(want) {
			t.Errorf("%s: principio = %s, quiero %s", tc.now, start.UTC(), want)
		}
		if next, want := c.NextSend(now), mustParse(t, tc.next); !next.Equal(want) {
			t.Errorf("%s: siguiente = %s, quiero %s", tc.now, next.UTC(), want)
		}
	}
}

func TestDigestEachLocalDayOnce(t *testing.T) {
	// Hora a hora durante un año, cada periodo cerrado empieza donde acabó el
	// anterior y acaba a las 9 de Madrid, con el cambio de hora en medio.
	c := madridCfg(DigestDaily)
	loc := c.Location()
	var last time.Time
	ends := 0
	for now := mustParse(t, "2026-01-01T00:30:00Z"); now.Year() == 2026; now = now.Add(time.Hour) {
		end := c.PeriodEnd(now)
		if end.After(now) || end.In(loc).Hour() != 9 || end.In(loc).Minute() != 0 {
			t.Fatalf("%s: fin %s", now, end.In(loc))
		}
		if end.Equal(last) {
			continue
		}
		if !last.IsZero() && !c.PeriodStart(end).Equal(last) {
			t.Fatalf("%s: el periodo empieza en %s y el anterior acabó en %s", now, c.PeriodStart(end), last)
		}
		last = end
		ends++
	}
	// El que acaba el 31 de diciembre de 2025 y los 365 de 2026.
	if ends != 366 {
		t.Errorf("%d periodos en 2026", ends)
	}
}

func TestDigestWeekly(t *testing.T) {
	c := madridCfg(DigestWeekly)
	c.Weekday = 1 // lunes
	for _, tc := range []struct{ now, end, start, next string }{
		// Domingo 22 de noviembre: el último lunes a las 9 fue el 16.
		{"2026-11-22T12:00:00Z", "2026-11-16T08:00:00Z", "2026-11-09T08:00:00Z", "2026-11-23T08:00:00Z"},
		// El mismo lunes 23 antes de las 9, aún no.
		{"2026-11-23T07:00:00Z", "2026-11-16T08:00:00Z", "2026-11-09T08:00:00Z", "2026-11-23T08:00:00Z"},
		{"2026-11-23T08:00:00Z", "2026-11-23T08:00:00Z", "2026-11-16T08:00:00Z", "2026-11-30T08:00:00Z"},
		// La semana del cambio de hora de otoño dura 7 días y una hora.
		{"2026-10-26T08:00:00Z", "2026-10-26T08:00:00Z", "2026-10-19T07:00:00Z", "2026-11-02T08:00:00Z"},
	} {
		now := mustParse(t, tc.now)
		end := c.PeriodEnd(now)
		if want := mustParse(t, tc.end); !end.Equal(want) {
			t.Errorf("%s: fin = %s, quiero %s", tc.now, end.UTC(), want)
		}
		if start, want := c.PeriodStart(end), mustParse(t, tc.start); !start.Equal(want) {
			t.Errorf("%s: principio = %s, quiero %s", tc.now, start.UTC(), want)
		}
		if next, want := c.NextSend(now), mustParse(t, tc.next); !next.Equal(want) {
			t.Errorf("%s: siguiente = %s, quiero %s", tc.now, next.UTC(), want)
		}
	}
}

func TestDigestNormalize(t *testing.T) {
	ok := func() DigestConfig {
		c := DefaultDigest(1)
		c.Enabled = true
		c.Destinations = []RuleAction{{Type: ActionWebhook, URL: "https://agente.example.com/resumen"}}
		return c
	}
	c := ok()
	gen, err := c.Normalize()
	if err != nil || len(gen) != 1 || len(c.Destinations[0].Secret) != 64 || c.Destinations[0].ID == "" {
		t.Fatalf("webhook sin secreto: %v %v %+v", gen, err, c.Destinations)
	}
	for name, change := range map[string]func(*DigestConfig){
		"frecuencia":  func(c *DigestConfig) { c.Frequency = "monthly" },
		"día":         func(c *DigestConfig) { c.Weekday = 7 },
		"hora":        func(c *DigestConfig) { c.Hour = 24 },
		"zona":        func(c *DigestConfig) { c.Timezone = "Europa/Madriz" },
		"idioma":      func(c *DigestConfig) { c.Language = "fr" },
		"sin destino": func(c *DigestConfig) { c.Destinations = nil },
		"linear":      func(c *DigestConfig) { c.Destinations = []RuleAction{{Type: ActionLinear}} },
		"correo":      func(c *DigestConfig) { c.Destinations = []RuleAction{{Type: ActionEmail, To: []string{"no es"}}} },
		"tapado": func(c *DigestConfig) {
			c.Destinations = []RuleAction{{Type: ActionSlack, URL: "https://hooks.slack.com/…abcd"}}
		},
	} {
		c := ok()
		change(&c)
		if _, err := c.Normalize(); !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	off := DefaultDigest(1)
	if _, err := off.Normalize(); err != nil {
		t.Errorf("apagado y sin destinos vale: %v", err)
	}
}

// El resumen de un proyecto que no lo ha configurado sale en DEFAULT_LANGUAGE;
// el que ya tiene idioma guardado lo conserva.
func TestDigestDefaultLanguage(t *testing.T) {
	ctx := context.Background()
	s := testStore(t)
	a, err := s.CreateProject(ctx, "a", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateProject(ctx, "b", randomHex(t, 16))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if c, err := s.Digest(ctx, a.ID, now); err != nil || c.Language != "en" {
		t.Fatalf("sin DEFAULT_LANGUAGE = %q, %v", c.Language, err)
	}
	saved := DefaultDigest(b.ID)
	saved.Language = "en"
	if _, _, err := s.SetDigest(ctx, b.ID, saved, now); err != nil {
		t.Fatal(err)
	}
	s.SetDefaultLanguage("es")
	if c, err := s.Digest(ctx, a.ID, now); err != nil || c.Language != "es" {
		t.Errorf("con DEFAULT_LANGUAGE=es, el de un proyecto sin configurar = %q, %v", c.Language, err)
	}
	if c, err := s.Digest(ctx, b.ID, now); err != nil || c.Language != "en" {
		t.Errorf("con DEFAULT_LANGUAGE=es, el que ya era en = %q, %v", c.Language, err)
	}
}
