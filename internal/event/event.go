// Package event pasa el payload de un item event a un Event normalizado.
//
// Protocolo: https://develop.sentry.dev/sdk/data-model/event-payloads/
package event

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

var ErrInvalid = errors.New("evento no válido")

type Event struct {
	ID          string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	Platform    string            `json:"platform,omitempty"`
	Level       string            `json:"level"`
	Logger      string            `json:"logger,omitempty"`
	Release     string            `json:"release,omitempty"`
	Dist        string            `json:"dist,omitempty"`
	Environment string            `json:"environment,omitempty"`
	SDK         SDK               `json:"sdk"`
	Message     string            `json:"message,omitempty"`
	Exceptions  []Exception       `json:"exceptions,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	User        *User             `json:"user,omitempty"`
	Request     *Request          `json:"request,omitempty"`
	Transaction string            `json:"transaction,omitempty"`
	Fingerprint []string          `json:"fingerprint,omitempty"`
	ServerName  string            `json:"server_name,omitempty"`
	Breadcrumbs json.RawMessage   `json:"breadcrumbs,omitempty"`
	Contexts    json.RawMessage   `json:"contexts,omitempty"`
	// DebugMeta va tal cual; sus imágenes sourcemap dicen el debug ID de cada .js.
	DebugMeta json.RawMessage `json:"debug_meta,omitempty"`
}

type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Exception va en el orden del SDK: la causa primero y la que se lanzó al final.
type Exception struct {
	Type      string     `json:"type,omitempty"`
	Value     string     `json:"value,omitempty"`
	Module    string     `json:"module,omitempty"`
	Mechanism *Mechanism `json:"mechanism,omitempty"`
	Frames    []Frame    `json:"frames,omitempty"`
}

type Mechanism struct {
	Type    string `json:"type,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
}

// Frame va en el orden del SDK: el más externo primero y el que falló al final.
type Frame struct {
	Filename    string   `json:"filename,omitempty"`
	AbsPath     string   `json:"abs_path,omitempty"`
	Function    string   `json:"function,omitempty"`
	Module      string   `json:"module,omitempty"`
	Line        int      `json:"lineno,omitempty"`
	Column      int      `json:"colno,omitempty"`
	InApp       *bool    `json:"in_app,omitempty"`
	PreContext  []string `json:"pre_context,omitempty"`
	ContextLine string   `json:"context_line,omitempty"`
	PostContext []string `json:"post_context,omitempty"`
	// Minified es la posición en el JS minificado cuando un source map la tradujo.
	Minified *Position `json:"minified,omitempty"`
}

type Position struct {
	Filename string `json:"filename"`
	Line     int    `json:"lineno"`
	Column   int    `json:"colno"`
}

type User struct {
	ID        string `json:"id,omitempty"`
	Email     string `json:"email,omitempty"`
	Username  string `json:"username,omitempty"`
	IPAddress string `json:"ip_address,omitempty"`
}

type Request struct {
	Method    string `json:"method,omitempty"`
	URL       string `json:"url,omitempty"`
	UserAgent string `json:"user_agent,omitempty"`
}

// text acepta cualquier valor JSON escalar como cadena; un objeto o una lista
// queda vacío. Así un tipo inesperado en un campo no tira el evento entero.
type text string

func (t *text) UnmarshalJSON(b []byte) error {
	b = nonNull(b)
	if len(b) > 0 && (b[0] == '{' || b[0] == '[') {
		*t = ""
		return nil
	}
	*t = text(scalar(b))
	return nil
}

type raw struct {
	EventID     text            `json:"event_id"`
	Timestamp   json.RawMessage `json:"timestamp"`
	Platform    text            `json:"platform"`
	Level       text            `json:"level"`
	Logger      text            `json:"logger"`
	Release     text            `json:"release"`
	Dist        text            `json:"dist"`
	Environment text            `json:"environment"`
	SDK         json.RawMessage `json:"sdk"`
	Logentry    json.RawMessage `json:"logentry"`
	Message     json.RawMessage `json:"message"`
	Exception   json.RawMessage `json:"exception"`
	Tags        json.RawMessage `json:"tags"`
	User        json.RawMessage `json:"user"`
	Request     json.RawMessage `json:"request"`
	Transaction text            `json:"transaction"`
	Fingerprint json.RawMessage `json:"fingerprint"`
	ServerName  text            `json:"server_name"`
	Breadcrumbs json.RawMessage `json:"breadcrumbs"`
	Contexts    json.RawMessage `json:"contexts"`
	DebugMeta   json.RawMessage `json:"debug_meta"`
}

type rawMessage struct {
	Formatted text `json:"formatted"`
	Message   text `json:"message"`
}

type rawException struct {
	Type       text            `json:"type"`
	Value      text            `json:"value"`
	Module     text            `json:"module"`
	Mechanism  json.RawMessage `json:"mechanism"`
	Stacktrace json.RawMessage `json:"stacktrace"`
}

