// sentry-falso sirve una API de Sentry falsa con testdata/sentry/ para probar
// a mano intake-errors import sentry. Acepta el token de SENTRY_IMPORT_TOKEN.
//
//	SENTRY_IMPORT_TOKEN=… go run ./tools/sentry-falso -addr 127.0.0.1:7803
//
// generar.py vuelve a generar testdata/sentry/ desde testdata/envelopes/.
package main

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/intake-es/intake-errors/internal/sentryimport/fake"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:7803", "dirección en la que escucha")
	dir := flag.String("dir", "testdata/sentry", "directorio con las respuestas")
	throttle := flag.Int("throttle", 0, "cuántas de las primeras peticiones responden 429")
	retryAfter := flag.String("retry-after", "1", "Retry-After de esos 429")
	flag.Parse()
	token := os.Getenv("SENTRY_IMPORT_TOKEN")
	if token == "" {
		log.Fatal("falta SENTRY_IMPORT_TOKEN")
	}
	s, err := fake.New(*dir, token)
	if err != nil {
		log.Fatal(err)
	}
	s.Throttle(*throttle, *retryAfter)
	log.Printf("API de Sentry falsa en http://%s", *addr)
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	log.Fatal(srv.ListenAndServe())
}
