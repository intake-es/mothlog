package alert

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/intake-es/intake-errors/internal/store"
)

// «Probar» manda sin guardar nada: ni enlaces que aplicar después ni
// consultas por el issue, que no existe.
func TestTestPendingSavesNothing(t *testing.T) {
	var mu sync.Mutex
	var reqs []string
	gh := serve(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, r.Method+" "+r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]any{"number": 7, "html_url": "https://github.com/o/r/issues/7"})
	}))
	e := newEnv(t, func(o *Options) { o.GitHubAPIURL = gh })
	project, err := e.db.ProjectByID(context.Background(), e.project)
	if err != nil {
		t.Fatal(err)
	}
	for _, act := range []store.RuleAction{
		{ID: "gh", Type: store.ActionGitHub, Repo: "o/r", Token: "ghp_prueba"},
		{ID: "lin", Type: store.ActionLinear},
	} {
		r := store.Rule{ID: 1, ProjectID: e.project, Name: "prueba", Actions: []store.RuleAction{act}}
		p := testPending(project, r, time.Now())
		if !p.test {
			t.Fatalf("%s: el aviso de prueba no va marcado como prueba", act.Type)
		}
		snd, err := e.alerter.actionSender(act)
		if err != nil {
			t.Fatal(err)
		}
		if err := snd.send(context.Background(), p); err != nil {
			t.Fatalf("%s: %v", act.Type, err)
		}
		if len(p.after) != 0 {
			t.Fatalf("%s: la prueba deja %d escrituras pendientes", act.Type, len(p.after))
		}
	}
	if len(reqs) != 1 || reqs[0] != "POST /repos/o/r/issues" {
		t.Fatalf("GitHub ha recibido %q", reqs)
	}
	if e.linear.created() != 1 {
		t.Fatalf("issues de Linear = %d", e.linear.created())
	}
}
