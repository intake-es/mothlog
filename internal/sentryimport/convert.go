package sentryimport

import (
	"bytes"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// flexString acepta una cadena o un número: los ids llegan como cadena, pero
// un servidor viejo los puede mandar como número.
type flexString string

func (s *flexString) UnmarshalJSON(b []byte) error {
	b = bytes.TrimSpace(b)
	var v string
	if json.Unmarshal(b, &v) == nil {
		*s = flexString(v)
		return nil
	}
	if len(b) > 0 && b[0] != '{' && b[0] != '[' && !bytes.Equal(b, []byte("null")) {
		*s = flexString(b)
	}
	return nil
}

// flexInt acepta un número o una cadena con un número, como count.
type flexInt int64

func (n *flexInt) UnmarshalJSON(b []byte) error {
	var s flexString
	_ = s.UnmarshalJSON(b)
	v, err := strconv.ParseFloat(string(s), 64)
	if err == nil && v >= 0 {
		*n = flexInt(v)
	}
	return nil
}

// flexTime acepta una fecha RFC 3339; si no lo es, queda vacía.
type flexTime struct{ time.Time }

func (t *flexTime) UnmarshalJSON(b []byte) error {
	var s flexString
	_ = s.UnmarshalJSON(b)
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04:05.999999999"} {
		if v, err := time.Parse(layout, string(s)); err == nil {
			t.Time = v.UTC()
			return nil
		}
	}
	return nil
}

type apiProject struct {
	ID   flexString `json:"id"`
	Slug flexString `json:"slug"`
	Name flexString `json:"name"`
}

type apiIssue struct {
	ID            flexString `json:"id"`
	ShortID       flexString `json:"shortId"`
	Title         flexString `json:"title"`
	Culprit       flexString `json:"culprit"`
	Level         flexString `json:"level"`
	Status        flexString `json:"status"`
	StatusDetails struct {
		InRelease     flexString      `json:"inRelease"`
		InNextRelease json.RawMessage `json:"inNextRelease"`
	} `json:"statusDetails"`
	Metadata struct {
		Type  flexString `json:"type"`
		Value flexString `json:"value"`
	} `json:"metadata"`
	Count     flexInt  `json:"count"`
	UserCount flexInt  `json:"userCount"`
	FirstSeen flexTime `json:"firstSeen"`
	LastSeen  flexTime `json:"lastSeen"`
	// FirstRelease solo viene en el detalle del issue, no siempre en la lista.
	FirstRelease json.RawMessage `json:"firstRelease"`
}

type apiRelease struct {
	Version      flexString `json:"version"`
	DateCreated  flexTime   `json:"dateCreated"`
	DateReleased flexTime   `json:"dateReleased"`
}

type apiDeploy struct {
	ID           flexString `json:"id"`
	Environment  flexString `json:"environment"`
	DateStarted  flexTime   `json:"dateStarted"`
	DateFinished flexTime   `json:"dateFinished"`
}

type apiEntry struct {
	Type flexString      `json:"type"`
	Data json.RawMessage `json:"data"`
}

type apiTag struct {
	Key   flexString `json:"key"`
	Value flexString `json:"value"`
}

type apiEvent struct {
	EventID     flexString      `json:"eventID"`
	ID          flexString      `json:"id"`
	DateCreated json.RawMessage `json:"dateCreated"`
	Platform    flexString      `json:"platform"`
	Message     flexString      `json:"message"`
	Dist        flexString      `json:"dist"`
	Release     json.RawMessage `json:"release"`
	Tags        json.RawMessage `json:"tags"`
	Entries     json.RawMessage `json:"entries"`
	User        json.RawMessage `json:"user"`
	SDK         json.RawMessage `json:"sdk"`
	Contexts    json.RawMessage `json:"contexts"`
}

// derivedTags son las etiquetas que Sentry saca del propio evento y que el
// SDK no manda como tags: aquí salen de los campos del evento, como en la ingesta.
var derivedTags = []string{
	"level", "release", "environment", "dist", "logger", "transaction", "server_name", "url", "user",
	"handled", "mechanism", "browser", "browser.name", "os", "os.name", "os.rooted", "device", "device.family",
	"runtime", "runtime.name", "client_os", "client_os.name", "sentry:user", "sentry:release", "sentry:dist",
}

