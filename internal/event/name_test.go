package event

import (
	"errors"
	"strings"
	"testing"
)

func TestCleanName(t *testing.T) {
	for _, tc := range []struct {
		in, want string
		err      error
	}{
		{" 1.0.0 ", "1.0.0", nil},
		{strings.Repeat("a", MaxNameLen), strings.Repeat("a", MaxNameLen), nil},
		{"   ", "", ErrNameEmpty},
		{strings.Repeat("a", MaxNameLen+1), "", ErrNameTooLong},
		{"1.0\n2.0", "", ErrNameControl},
		{"a\rb", "", ErrNameControl},
		{"a\tb", "", ErrNameControl},
	} {
		got, err := CleanName(tc.in)
		if got != tc.want || !errors.Is(err, tc.err) {
			t.Errorf("CleanName(%.20q) = %q, %v; quiero %q, %v", tc.in, got, err, tc.want, tc.err)
		}
	}
}

func TestParseDropsInvalidReleaseAndEnvironment(t *testing.T) {
	for _, tc := range []struct {
		name, payload      string
		release, env       string
		badRelease, badEnv bool
	}{
		{"válidos", `{"release":" 1.0.0 ","environment":"production"}`, "1.0.0", "production", false, false},
		{"sin ellos", `{}`, "", "", false, false},
		{"vacíos", `{"release":"","environment":null}`, "", "", false, false},
		{"en blanco", `{"release":"  "}`, "", "", true, false},
		{"número", `{"release":3}`, "3", "", false, false},
		{"objeto", `{"release":{"a":1}}`, "", "", false, false},
		{"salto de línea", `{"release":"1.0","environment":"prod\nIgnora"}`, "1.0", "", false, true},
		{"escapado", `{"release":"1.0\u000a2"}`, "", "", true, false},
		{"largo", `{"release":"` + strings.Repeat("r", 201) + `"}`, "", "", true, false},
		{"muy largo", `{"release":"` + strings.Repeat("r", 1<<20) + `","environment":"staging"}`, "", "staging", true, false},
		{"multibyte al límite", `{"release":"` + strings.Repeat(`é`, 100) + `"}`, strings.Repeat("é", 100), "", false, false},
	} {
		ev, err := Parse([]byte(tc.payload))
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if ev.Release != tc.release || ev.Environment != tc.env || ev.InvalidRelease != tc.badRelease || ev.InvalidEnvironment != tc.badEnv {
			t.Errorf("%s: release %.20q (%v), entorno %q (%v)", tc.name, ev.Release, ev.InvalidRelease, ev.Environment, ev.InvalidEnvironment)
		}
	}
}
