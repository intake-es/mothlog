// Package lang es el idioma de lo que el servicio escribe para quien lo usa:
// avisos, Markdown para agentes, el MCP y llms.txt.
package lang

import "fmt"

// Lang es un idioma: en o es.
type Lang string

const (
	EN Lang = "en"
	ES Lang = "es"
)

// Env es la variable que elige el idioma por defecto.
const Env = "DEFAULT_LANGUAGE"

// Parse lee el valor de DEFAULT_LANGUAGE; vacío es inglés.
func Parse(s string) (Lang, error) {
	switch Lang(s) {
	case "", EN:
		return EN, nil
	case ES:
		return ES, nil
	}
	return EN, fmt.Errorf("%s must be en or es, not %q", Env, s)
}

// T elige el texto del idioma; cualquier idioma que no sea es, en inglés.
func (l Lang) T(en, es string) string {
	if l == ES {
		return es
	}
	return en
}

// F es T con formato.
func (l Lang) F(en, es string, a ...any) string { return fmt.Sprintf(l.T(en, es), a...) }

// N es T con singular y plural según n.
func (l Lang) N(n int64, enOne, enOther, esOne, esOther string) string {
	if n == 1 {
		return l.T(enOne, esOne)
	}
	return l.T(enOther, esOther)
}

// String es el código del idioma; vacío es en.
func (l Lang) String() string { return l.T("en", "es") }
