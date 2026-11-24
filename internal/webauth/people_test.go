package webauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/mothlog/internal/store"
)

// savedPeople hace de la tabla people en memoria.
type savedPeople struct {
	mu   sync.Mutex
	got  []store.Person
	fail bool
	// login es el que devuelve la base; sin él, el que llega.
	login func(store.Person) string
	since map[string]time.Time
	// readFails hace fallar la consulta de si la sesión está revocada.
	readFails bool
}

func (s *savedPeople) SaveIdentity(_ context.Context, p store.Person) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("la base no responde")
	}
	s.got = append(s.got, p)
	login := p.Login
	if s.login != nil {
		login = s.login(p)
	}
	if s.since == nil {
		s.since = map[string]time.Time{}
	}
	if _, ok := s.since[login]; !ok {
		s.since[login] = time.Unix(0, 0)
	}
	return login, nil
}

func (s *savedPeople) SessionsValidSince(_ context.Context, login string) (time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.readFails {
		return time.Time{}, errors.New("la base no responde")
	}
	t, ok := s.since[login]
	if !ok {
		return time.Time{}, store.ErrNotFound
	}
	return t, nil
}

func (s *savedPeople) RevokeSessions(_ context.Context, login string, at time.Time) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.since[login]; !ok {
		return "", store.ErrNotFound
	}
	s.since[login] = at
	return login, nil
}

func (s *savedPeople) RenamePerson(_ context.Context, from, base string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.since[from]
	if !ok {
		return "", store.ErrNotFound
	}
	delete(s.since, from)
	s.since[base] = t
	return base, nil
}

func TestLoginSavesPerson(t *testing.T) {
	gh := newFakeGitHub(t)
	h := newHarness(t, githubConfig(gh))
	people := &savedPeople{}
	h.auth.SavePeople(people)

	if res := h.login(t, "ana", "/"); res.StatusCode != http.StatusFound || cookieNamed(res, sessionCookie) == nil {
		t.Fatalf("login = %d", res.StatusCode)
	}
	// Quien no es miembro activo no se guarda.
	for _, code := range []string{"pepe", "eve"} {
		h.login(t, code, "/")
	}
	if len(people.got) != 1 || people.got[0].Login != "ana" || people.got[0].Name != "Ana" ||
		people.got[0].AvatarURL == nil || *people.got[0].AvatarURL != "https://avatars.example.com/ana" {
		t.Fatalf("guardadas = %+v", people.got)
	}

	// Si no se puede guardar, no hay sesión, también con solo GitHub: sin
	// fila no se podría revocar.
	people.fail = true
	if res := h.login(t, "ana", "/"); cookieNamed(res, sessionCookie) != nil || res.Header.Get("Location") != "/login?error=save" {
		t.Errorf("sin poder guardar a la persona = %d %s %v", res.StatusCode, res.Header.Get("Location"), res.Cookies())
	}
	people.fail = false

	// Sin tabla people no se entra.
	bare, err := New(githubConfig(gh), quiet)
	if err != nil {
		t.Fatal(err)
	}
	h.auth = bare
	h.mux = http.NewServeMux()
	bare.Register(h.mux)
	if res := h.login(t, "ana", "/"); cookieNamed(res, sessionCookie) != nil {
		t.Errorf("sin people deja entrar: %v", res.Cookies())
	}

	dev := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true})
	devPeople := &savedPeople{}
	dev.auth.SavePeople(devPeople)
	dev.devLogin(t, "")
	if len(devPeople.got) != 1 || devPeople.got[0].Login != "dev" || devPeople.got[0].AvatarURL != nil {
		t.Errorf("dev = %+v", devPeople.got)
	}
}
