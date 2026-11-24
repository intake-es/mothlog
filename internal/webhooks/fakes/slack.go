package fakes

import "net/http"

// Slack es un webhook entrante de Slack falso: apunta cada mensaje y responde
// «ok», como el de verdad.
type Slack struct {
	log *Log
}

func NewSlack(log *Log) *Slack { return &Slack{log: log} }

// Handler recibe los POST del webhook en cualquier ruta.
func (s *Slack) Handler() http.Handler {
	return logged("slack", s.log, func(w http.ResponseWriter, r *http.Request, _ []byte) {
		if r.Method != http.MethodPost {
			http.Error(w, "invalid_method", http.StatusMethodNotAllowed)
			return
		}
		_, _ = w.Write([]byte("ok"))
	})
}