type rawFrame struct {
	Filename    text            `json:"filename"`
	AbsPath     text            `json:"abs_path"`
	Function    text            `json:"function"`
	Module      text            `json:"module"`
	Lineno      json.RawMessage `json:"lineno"`
	Colno       json.RawMessage `json:"colno"`
	InApp       json.RawMessage `json:"in_app"`
	PreContext  json.RawMessage `json:"pre_context"`
	ContextLine text            `json:"context_line"`
	PostContext json.RawMessage `json:"post_context"`
}

// Parse normaliza el payload de un item event. Los campos que faltan quedan
// vacíos: completar el id o la fecha es cosa de quien lo recibe.
func Parse(payload []byte) (*Event, error) {
	var r raw
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	ts, err := parseTimestamp(r.Timestamp)
	if err != nil {
		return nil, err
	}
	excs, err := parseExceptions(r.Exception)
	if err != nil {
		return nil, err
	}
	e := &Event{
		ID:          normalizeID(string(r.EventID)),
		Timestamp:   ts,
		Platform:    string(r.Platform),
		Level:       string(r.Level),
		Logger:      string(r.Logger),
		Release:     string(r.Release),
		Dist:        string(r.Dist),
		Environment: string(r.Environment),
		SDK:         parseSDK(r.SDK),
		Message:     message(r),
		Exceptions:  excs,
		Tags:        parseTags(r.Tags),
		User:        parseUser(r.User),
		Request:     parseRequest(r.Request),
		Transaction: string(r.Transaction),
		ServerName:  string(r.ServerName),
		Breadcrumbs: nonNull(r.Breadcrumbs),
		Contexts:    nonNull(r.Contexts),
		DebugMeta:   object(r.DebugMeta),
	}
	if e.Level == "" {
		e.Level = "error"
	}
	var fp []text
	if json.Unmarshal(nonNull(r.Fingerprint), &fp) == nil {
		for _, f := range fp {
			e.Fingerprint = append(e.Fingerprint, string(f))
		}
	}
	return e, nil
}

// normalizeID deja el id como 32 caracteres hexadecimales en minúscula.
func normalizeID(id string) string {
	return strings.ToLower(strings.ReplaceAll(id, "-", ""))
}

// parseTimestamp acepta RFC 3339 (Python) o segundos Unix con decimales (JS).
func parseTimestamp(b json.RawMessage) (time.Time, error) {
	if len(nonNull(b)) == 0 {
		return time.Time{}, nil
	}
	var f float64
	if err := json.Unmarshal(b, &f); err == nil {
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return time.Time{}, fmt.Errorf("%w: timestamp %s", ErrInvalid, b)
		}
		sec, frac := math.Modf(f)
		return time.Unix(int64(sec), int64(math.Round(frac*1e6))*1e3).UTC(), nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return time.Time{}, fmt.Errorf("%w: timestamp %s", ErrInvalid, b)
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return parseTimestamp(json.RawMessage(strconv.FormatFloat(f, 'f', -1, 64)))
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%w: timestamp %q", ErrInvalid, s)
}

func parseSDK(b json.RawMessage) SDK {
	var sdk struct{ Name, Version text }
	_ = json.Unmarshal(nonNull(b), &sdk)
	return SDK{Name: string(sdk.Name), Version: string(sdk.Version)}
}

func message(r raw) string {
	var le rawMessage
	if json.Unmarshal(nonNull(r.Logentry), &le) == nil {
		if le.Formatted != "" {
			return string(le.Formatted)
		}
		if le.Message != "" {
			return string(le.Message)
		}
	}
	var s text
	if json.Unmarshal(nonNull(r.Message), &s) == nil && s != "" {
		return string(s)
	}
	// message también puede venir con la forma de logentry.
	var m rawMessage
	if json.Unmarshal(nonNull(r.Message), &m) == nil {
		if m.Formatted != "" {
			return string(m.Formatted)
		}
		return string(m.Message)
	}
	return ""
}

