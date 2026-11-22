package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// fixtureItems lee un envío grabado de testdata/envelopes.
func fixtureItems(t *testing.T, name string) []Item {
	t.Helper()
	dir := "../../testdata/envelopes"
	body, err := os.ReadFile(filepath.Join(dir, name+".envelope"))
	if err != nil {
		t.Fatal(err)
	}
	var meta struct {
		Headers map[string]string `json:"headers"`
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".meta.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &meta); err != nil {
		t.Fatal(err)
	}
	data, err := Decompress(bytes.NewReader(body), meta.Headers["Content-Encoding"], 10<<20)
	if err != nil {
		t.Fatal(err)
	}
	env, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	return env.Sessions()
}

func TestParseSessionFixtures(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		sid, did, status     string
		init                 bool
		errors               int64
		started              string
		flags                SessionFlags
		release, environment string
	}{
		{"web-sesion-1", "44c6683272b248fbbc99f78bf5bf9d2e", "", "ok", true, 0, "2026-11-19T17:06:57.513Z",
			SessionFlags{}, "fixture-release-1", "production"},
		{"web-sesion-2", "44c6683272b248fbbc99f78bf5bf9d2e", "", "unhandled", false, 1, "2026-11-19T17:06:57.513Z",
			SessionFlags{Errored: true, Crashed: true}, "fixture-release-1", "production"},
		{"api-sesion-ok-1", "89e4f5f4f3d640ae9b57225db9308e05", "u_ana", "exited", true, 0, "2026-11-22T15:37:49.620002Z",
			SessionFlags{}, "fixture-release-1", "production"},
		{"api-sesion-crash-2", "85a03e7eefaf4b968a30cd9fda0d81ac", "u_luis", "crashed", true, 1, "2026-11-22T15:37:49.872976Z",
			SessionFlags{Errored: true, Crashed: true}, "fixture-release-1", "production"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := fixtureItems(t, tc.name)
			if len(items) != 1 || items[0].Header.Type != TypeSession {
				t.Fatalf("items = %+v", items)
			}
			s, err := ParseSession(items[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			started, _ := time.Parse(time.RFC3339Nano, tc.started)
			if s.SID != tc.sid || s.DID != tc.did || s.Status != tc.status || s.Init != tc.init || s.Errors != tc.errors ||
				!s.Started.Equal(started) || s.Attrs.Release != tc.release || s.Attrs.Environment != tc.environment {
				t.Errorf("sesión = %+v", s)
			}
			if f := Flags(s.Status, s.Errors); f != tc.flags {
				t.Errorf("Flags = %+v, quiero %+v", f, tc.flags)
			}
		})
	}
}

func TestParseSessionAggregatesFixtures(t *testing.T) {
	for _, tc := range []struct {
		name                             string
		total, exited, errored, crashed  int64
		release, environment, startedISO string
	}{
		{"api-sesiones-1", 4, 3, 0, 1, "fixture-release-1", "production", "2026-11-22T15:37:00Z"},
		{"api-excepcion-2", 1, 0, 0, 1, "fixture-release-1", "production", "2026-11-19T17:05:00Z"},
		{"api-descartado-1", 1, 1, 0, 0, "fixture-release-1", "production", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			items := fixtureItems(t, tc.name)
			if len(items) != 1 || items[0].Header.Type != TypeSessions {
				t.Fatalf("items = %+v", items)
			}
			s, err := ParseSessionAggregates(items[0].Payload)
			if err != nil {
				t.Fatal(err)
			}
			if s.Attrs.Release != tc.release || s.Attrs.Environment != tc.environment || len(s.Aggregates) != 1 {
				t.Fatalf("sessions = %+v", s)
			}
			a := s.Aggregates[0]
			if a.Total() != tc.total || a.Exited != tc.exited || a.Errored != tc.errored || a.Crashed != tc.crashed || a.DID != "" {
				t.Errorf("agregado = %+v", a)
			}
			if tc.startedISO != "" {
				want, _ := time.Parse(time.RFC3339, tc.startedISO)
				if !a.Started.Equal(want) {
					t.Errorf("started = %v", a.Started)
				}
			}
		})
	}
}

func TestParseSessionMalformed(t *testing.T) {
	for name, payload := range map[string]string{
		"json":        `{"sid":`,
		"sin sid":     `{"init":true,"attrs":{"release":"1.0"}}`,
		"sin release": `{"sid":"abc","init":true,"attrs":{}}`,
		"negativo":    `{"sid":"abc","errors":-1,"attrs":{"release":"1.0"}}`,
	} {
		if _, err := ParseSession([]byte(payload)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	for name, payload := range map[string]string{
		"json":        `[`,
		"sin release": `{"aggregates":[{"exited":1}]}`,
		"negativo":    `{"attrs":{"release":"1.0"},"aggregates":[{"crashed":-1}]}`,
	} {
		if _, err := ParseSessionAggregates([]byte(payload)); !errors.Is(err, ErrMalformed) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
}

func TestParseSessionDefaults(t *testing.T) {
	s, err := ParseSession([]byte(`{"sid":"AB-CD","timestamp":"2026-11-20T10:00:00Z","attrs":{"release":" 1.0 "}}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.SID != "abcd" || s.Status != SessionOK || s.Attrs.Release != "1.0" || !s.Started.Equal(s.Timestamp) {
		t.Errorf("sesión = %+v", s)
	}
}

func TestSessionFlags(t *testing.T) {
	for _, tc := range []struct {
		status   string
		errors   int64
		want     SessionFlags
		terminal bool
	}{
		{SessionOK, 0, SessionFlags{}, false},
		{SessionOK, 2, SessionFlags{Errored: true}, false},
		{SessionExited, 0, SessionFlags{}, true},
		{SessionErrored, 0, SessionFlags{Errored: true}, false},
		{SessionCrashed, 0, SessionFlags{Errored: true, Crashed: true}, true},
		{SessionUnhandled, 1, SessionFlags{Errored: true, Crashed: true}, true},
		{SessionAbnormal, 0, SessionFlags{Errored: true, Abnormal: true}, true},
	} {
		if got := Flags(tc.status, tc.errors); got != tc.want {
			t.Errorf("Flags(%s, %d) = %+v", tc.status, tc.errors, got)
		}
		if Terminal(tc.status) != tc.terminal {
			t.Errorf("Terminal(%s) = %v", tc.status, !tc.terminal)
		}
	}
}
