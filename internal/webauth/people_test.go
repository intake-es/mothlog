package webauth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/intake-es/intake-errors/internal/store"
)

type savedPeople struct {
	mu   sync.Mutex
	got  []store.Person
	fail bool
	// login es el que devuelve la base; sin él, el que llega.
	login func(store.Person) string
}

func (s *savedPeople) SaveIdentity(_ context.Context, p store.Person) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fail {
		return "", errors.New("la base no responde")
	}
	s.got = append(s.got, p)
	if s.login != nil {
		return s.login(p), nil
	}
	return p.Login, nil
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

	// Si no se puede guardar, se entra igual.
	people.fail = true
	if res := h.login(t, "ana", "/"); cookieNamed(res, sessionCookie) == nil {
		t.Errorf("sin poder guardar a la persona no deja entrar: %d", res.StatusCode)
	}

	dev := newHarness(t, Config{ListenAddr: "127.0.0.1:8080", DevLogin: true})
	devPeople := &savedPeople{}
	dev.auth.SavePeople(devPeople)
	dev.do(t, http.MethodGet, "/auth/dev/login", nil, nil)
	if len(devPeople.got) != 1 || devPeople.got[0].Login != "dev" || devPeople.got[0].AvatarURL != nil {
		t.Errorf("dev = %+v", devPeople.got)
	}
}
