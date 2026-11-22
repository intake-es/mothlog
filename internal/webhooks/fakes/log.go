// Package fakes son un Linear y un GitHub falsos para los tests y las pruebas
// a mano: hacen de su API, mandan sus webhooks firmados con la forma de los de
// verdad y apuntan en orden lo que reciben y lo que mandan.
package fakes

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sync"
	"time"
)

// Entry es una petición recibida o un webhook mandado.
type Entry struct {
	Seq    int             `json:"seq"`
	At     time.Time       `json:"at"`
	Server string          `json:"server"`
	Dir    string          `json:"dir"` // recibe o manda
	Method string          `json:"method"`
	Path   string          `json:"path"`
	Event  string          `json:"event,omitempty"`
	Body   json.RawMessage `json:"body,omitempty"`
	Status int             `json:"status"`
	Reply  json.RawMessage `json:"reply,omitempty"`
}

// Log guarda las entradas de los dos servidores en un solo orden y, si
// tiene Out, las escribe ahí en JSON Lines.
type Log struct {
	mu      sync.Mutex
	entries []Entry
	Out     io.Writer
}

func (l *Log) add(e Entry) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e.Seq = len(l.entries) + 1
	e.At = time.Now().UTC()
	l.entries = append(l.entries, e)
	if l.Out != nil {
		b, _ := json.Marshal(e)
		_, _ = l.Out.Write(append(b, '\n'))
	}
}

func (l *Log) Entries() []Entry {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Entry(nil), l.entries...)
}

func rawJSON(b []byte) json.RawMessage {
	if json.Valid(b) {
		return json.RawMessage(bytes.Clone(b))
	}
	s, _ := json.Marshal(string(b))
	return s
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// hook es un webhook por mandar.
type hook struct {
	event   string
	headers map[string]string
	body    []byte
}

// outbox manda los webhooks en orden: en segundo plano con Async o, si no,
// al llamar a Flush.
type outbox struct {
	server string
	log    *Log
	client *http.Client
	url    func() string

	mu      sync.Mutex
	pending []hook
	async   bool
	kick    chan struct{}
}

func newOutbox(server string, log *Log, url func() string) *outbox {
	return &outbox{server: server, log: log, client: &http.Client{Timeout: 10 * time.Second}, url: url, kick: make(chan struct{}, 1)}
}

func (o *outbox) push(h hook) {
	o.mu.Lock()
	o.pending = append(o.pending, h)
	async := o.async
	o.mu.Unlock()
	if async {
		select {
		case o.kick <- struct{}{}:
		default:
		}
	}
}

// startAsync manda los webhooks en segundo plano, como los servicios de verdad.
func (o *outbox) startAsync() {
	o.mu.Lock()
	o.async = true
	o.mu.Unlock()
	go func() {
		for range o.kick {
			time.Sleep(200 * time.Millisecond)
			_, _ = o.Flush()
		}
	}()
	o.kick <- struct{}{}
}

// Flush manda los webhooks pendientes, en orden, y devuelve los códigos de respuesta.
func (o *outbox) Flush() ([]int, error) {
	var codes []int
	for {
		o.mu.Lock()
		if len(o.pending) == 0 {
			o.mu.Unlock()
			return codes, nil
		}
		h := o.pending[0]
		o.pending = o.pending[1:]
		o.mu.Unlock()
		code, err := o.send(h)
		if err != nil {
			return codes, err
		}
		codes = append(codes, code)
	}
}

// reverse da la vuelta a los webhooks pendientes, para mandarlos desordenados.
func (o *outbox) reverse() {
	o.mu.Lock()
	defer o.mu.Unlock()
	slices.Reverse(o.pending)
}

func (o *outbox) Pending() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.pending)
}

func (o *outbox) send(h hook) (int, error) {
	target := o.url()
	if target == "" {
		return 0, fmt.Errorf("%s: sin URL de webhook", o.server)
	}
	req, err := http.NewRequest(http.MethodPost, target, bytes.NewReader(h.body))
	if err != nil {
		return 0, err
	}
	for k, v := range h.headers {
		req.Header.Set(k, v)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		o.log.add(Entry{Server: o.server, Dir: "manda", Method: http.MethodPost, Path: target, Event: h.event, Body: rawJSON(h.body)})
		return 0, err
	}
	reply, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
	o.log.add(Entry{Server: o.server, Dir: "manda", Method: http.MethodPost, Path: target, Event: h.event,
		Body: rawJSON(h.body), Status: resp.StatusCode, Reply: rawJSON(bytes.TrimSpace(reply))})
	return resp.StatusCode, nil
}

// recorder guarda la respuesta para apuntarla.
type recorder struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	r.buf.Write(b)
	return r.ResponseWriter.Write(b)
}

// logged apunta cada petición que recibe el servidor con su respuesta.
func logged(server string, log *Log, next func(w http.ResponseWriter, r *http.Request, body []byte)) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		rec := &recorder{ResponseWriter: w}
		next(rec, r, body)
		path := r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}
		log.add(Entry{Server: server, Dir: "recibe", Method: r.Method, Path: path, Body: rawJSON(body),
			Status: rec.status, Reply: rawJSON(bytes.TrimSpace(rec.buf.Bytes()))})
	})
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
