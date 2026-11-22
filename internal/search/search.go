// Package search lee el lenguaje de búsqueda de issues: términos separados
// por espacios que se cumplen todos a la vez.
//
//	is:open release:1.2.0 browser:Safari os:"Mac OS" !environment:staging assigned:me timeout
package search

import (
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// Statuses son los valores de is:.
var Statuses = []string{"open", "resolved", "resolved_next", "silenced", "regressed", "assigned", "unassigned"}

// assignment son los valores de is: que miran el asignado y no el estado.
var assignment = []string{"assigned", "unassigned"}

// Keys son las claves con un significado propio; cualquier otra es una etiqueta.
var Keys = []string{"is", "release", "environment", "level", "project", "user", "assigned"}

// Me, como valor de assigned:, es quien busca.
const Me = "me"

// Term es un término de la búsqueda. Sin Key, Value es texto libre.
type Term struct {
	Key    string `json:"key,omitempty"`
	Value  string `json:"value"`
	Negate bool   `json:"negate,omitempty"`
}

type Query struct {
	Terms []Term
}

// Status devuelve los términos is: de la búsqueda que miran el estado.
func (q Query) Status() []Term {
	var out []Term
	for _, t := range q.Terms {
		if t.Key == "is" && !slices.Contains(assignment, t.Value) {
			out = append(out, t)
		}
	}
	return out
}

// Error es un error de sintaxis. Code y los demás campos dejan a la interfaz
// escribirlo en su idioma; Error() lo da en español.
type Error struct {
	Code string `json:"code"`
	// Pos es la posición, en caracteres desde 0, donde está el problema.
	Pos   int    `json:"position"`
	Key   string `json:"key,omitempty"`
	Value string `json:"value,omitempty"`
}

const (
	ErrUnclosedQuote = "unclosed_quote"
	ErrEmptyKey      = "empty_key"
	ErrLoneNegation  = "lone_negation"
	ErrBadStatus     = "bad_status"
	ErrAfterQuote    = "after_quote"
)

func (e *Error) Error() string {
	var msg string
	switch e.Code {
	case ErrUnclosedQuote:
		msg = "faltan las comillas de cierre"
	case ErrEmptyKey:
		msg = "falta la clave antes de los dos puntos"
	case ErrLoneNegation:
		msg = "! va pegado al término que niega, como !browser:Chrome"
	case ErrBadStatus:
		msg = fmt.Sprintf("is:%s no existe; is: es %s", e.Value, strings.Join(Statuses, ", "))
	case ErrAfterQuote:
		msg = "tras las comillas de cierre va un espacio"
	default:
		msg = e.Code
	}
	return fmt.Sprintf("búsqueda, carácter %d: %s", e.Pos+1, msg)
}

// Parse lee una búsqueda. Una clave es una letra o _ seguida de letras,
// números, _, . o -; lo que no tiene esa forma antes de los dos puntos, sigue
// con //, como una URL, o no tiene nada detrás de ellos, es texto.
func Parse(s string) (Query, error) {
	p := parser{src: []rune(s)}
	var q Query
	for {
		p.skipSpace()
		if p.eof() {
			return q, nil
		}
		t, err := p.term()
		if err != nil {
			return Query{}, err
		}
		q.Terms = append(q.Terms, t)
	}
}

type parser struct {
	src []rune
	pos int
}

func (p *parser) eof() bool { return p.pos >= len(p.src) }

func (p *parser) peek() rune { return p.src[p.pos] }

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

func (p *parser) skipSpace() {
	for !p.eof() && isSpace(p.peek()) {
		p.pos++
	}
}

func (p *parser) term() (Term, error) {
	start := p.pos
	var t Term
	if p.peek() == '!' {
		t.Negate = true
		p.pos++
		if p.eof() || isSpace(p.peek()) {
			return t, &Error{Code: ErrLoneNegation, Pos: start}
		}
	}
	if p.peek() == '"' {
		v, err := p.quoted()
		if err != nil {
			return t, err
		}
		t.Value = v
		return t, nil
	}
	wordStart := p.pos
	for !p.eof() && !isSpace(p.peek()) && p.peek() != ':' {
		p.pos++
	}
	word := string(p.src[wordStart:p.pos])
	if p.eof() || p.peek() != ':' {
		t.Value = word
		return t, nil
	}
	if word == "" {
		return t, &Error{Code: ErrEmptyKey, Pos: p.pos}
	}
	isURL := p.pos+2 < len(p.src) && p.src[p.pos+1] == '/' && p.src[p.pos+2] == '/'
	if !validKey(word) || isURL {
		// Una URL o un «a:b» que no es una clave: texto hasta el espacio.
		for !p.eof() && !isSpace(p.peek()) {
			p.pos++
		}
		t.Value = string(p.src[wordStart:p.pos])
		return t, nil
	}
	p.pos++ // ':'
	if p.eof() || isSpace(p.peek()) {
		// «TypeError: Cannot read…» pegado de un título: texto, no una clave sin valor.
		t.Value = word + ":"
		return t, nil
	}
	t.Key = word
	if k := strings.ToLower(word); slices.Contains(Keys, k) {
		t.Key = k
	}
	valStart := p.pos
	if p.peek() == '"' {
		v, err := p.quoted()
		if err != nil {
			return t, err
		}
		t.Value = v
	} else {
		for !p.eof() && !isSpace(p.peek()) {
			p.pos++
		}
		t.Value = string(p.src[valStart:p.pos])
	}
	if t.Key == "is" {
		t.Value = strings.ToLower(t.Value)
		if !slices.Contains(Statuses, t.Value) {
			return t, &Error{Code: ErrBadStatus, Pos: valStart, Key: "is", Value: t.Value}
		}
	}
	return t, nil
}

// quoted lee "…" con \" y \\ como escapes.
func (p *parser) quoted() (string, error) {
	open := p.pos
	p.pos++
	var b strings.Builder
	for {
		if p.eof() {
			return "", &Error{Code: ErrUnclosedQuote, Pos: open}
		}
		r := p.peek()
		p.pos++
		switch {
		case r == '\\' && !p.eof() && (p.peek() == '"' || p.peek() == '\\'):
			b.WriteRune(p.peek())
			p.pos++
		case r == '"':
			if !p.eof() && !isSpace(p.peek()) {
				return "", &Error{Code: ErrAfterQuote, Pos: p.pos}
			}
			return b.String(), nil
		default:
			b.WriteRune(r)
		}
	}
}

func validKey(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > MaxKeyLen {
		return false
	}
	for i, r := range s {
		letter := r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if i == 0 && !letter {
			return false
		}
		digit := r >= '0' && r <= '9'
		if !letter && !digit && r != '.' && r != '-' {
			return false
		}
	}
	return true
}

// MaxKeyLen y MaxValueLen son lo más largo que se guarda de una etiqueta.
const (
	MaxKeyLen   = 32
	MaxValueLen = 200
)

// ValidKey dice si s vale como clave de etiqueta.
func ValidKey(s string) bool { return validKey(s) }

// Quote escribe v como valor de un término, entre comillas si hace falta.
func Quote(v string) string {
	if v != "" && !strings.ContainsAny(v, " \t\n\r\"\\") {
		return v
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(v) + `"`
}
