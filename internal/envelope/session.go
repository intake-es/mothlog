package envelope

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/intake-es/mothlog/internal/event"
)

// MaxAggregates es cuántos grupos puede traer un item sessions.
const MaxAggregates = 1000

// Estados de una sesión. unhandled es como cuentan los SDK del navegador una
// sesión con un error sin capturar; aquí cuenta como crash.
const (
	SessionOK        = "ok"
	SessionExited    = "exited"
	SessionErrored   = "errored"
	SessionCrashed   = "crashed"
	SessionAbnormal  = "abnormal"
	SessionUnhandled = "unhandled"
)

type SessionAttrs struct {
	Release     string `json:"release"`
	Environment string `json:"environment"`
}

// clean deja la release y el entorno como los guarda la API. Sin release, o
// con una release o un entorno que la API no aceptaría, la sesión no vale: no
// se le quita el entorno porque sumaría en el de las sesiones sin él.
func (a *SessionAttrs) clean(item string) error {
	rel, err := event.CleanName(a.Release)
	if err != nil {
		return fmt.Errorf("%w: %s with invalid release: %v", ErrMalformed, item, err)
	}
	a.Release = rel
	if strings.TrimSpace(a.Environment) == "" {
		a.Environment = ""
		return nil
	}
	env, err := event.CleanName(a.Environment)
	if err != nil {
		return fmt.Errorf("%w: %s with invalid environment: %v", ErrMalformed, item, err)
	}
	a.Environment = env
	return nil
}

// Session es un item session: el inicio, una actualización o el final de una
// sesión.
type Session struct {
	SID       string       `json:"sid"`
	DID       string       `json:"did"`
	Init      bool         `json:"init"`
	Started   time.Time    `json:"started"`
	Timestamp time.Time    `json:"timestamp"`
	Status    string       `json:"status"`
	Errors    int64        `json:"errors"`
	Attrs     SessionAttrs `json:"attrs"`
}

// Terminal dice si el estado cierra la sesión: lo que llegue después no lo cambia.
func Terminal(status string) bool {
	switch status {
	case SessionExited, SessionCrashed, SessionAbnormal, SessionUnhandled:
		return true
	}
	return false
}

// SessionFlags es lo que cuenta una sesión con ese estado y esos errores.
type SessionFlags struct {
	Errored, Crashed, Abnormal bool
}

func Flags(status string, errors int64) SessionFlags {
	crashed := status == SessionCrashed || status == SessionUnhandled
	abnormal := status == SessionAbnormal
	return SessionFlags{
		Errored:  errors > 0 || status == SessionErrored || crashed || abnormal,
		Crashed:  crashed,
		Abnormal: abnormal,
	}
}

// ParseSession lee un item session. Sin sid ni release no se puede contar.
func ParseSession(payload []byte) (*Session, error) {
	var s Session
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("%w: session: %v", ErrMalformed, err)
	}
	s.SID = strings.ToLower(strings.ReplaceAll(s.SID, "-", ""))
	if s.SID == "" {
		return nil, fmt.Errorf("%w: session without sid", ErrMalformed)
	}
	if err := s.Attrs.clean("session"); err != nil {
		return nil, err
	}
	switch {
	case s.Errors < 0:
		return nil, fmt.Errorf("%w: session with negative errors", ErrMalformed)
	}
	if s.Status == "" {
		s.Status = SessionOK
	}
	if s.Started.IsZero() {
		s.Started = s.Timestamp
	}
	return &s, nil
}

// SessionAggregate son las sesiones de un minuto (o de una hora) que los SDK
// de servidor mandan ya sumadas, de un usuario si traen did.
type SessionAggregate struct {
	Started   time.Time `json:"started"`
	DID       string    `json:"did"`
	Exited    int64     `json:"exited"`
	Errored   int64     `json:"errored"`
	Abnormal  int64     `json:"abnormal"`
	Crashed   int64     `json:"crashed"`
	Unhandled int64     `json:"unhandled"`
}

// Total son todas las sesiones del grupo: cada estado cuenta una vez.
func (a SessionAggregate) Total() int64 {
	return a.Exited + a.Errored + a.Abnormal + a.Crashed + a.Unhandled
}

// SessionAggregates es un item sessions.
type SessionAggregates struct {
	Attrs      SessionAttrs       `json:"attrs"`
	Aggregates []SessionAggregate `json:"aggregates"`
}

func ParseSessionAggregates(payload []byte) (*SessionAggregates, error) {
	var s SessionAggregates
	if err := json.Unmarshal(payload, &s); err != nil {
		return nil, fmt.Errorf("%w: sessions: %v", ErrMalformed, err)
	}
	if err := s.Attrs.clean("sessions"); err != nil {
		return nil, err
	}
	if len(s.Aggregates) > MaxAggregates {
		return nil, fmt.Errorf("%w: sessions with more than %d groups", ErrMalformed, MaxAggregates)
	}
	for _, a := range s.Aggregates {
		if a.Exited < 0 || a.Errored < 0 || a.Abnormal < 0 || a.Crashed < 0 || a.Unhandled < 0 {
			return nil, fmt.Errorf("%w: sessions with a negative count", ErrMalformed)
		}
	}
	return &s, nil
}

// Sessions devuelve los items session y sessions.
func (e *Envelope) Sessions() []Item {
	var out []Item
	for _, it := range e.Items {
		if it.Header.Type == TypeSession || it.Header.Type == TypeSessions {
			out = append(out, it)
		}
	}
	return out
}
