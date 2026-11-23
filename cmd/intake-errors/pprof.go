package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"
)

// servePprof sirve los perfiles de net/http/pprof en addr, aparte del puerto
// público. Solo con PPROF_ADDR, para medir: la prueba de carga lo usa.
func servePprof(addr string, log *slog.Logger) {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	log.Warn("perfiles de pprof abiertos", "addr", addr)
	if err := srv.ListenAndServe(); err != nil {
		log.Error("pprof", "err", err)
	}
}
