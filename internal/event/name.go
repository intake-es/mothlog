package event

import (
	"bytes"
	"errors"
	"strings"
)

// MaxNameLen es la longitud máxima, en bytes, de una release o un entorno.
const MaxNameLen = 200

var (
	ErrNameEmpty   = errors.New("empty")
	ErrNameTooLong = errors.New("too long")
	ErrNameControl = errors.New("with line breaks or tabs")
)

// CleanName aplica a una release o un entorno las reglas de la API: sin
// espacios alrededor, no vacío, de MaxNameLen bytes como máximo y sin CR, LF
// ni tabuladores.
func CleanName(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case s == "":
		return "", ErrNameEmpty
	case len(s) > MaxNameLen:
		return "", ErrNameTooLong
	case strings.ContainsAny(s, "\n\r\t"):
		return "", ErrNameControl
	}
	return s, nil
}

// name es una release o un entorno tal como llega. Si el JSON ya es más largo
// de lo que puede ocupar un nombre válido, ni se decodifica.
type name struct {
	s       string
	invalid bool
}

// maxNameJSON es lo más que ocupa en JSON un nombre válido: \u00XX por byte.
const maxNameJSON = 6*MaxNameLen + 2

func (n *name) UnmarshalJSON(b []byte) error {
	*n = name{}
	b = bytes.TrimSpace(b)
	if len(b) > maxNameJSON {
		n.invalid = true
		return nil
	}
	var t text
	if err := t.UnmarshalJSON(b); err != nil {
		return err
	}
	if t == "" {
		return nil
	}
	s, err := CleanName(string(t))
	if err != nil {
		n.invalid = true
		return nil
	}
	n.s = s
	return nil
}
