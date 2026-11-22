package search

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		in   string
		want []Term
	}{
		{"", nil},
		{"   ", nil},
		{"timeout", []Term{{Value: "timeout"}}},
		{"division by zero", []Term{{Value: "division"}, {Value: "by"}, {Value: "zero"}}},
		{`"division by zero"`, []Term{{Value: "division by zero"}}},
		{"is:open", []Term{{Key: "is", Value: "open"}}},
		{"is:resolved", []Term{{Key: "is", Value: "resolved"}}},
		{"is:resolved_next", []Term{{Key: "is", Value: "resolved_next"}}},
		{"is:silenced", []Term{{Key: "is", Value: "silenced"}}},
		{"IS:Regressed", []Term{{Key: "is", Value: "regressed"}}},
		{"release:1.2.0", []Term{{Key: "release", Value: "1.2.0"}}},
		{"environment:production", []Term{{Key: "environment", Value: "production"}}},
		{"level:warning", []Term{{Key: "level", Value: "warning"}}},
		{"project:web", []Term{{Key: "project", Value: "web"}}},
		{"user:ana@example.com", []Term{{Key: "user", Value: "ana@example.com"}}},
		{"browser:Safari", []Term{{Key: "browser", Value: "Safari"}}},
		{`os:"Mac OS"`, []Term{{Key: "os", Value: "Mac OS"}}},
		{"Component:api", []Term{{Key: "Component", Value: "api"}}},
		{"browser.version:Chrome-156", []Term{{Key: "browser.version", Value: "Chrome-156"}}},
		{"!browser:Chrome", []Term{{Key: "browser", Value: "Chrome", Negate: true}}},
		{"!is:resolved", []Term{{Key: "is", Value: "resolved", Negate: true}}},
		{"!timeout", []Term{{Value: "timeout", Negate: true}}},
		{`!"timed out"`, []Term{{Value: "timed out", Negate: true}}},
		{`tag:"con \"comillas\" y \\"`, []Term{{Key: "tag", Value: `con "comillas" y \`}}},
		{`os:""`, []Term{{Key: "os", Value: ""}}},
		{"release:a:b", []Term{{Key: "release", Value: "a:b"}}},
		{"https://app.example.com/x", []Term{{Value: "https://app.example.com/x"}}},
		{"1:2", []Term{{Value: "1:2"}}},
		{"a!b", []Term{{Value: "a!b"}}},
		{
			`is:open  project:web	!environment:staging os:"Mac OS" "can't read" reduce`,
			[]Term{
				{Key: "is", Value: "open"},
				{Key: "project", Value: "web"},
				{Key: "environment", Value: "staging", Negate: true},
				{Key: "os", Value: "Mac OS"},
				{Value: "can't read"},
				{Value: "reduce"},
			},
		},
		{"año:ñu", []Term{{Value: "año:ñu"}}},
		{"TypeError: Cannot read", []Term{{Value: "TypeError:"}, {Value: "Cannot"}, {Value: "read"}}},
		{"browser:", []Term{{Value: "browser:"}}},
		{"!release: x", []Term{{Value: "release:", Negate: true}, {Value: "x"}}},
	}
	for _, tt := range tests {
		q, err := Parse(tt.in)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(q.Terms, tt.want) {
			t.Errorf("Parse(%q) = %+v; quiero %+v", tt.in, q.Terms, tt.want)
		}
	}
}

func TestParseErrors(t *testing.T) {
	tests := []struct {
		in    string
		code  string
		pos   int
		inMsg string
	}{
		{`os:"Mac OS`, ErrUnclosedQuote, 3, "comillas de cierre"},
		{`"sin cerrar`, ErrUnclosedQuote, 0, "comillas de cierre"},
		{`timeout "a`, ErrUnclosedQuote, 8, "carácter 9"},
		{":Chrome", ErrEmptyKey, 0, "falta la clave"},
		{"! browser:Chrome", ErrLoneNegation, 0, "!browser:Chrome"},
		{"timeout !", ErrLoneNegation, 8, "!"},
		{"is:abierto", ErrBadStatus, 3, "is:abierto no existe; is: es open, resolved, resolved_next, silenced, regressed"},
		{`os:"Mac"OS`, ErrAfterQuote, 8, "espacio"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		var se *Error
		if !errors.As(err, &se) {
			t.Errorf("Parse(%q): %v; quiero un *Error", tt.in, err)
			continue
		}
		if se.Code != tt.code || se.Pos != tt.pos {
			t.Errorf("Parse(%q) = %s en %d; quiero %s en %d", tt.in, se.Code, se.Pos, tt.code, tt.pos)
		}
		if !strings.Contains(err.Error(), tt.inMsg) {
			t.Errorf("Parse(%q): %q no dice %q", tt.in, err.Error(), tt.inMsg)
		}
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"Chrome":    "Chrome",
		"Mac OS":    `"Mac OS"`,
		`a"b`:       `"a\"b"`,
		"":          `""`,
		`c:\dir`:    `"c:\\dir"`,
		"1.2.0+abc": "1.2.0+abc",
	} {
		got := Quote(in)
		if got != want {
			t.Errorf("Quote(%q) = %s; quiero %s", in, got, want)
		}
		q, err := Parse("k:" + got)
		if err != nil || len(q.Terms) != 1 || q.Terms[0].Value != in {
			t.Errorf("Parse(k:%s) = %+v, %v; no vuelve a %q", got, q.Terms, err, in)
		}
	}
}
