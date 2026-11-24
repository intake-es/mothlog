// Package sentryimport trae de la API de Sentry los proyectos, los issues con
// sus últimos eventos y las releases con sus despliegues.
package sentryimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	maxRetries = 5
	// maxWait es lo más que se espera de una vez por el límite de la API.
	maxWait  = 5 * time.Minute
	maxBody  = 32 << 20
	pageSize = 100
)

// APIError es una respuesta de la API que no se ha podido usar. No lleva el
// token: va en la cabecera y no sale nunca en la URL ni en el mensaje.
type APIError struct {
	Path   string
	Status int
	Detail string
}

func (e *APIError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("GET %s: %s", e.Path, e.Detail)
	}
	if e.Detail == "" {
		return fmt.Sprintf("GET %s: %d", e.Path, e.Status)
	}
	return fmt.Sprintf("GET %s: %d %s", e.Path, e.Status, e.Detail)
}

// Client pide a la API de Sentry con el token de la organización, pagina con
// la cabecera Link y espera lo que pidan las cabeceras de límite.
type Client struct {
	base  *url.URL
	token string
	http  *http.Client
	// Sleep espera d o hasta que se cancele ctx; los tests la cambian.
	Sleep func(ctx context.Context, d time.Duration) error
	// Now da la hora para X-Sentry-Rate-Limit-Reset.
	Now func() time.Time
	// Waited, si no es nil, se llama antes de cada espera por el límite.
	Waited func(d time.Duration, why string)
}

// NewClient prepara el cliente para el servidor base (https://sentry.io o
// uno propio).
func NewClient(base, token string, hc *http.Client) (*Client, error) {
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(base), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, fmt.Errorf("invalid --url: %q", base)
	}
	if token == "" {
		return nil, errors.New("SENTRY_IMPORT_TOKEN is missing")
	}
	if hc == nil {
		hc = &http.Client{Timeout: time.Minute}
	}
	u.RawQuery, u.Fragment = "", ""
	return &Client{base: u, token: token, http: hc, Sleep: sleep, Now: time.Now}, nil
}

// Server es el servidor tal como se guarda en el origen de lo importado.
func (c *Client) Server() string { return c.base.String() }

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// path monta /api/0/… con cada parte escapada.
func path(parts ...string) string {
	var b strings.Builder
	b.WriteString("/api/0")
	for _, p := range parts {
		b.WriteString("/")
		b.WriteString(url.PathEscape(p))
	}
	b.WriteString("/")
	return b.String()
}

func (c *Client) resolve(p string, q url.Values) *url.URL {
	u := *c.base
	u.Path = strings.TrimRight(c.base.Path, "/") + p
	u.RawQuery = q.Encode()
	return &u
}

// List pide todas las páginas de una lista y llama a each con cada elemento,
// hasta que no queden o each devuelva false.
func (c *Client) List(ctx context.Context, p string, q url.Values, each func(json.RawMessage) (bool, error)) error {
	next := c.resolve(p, q)
	for next != nil {
		var items []json.RawMessage
		link, err := c.get(ctx, next, &items)
		if err != nil {
			return err
		}
		for _, it := range items {
			more, err := each(it)
			if err != nil || !more {
				return err
			}
		}
		if next, err = c.nextPage(link); err != nil {
			return err
		}
	}
	return nil
}

// nextPage lee el enlace rel="next" de Link. Solo se sigue si trae
// resultados y va al mismo servidor, para no mandar el token a otro sitio.
func (c *Client) nextPage(link string) (*url.URL, error) {
	for part := range strings.SplitSeq(link, ",") {
		target, params, ok := strings.Cut(strings.TrimSpace(part), ";")
		if !ok {
			continue
		}
		attrs := map[string]string{}
		for kv := range strings.SplitSeq(params, ";") {
			k, v, _ := strings.Cut(strings.TrimSpace(kv), "=")
			attrs[strings.ToLower(k)] = strings.Trim(v, `"`)
		}
		if attrs["rel"] != "next" || attrs["results"] != "true" {
			continue
		}
		u, err := url.Parse(strings.Trim(strings.TrimSpace(target), "<>"))
		if err != nil {
			return nil, fmt.Errorf("invalid next page link: %w", err)
		}
		u = c.base.ResolveReference(u)
		if u.Scheme != c.base.Scheme || u.Host != c.base.Host {
			return nil, fmt.Errorf("the next page goes to another server (%s): not followed", u.Host)
		}
		return u, nil
	}
	return nil, nil
}

