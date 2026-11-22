package fakes

import (
	"encoding/json"
	"net/http"
	"strconv"
)

// Control son las acciones de una persona en el Linear falso, bajo /_control/:
// GET issues y POST issues/{ref}/state con {"state": "Done", "actor": "Ana"}.
func (l *Linear) Control() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /_control/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, l.Issues())
	})
	mux.HandleFunc("POST /_control/issues/{ref}/state", func(w http.ResponseWriter, r *http.Request) {
		var in struct{ State, Actor string }
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Actor == "" {
			in.Actor = "Persona de Linear"
		}
		err := l.SetState(r.PathValue("ref"), in.State, LinearActor{ID: "user-" + strconv.Itoa(len(in.Actor)), Name: in.Actor})
		l.log.add(Entry{Server: "linear", Dir: "accion", Method: r.Method, Path: r.URL.Path, Body: mustJSON(in), Status: status(err)})
		reply(w, err)
	})
	return mux
}

// Control son las acciones de una persona en el GitHub falso, bajo /_control/repos/{dueño}/{repo}/:
// GET issues, POST issues/{n}/close con {"reason", "login"}, POST issues/{n}/reopen
// y POST merge con {"title", "body", "login"}, que abre y mergea un PR.
func (g *GitHub) Control() http.Handler {
	mux := http.NewServeMux()
	repo := func(r *http.Request) string { return r.PathValue("owner") + "/" + r.PathValue("repo") }
	num := func(r *http.Request) int { n, _ := strconv.Atoi(r.PathValue("n")); return n }
	type action struct{ Reason, Login, Title, Body string }
	decode := func(r *http.Request) action {
		var in action
		_ = json.NewDecoder(r.Body).Decode(&in)
		if in.Login == "" {
			in.Login = "persona-de-github"
		}
		return in
	}
	mux.HandleFunc("GET /_control/repos/{owner}/{repo}/issues", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, g.Issues(repo(r)))
	})
	mux.HandleFunc("POST /_control/repos/{owner}/{repo}/issues/{n}/close", func(w http.ResponseWriter, r *http.Request) {
		in := decode(r)
		err := g.Close(repo(r), num(r), in.Reason, in.Login)
		g.log.add(Entry{Server: "github", Dir: "accion", Method: r.Method, Path: r.URL.Path, Body: mustJSON(in), Status: status(err)})
		reply(w, err)
	})
	mux.HandleFunc("POST /_control/repos/{owner}/{repo}/issues/{n}/reopen", func(w http.ResponseWriter, r *http.Request) {
		in := decode(r)
		err := g.Reopen(repo(r), num(r), in.Login)
		g.log.add(Entry{Server: "github", Dir: "accion", Method: r.Method, Path: r.URL.Path, Body: mustJSON(in), Status: status(err)})
		reply(w, err)
	})
	mux.HandleFunc("POST /_control/repos/{owner}/{repo}/merge", func(w http.ResponseWriter, r *http.Request) {
		in := decode(r)
		n := g.MergePR(repo(r), in.Title, in.Body, in.Login)
		g.log.add(Entry{Server: "github", Dir: "accion", Method: r.Method, Path: r.URL.Path, Body: mustJSON(in), Status: http.StatusOK})
		writeJSON(w, http.StatusOK, map[string]int{"number": n})
	})
	return mux
}

func status(err error) int {
	if err != nil {
		return http.StatusNotFound
	}
	return http.StatusOK
}

func reply(w http.ResponseWriter, err error) {
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
