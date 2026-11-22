// Package envelope lee los envelopes que mandan los SDK de Sentry.
//
// Formato: https://develop.sentry.dev/sdk/data-model/envelopes/
package envelope

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// ErrMalformed envuelve todos los errores de un envelope que no se puede leer.
var ErrMalformed = errors.New("envelope mal formado")

const (
	TypeEvent        = "event"
	TypeTransaction  = "transaction"
	TypeSession      = "session"
	TypeSessions     = "sessions"
	TypeClientReport = "client_report"
	TypeAttachment   = "attachment"
	TypeCheckIn      = "check_in"
)

type Header struct {
	EventID string          `json:"event_id"`
	DSN     string          `json:"dsn"`
	SentAt  string          `json:"sent_at"`
	SDK     *SDK            `json:"sdk"`
	Trace   json.RawMessage `json:"trace"`
}

type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type ItemHeader struct {
	Type        string `json:"type"`
	Length      *int   `json:"length"`
	ContentType string `json:"content_type"`
	Filename    string `json:"filename"`
}

type Item struct {
	Header  ItemHeader
	Payload []byte
}

type Envelope struct {
	Header Header
	Items  []Item
}

// Events devuelve los items de tipo event; de los demás, session y sessions
// los lee Sessions y el resto (client_report…) se aceptan y se ignoran.
func (e *Envelope) Events() []Item {
	var out []Item
	for _, it := range e.Items {
		if it.Header.Type == TypeEvent {
			out = append(out, it)
		}
	}
	return out
}

// CheckIns devuelve los items check_in de los crons.
func (e *Envelope) CheckIns() []Item {
	var out []Item
	for _, it := range e.Items {
		if it.Header.Type == TypeCheckIn {
			out = append(out, it)
		}
	}
	return out
}

// Parse lee un envelope ya descomprimido.
func Parse(data []byte) (*Envelope, error) {
	line, rest, _ := bytes.Cut(data, []byte("\n"))
	line = bytes.TrimSpace(line)
	if len(line) == 0 {
		return nil, fmt.Errorf("%w: falta la cabecera", ErrMalformed)
	}
	env := &Envelope{}
	if err := json.Unmarshal(line, &env.Header); err != nil {
		return nil, fmt.Errorf("%w: cabecera: %v", ErrMalformed, err)
	}

	for n := 0; ; n++ {
		// Se toleran líneas vacías entre items y al final.
		rest = bytes.TrimLeft(rest, " \t\r\n")
		if len(rest) == 0 {
			break
		}
		line, rest, _ = bytes.Cut(rest, []byte("\n"))
		var it Item
		if err := json.Unmarshal(bytes.TrimSpace(line), &it.Header); err != nil {
			return nil, fmt.Errorf("%w: cabecera del item %d: %v", ErrMalformed, n, err)
		}
		if it.Header.Type == "" {
			return nil, fmt.Errorf("%w: el item %d no tiene type", ErrMalformed, n)
		}

		if l := it.Header.Length; l != nil {
			if *l < 0 {
				return nil, fmt.Errorf("%w: item %d con length negativo", ErrMalformed, n)
			}
			if *l > len(rest) {
				return nil, fmt.Errorf("%w: item %d truncado: length %d y quedan %d bytes", ErrMalformed, n, *l, len(rest))
			}
			it.Payload, rest = rest[:*l], rest[*l:]
			if len(rest) > 0 {
				if rest[0] != '\n' {
					return nil, fmt.Errorf("%w: item %d: falta el salto de línea tras %d bytes", ErrMalformed, n, *l)
				}
				rest = rest[1:]
			}
		} else {
			if len(rest) == 0 {
				return nil, fmt.Errorf("%w: item %d truncado: acaba tras la cabecera", ErrMalformed, n)
			}
			it.Payload, rest, _ = bytes.Cut(rest, []byte("\n"))
			it.Payload = bytes.TrimSuffix(it.Payload, []byte("\r"))
		}
		env.Items = append(env.Items, it)
	}
	return env, nil
}