// Get pide un objeto.
func (c *Client) Get(ctx context.Context, p string, q url.Values, out any) error {
	_, err := c.get(ctx, c.resolve(p, q), out)
	return err
}

func (c *Client) get(ctx context.Context, u *url.URL, out any) (link string, err error) {
	shown := u.Path
	if u.RawQuery != "" {
		shown += "?" + u.RawQuery
	}
	shown = c.redact(shown)
	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
		if err != nil {
			return "", &APIError{Path: shown, Detail: c.redact(err.Error())}
		}
		req.Header.Set("Authorization", "Bearer "+c.token)
		req.Header.Set("Accept", "application/json")
		resp, err := c.http.Do(req)
		if err != nil {
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if attempt < maxRetries {
				if err := c.wait(ctx, backoff(attempt), "the API is not responding"); err != nil {
					return "", err
				}
				continue
			}
			return "", &APIError{Path: shown, Detail: c.redact(err.Error())}
		}
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, maxBody))
		_ = resp.Body.Close()
		switch {
		case resp.StatusCode == http.StatusTooManyRequests && attempt < maxRetries:
			d := retryAfter(resp.Header, c.Now())
			if d <= 0 {
				d = backoff(attempt)
			}
			if err := c.wait(ctx, d, "429 from the API"); err != nil {
				return "", err
			}
			continue
		case resp.StatusCode >= 500 && attempt < maxRetries:
			if err := c.wait(ctx, backoff(attempt), fmt.Sprintf("%d from the API", resp.StatusCode)); err != nil {
				return "", err
			}
			continue
		case resp.StatusCode != http.StatusOK:
			return "", &APIError{Path: shown, Status: resp.StatusCode, Detail: c.redact(detail(body))}
		case rerr != nil:
			return "", &APIError{Path: shown, Detail: c.redact(rerr.Error())}
		}
		if err := json.Unmarshal(body, out); err != nil {
			return "", &APIError{Path: shown, Status: resp.StatusCode, Detail: "unreadable response: " + c.redact(err.Error())}
		}
		// Sin peticiones restantes, se espera a que el límite vuelva a empezar.
		if resp.Header.Get("X-Sentry-Rate-Limit-Remaining") == "0" {
			if d := resetIn(resp.Header, c.Now()); d > 0 {
				if err := c.wait(ctx, d, "API rate limit exhausted"); err != nil {
					return "", err
				}
			}
		}
		return resp.Header.Get("Link"), nil
	}
}

func (c *Client) wait(ctx context.Context, d time.Duration, why string) error {
	d = min(d, maxWait)
	if c.Waited != nil {
		c.Waited(d, why)
	}
	return c.Sleep(ctx, d)
}

func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[token]")
}

func backoff(attempt int) time.Duration { return time.Second << min(attempt, 5) }

// retryAfter lee Retry-After en segundos o como fecha y, si falta, el
// reinicio del límite.
func retryAfter(h http.Header, now time.Time) time.Duration {
	if v := strings.TrimSpace(h.Get("Retry-After")); v != "" {
		if s, err := strconv.ParseFloat(v, 64); err == nil && s >= 0 {
			return time.Duration(s * float64(time.Second))
		}
		if t, err := http.ParseTime(v); err == nil {
			return t.Sub(now)
		}
	}
	return resetIn(h, now)
}

// resetIn lee X-Sentry-Rate-Limit-Reset, en segundos Unix.
func resetIn(h http.Header, now time.Time) time.Duration {
	v, err := strconv.ParseFloat(strings.TrimSpace(h.Get("X-Sentry-Rate-Limit-Reset")), 64)
	if err != nil || v <= 0 {
		return 0
	}
	return time.Unix(0, int64(v*1e9)).Sub(now)
}

// detail saca el mensaje de error de la respuesta, corto.
func detail(body []byte) string {
	var e struct {
		Detail string `json:"detail"`
	}
	s := strings.TrimSpace(string(body))
	if json.Unmarshal(body, &e) == nil && e.Detail != "" {
		s = e.Detail
	}
	if r := []rune(s); len(r) > 200 {
		s = string(r[:200]) + "…"
	}
	return s
}