// parseExceptions acepta {"values": [...]}, la lista directa o una sola excepción.
func parseExceptions(b json.RawMessage) ([]Exception, error) {
	b = nonNull(b)
	if len(b) == 0 {
		return nil, nil
	}
	var list []rawException
	switch b[0] {
	case '[':
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, fmt.Errorf("%w: exception: %v", ErrInvalid, err)
		}
	case '{':
		var wrap struct {
			Values *[]rawException `json:"values"`
		}
		if err := json.Unmarshal(b, &wrap); err != nil {
			return nil, fmt.Errorf("%w: exception: %v", ErrInvalid, err)
		}
		if wrap.Values != nil {
			list = *wrap.Values
			break
		}
		var one rawException
		if err := json.Unmarshal(b, &one); err != nil {
			return nil, fmt.Errorf("%w: exception: %v", ErrInvalid, err)
		}
		if one.Type != "" || len(one.Value) > 0 {
			list = []rawException{one}
		}
	default:
		return nil, fmt.Errorf("%w: exception no es un objeto ni una lista", ErrInvalid)
	}

	out := make([]Exception, 0, len(list))
	for _, x := range list {
		e := Exception{Type: string(x.Type), Value: string(x.Value), Module: string(x.Module), Mechanism: parseMechanism(x.Mechanism)}
		var st struct {
			Frames []json.RawMessage `json:"frames"`
		}
		_ = json.Unmarshal(nonNull(x.Stacktrace), &st)
		for _, fb := range st.Frames {
			var f rawFrame
			if json.Unmarshal(fb, &f) != nil {
				continue
			}
			e.Frames = append(e.Frames, Frame{
				Filename:    string(f.Filename),
				AbsPath:     string(f.AbsPath),
				Function:    string(f.Function),
				Module:      string(f.Module),
				Line:        intOf(f.Lineno),
				Column:      intOf(f.Colno),
				InApp:       boolOf(f.InApp),
				PreContext:  lines(f.PreContext),
				ContextLine: string(f.ContextLine),
				PostContext: lines(f.PostContext),
			})
		}
		out = append(out, e)
	}
	return out, nil
}

func parseMechanism(b json.RawMessage) *Mechanism {
	var m struct {
		Type    text            `json:"type"`
		Handled json.RawMessage `json:"handled"`
	}
	if json.Unmarshal(nonNull(b), &m) != nil || (m.Type == "" && len(nonNull(m.Handled)) == 0) {
		return nil
	}
	return &Mechanism{Type: string(m.Type), Handled: boolOf(m.Handled)}
}

func boolOf(b json.RawMessage) *bool {
	var v bool
	if json.Unmarshal(nonNull(b), &v) != nil {
		return nil
	}
	return &v
}

// parseTags acepta un objeto o una lista de pares [clave, valor].
func parseTags(b json.RawMessage) map[string]string {
	b = nonNull(b)
	if len(b) == 0 {
		return nil
	}
	out := map[string]string{}
	var m map[string]json.RawMessage
	if json.Unmarshal(b, &m) == nil {
		for k, v := range m {
			out[k] = scalar(v)
		}
	} else {
		var pairs [][]json.RawMessage
		if json.Unmarshal(b, &pairs) == nil {
			for _, p := range pairs {
				if len(p) == 2 {
					out[scalar(p[0])] = scalar(p[1])
				}
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseUser(b json.RawMessage) *User {
	var u struct {
		ID        text `json:"id"`
		Email     text `json:"email"`
		Username  text `json:"username"`
		IPAddress text `json:"ip_address"`
	}
	if json.Unmarshal(nonNull(b), &u) != nil {
		return nil
	}
	user := User{ID: string(u.ID), Email: string(u.Email), Username: string(u.Username), IPAddress: string(u.IPAddress)}
	if user == (User{}) {
		return nil
	}
	return &user
}

func parseRequest(b json.RawMessage) *Request {
	var raw struct {
		Method  text            `json:"method"`
		URL     text            `json:"url"`
		Headers json.RawMessage `json:"headers"`
	}
	if json.Unmarshal(nonNull(b), &raw) != nil {
		return nil
	}
	r := Request{Method: string(raw.Method), URL: string(raw.URL)}
	// headers llega como objeto o como lista de pares, igual que tags.
	for k, v := range parseTags(raw.Headers) {
		if strings.EqualFold(k, "User-Agent") {
			r.UserAgent = v
		}
	}
	if r == (Request{}) {
		return nil
	}
	return &r
}

// scalar pasa a texto un valor JSON que debería ser una cadena pero a veces
// llega como número o booleano.
func scalar(b json.RawMessage) string {
	b = nonNull(b)
	if len(b) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(b, &s) == nil {
		return s
	}
	return string(b)
}

func intOf(b json.RawMessage) int {
	var f float64
	if json.Unmarshal(nonNull(b), &f) == nil && f >= 0 && f <= math.MaxInt32 {
		return int(f)
	}
	return 0
}

// lines devuelve nil si las líneas de contexto no llegan como lista.
func lines(b json.RawMessage) []string {
	var in []text
	if json.Unmarshal(nonNull(b), &in) != nil || len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = string(s)
	}
	return out
}

// object devuelve b si es un objeto JSON, o nil.
func object(b json.RawMessage) json.RawMessage {
	b = nonNull(b)
	if len(b) == 0 || b[0] != '{' {
		return nil
	}
	return b
}

func nonNull(b json.RawMessage) json.RawMessage {
	b = bytes.TrimSpace(b)
	if len(b) == 0 || bytes.Equal(b, []byte("null")) {
		return nil
	}
	return b
}
