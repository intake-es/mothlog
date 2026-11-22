// servicios-falsos levanta un Linear y un GitHub falsos para probar a mano
// la sincronización: hacen de sus API, mandan sus webhooks firmados al
// servicio y apuntan en JSON Lines lo que reciben y mandan, en orden.
//
//	go run ./tools/servicios-falsos -linear 127.0.0.1:7801 -github 127.0.0.1:7802 \
//	  -webhooks http://127.0.0.1:8080 -log servicios.jsonl
//
// Lo que haría una persona allí va por /_control/ (ver internal/webhooks/fakes).
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/intake-es/intake-errors/internal/webhooks/fakes"
)

func main() {
	linearAddr := flag.String("linear", "127.0.0.1:7801", "dirección del Linear falso")
	githubAddr := flag.String("github", "127.0.0.1:7802", "dirección del GitHub falso")
	target := flag.String("webhooks", "http://127.0.0.1:8080", "URL base del servicio que recibe los webhooks")
	logPath := flag.String("log", "", "fichero JSON Lines con lo que reciben y mandan; vacío, la salida estándar")
	flag.Parse()

	l := &fakes.Log{Out: os.Stdout}
	if *logPath != "" {
		f, err := os.Create(*logPath)
		if err != nil {
			log.Fatal(err)
		}
		defer func() { _ = f.Close() }()
		l.Out = f
	}
	env := func(name, def string) string {
		if v := os.Getenv(name); v != "" {
			return v
		}
		return def
	}
	base := strings.TrimRight(*target, "/")
	lin := fakes.NewLinear(l, env("LINEAR_API_KEY", "lin_api_falsa"), env("LINEAR_WEBHOOK_SECRET", "secreto-linear"))
	lin.WebhookURL = base + "/webhooks/linear"
	lin.Async()
	gh := fakes.NewGitHub(l, env("GITHUB_WEBHOOK_SECRET", "secreto-github"), map[string]string{env("GITHUB_TOKEN", "ghp_falso"): "bot-de-errores"})
	gh.WebhookURL = base + "/webhooks/github"
	gh.Async()

	serve := func(addr string, api, control http.Handler) {
		mux := http.NewServeMux()
		mux.Handle("/_control/", control)
		mux.Handle("/", api)
		log.Printf("escuchando en %s", addr)
		log.Fatal(http.ListenAndServe(addr, mux))
	}
	go serve(*linearAddr, lin.Handler(), lin.Control())
	serve(*githubAddr, gh.Handler(), gh.Control())
}