// toPayload pasa un evento de la API de Sentry a la forma del payload que
// manda un SDK, para leerlo después con event.Parse como en la ingesta.
func toPayload(raw json.RawMessage) ([]byte, error) {
	var e apiEvent
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, err
	}
	id := string(e.EventID)
	if id == "" {
		id = string(e.ID)
	}
	tags := map[string]string{}
	var list []apiTag
	_ = json.Unmarshal(e.Tags, &list)
	for _, t := range list {
		tags[string(t.Key)] = string(t.Value)
	}
	p := map[string]any{
		"event_id":    id,
		"timestamp":   e.DateCreated,
		"platform":    string(e.Platform),
		"level":       tags["level"],
		"logger":      tags["logger"],
		"release":     releaseOf(e.Release, tags["release"]),
		"dist":        firstOf(string(e.Dist), tags["dist"]),
		"environment": tags["environment"],
		"transaction": tags["transaction"],
		"server_name": tags["server_name"],
		"sdk":         e.SDK,
		"contexts":    e.Contexts,
		"user":        e.User,
	}
	own := map[string]string{}
	for k, v := range tags {
		if !slices.Contains(derivedTags, k) {
			own[k] = v
		}
	}
	if len(own) > 0 {
		p["tags"] = own
	}
	message := string(e.Message)
	var entries []apiEntry
	_ = json.Unmarshal(e.Entries, &entries)
	for _, en := range entries {
		switch en.Type {
		case "exception":
			p["exception"] = exception(en.Data)
		case "message":
			var m struct{ Formatted, Message flexString }
			if json.Unmarshal(en.Data, &m) == nil {
				message = firstOf(string(m.Formatted), string(m.Message), message)
			}
		case "request":
			p["request"] = en.Data
		case "breadcrumbs":
			p["breadcrumbs"] = en.Data
		}
	}
	if message != "" {
		p["message"] = message
	}
	return json.Marshal(p)
}

func releaseOf(raw json.RawMessage, tag string) string {
	var r struct {
		Version flexString `json:"version"`
	}
	if json.Unmarshal(raw, &r) == nil && r.Version != "" {
		return string(r.Version)
	}
	var s flexString
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return string(s)
	}
	return tag
}

type apiException struct {
	Type       json.RawMessage `json:"type"`
	Value      json.RawMessage `json:"value"`
	Module     json.RawMessage `json:"module"`
	Mechanism  json.RawMessage `json:"mechanism"`
	Stacktrace *struct {
		Frames []json.RawMessage `json:"frames"`
	} `json:"stacktrace"`
}

type apiFrame struct {
	Filename json.RawMessage `json:"filename"`
	AbsPath  json.RawMessage `json:"absPath"`
	Function json.RawMessage `json:"function"`
	Module   json.RawMessage `json:"module"`
	LineNo   json.RawMessage `json:"lineNo"`
	ColNo    json.RawMessage `json:"colNo"`
	InApp    json.RawMessage `json:"inApp"`
	Context  json.RawMessage `json:"context"`
}

// exception pasa la entrada exception, con los fotogramas en camelCase y el
// código como pares [línea, texto], a la forma del SDK.
func exception(data json.RawMessage) any {
	var w struct {
		Values []apiException `json:"values"`
	}
	if json.Unmarshal(data, &w) != nil {
		return nil
	}
	values := make([]map[string]any, 0, len(w.Values))
	for _, x := range w.Values {
		v := map[string]any{"type": x.Type, "value": x.Value, "module": x.Module, "mechanism": x.Mechanism}
		if x.Stacktrace != nil {
			frames := make([]map[string]any, 0, len(x.Stacktrace.Frames))
			for _, fb := range x.Stacktrace.Frames {
				var f apiFrame
				if json.Unmarshal(fb, &f) != nil {
					continue
				}
				fr := map[string]any{
					"filename": f.Filename, "abs_path": f.AbsPath, "function": f.Function, "module": f.Module,
					"lineno": f.LineNo, "colno": f.ColNo, "in_app": f.InApp,
				}
				var line int
				_ = json.Unmarshal(f.LineNo, &line)
				pre, cur, post := codeContext(f.Context, line)
				if pre != nil {
					fr["pre_context"] = pre
				}
				if cur != nil {
					fr["context_line"] = *cur
				}
				if post != nil {
					fr["post_context"] = post
				}
				frames = append(frames, fr)
			}
			v["stacktrace"] = map[string]any{"frames": frames}
		}
		values = append(values, v)
	}
	return map[string]any{"values": values}
}

// codeContext reparte los pares [línea, texto] en las líneas de antes, la
// del fallo y las de después.
func codeContext(raw json.RawMessage, line int) (pre []string, cur *string, post []string) {
	var pairs [][]json.RawMessage
	if json.Unmarshal(raw, &pairs) != nil || line <= 0 {
		return nil, nil, nil
	}
	for _, p := range pairs {
		if len(p) != 2 {
			continue
		}
		var n int
		var text flexString
		if json.Unmarshal(p[0], &n) != nil {
			continue
		}
		_ = json.Unmarshal(p[1], &text)
		switch {
		case n < line:
			pre = append(pre, string(text))
		case n == line:
			s := string(text)
			cur = &s
		default:
			post = append(post, string(text))
		}
	}
	return pre, cur, post
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
