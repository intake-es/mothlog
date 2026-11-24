package store

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"

	"github.com/intake-es/mothlog/internal/db"
	"github.com/intake-es/mothlog/internal/event"
)

// Un evento se guarda como el event.Event normalizado en JSON comprimido con
// zstd en events.data_zstd. Lo que se consulta por SQL va en sus columnas:
// user_label, user_ident, organization_id y summary. Los eventos de antes
// siguen en events.data (jsonb) hasta que ConvertLegacyEvents los pasa.

// Los diccionarios con que se comprimen los eventos, por su id, que zstd
// escribe en la cabecera de cada evento. El 1 son los eventos de
// testdata/envelopes tal como se guardan. Para cambiarlo se añade otro con un
// id nuevo y se apunta currentDict a él; los de antes no se tocan nunca,
// porque sin ellos no se leen los eventos que los usan.
const currentDict = 1

//go:embed zstd-dict-1.jsonl
var eventDict1 []byte

var eventDicts = map[uint32][]byte{1: eventDict1}

// Un solo codificador y un solo decodificador para todo el proceso, creados
// al usarse por primera vez: EncodeAll y DecodeAll se pueden llamar a la vez y
// reutilizan por dentro hasta codecConcurrency estados. Con el diccionario,
// SpeedFastest ocupa unos 1,7 MB por estado y SpeedDefault el doble, para
// comprimir lo mismo.
const codecConcurrency = 2

var (
	zstdEncoder = sync.OnceValue(func() *zstd.Encoder {
		e, err := zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.SpeedFastest),
			zstd.WithEncoderConcurrency(codecConcurrency),
			zstd.WithWindowSize(1<<20),
			zstd.WithLowerEncoderMem(true),
			zstd.WithEncoderDictRaw(currentDict, eventDicts[currentDict]))
		if err != nil {
			panic(err)
		}
		return e
	})
	zstdDecoder = sync.OnceValue(func() *zstd.Decoder {
		opts := []zstd.DOption{
			zstd.WithDecoderConcurrency(codecConcurrency),
			zstd.WithDecoderLowmem(true),
			zstd.WithDecoderMaxMemory(64 << 20),
		}
		for id, d := range eventDicts {
			opts = append(opts, zstd.WithDecoderDictRaw(id, d))
		}
		d, err := zstd.NewReader(nil, opts...)
		if err != nil {
			panic(err)
		}
		return d
	})
)

// EncodedEvent es un evento listo para guardar: el JSON comprimido y lo que
// sale a columnas propias.
type EncodedEvent struct {
	Data []byte
	// UserLabel es el usuario que se enseña: su id, email, username o IP, el
	// primero que tenga, tal cual.
	UserLabel string
	// UserIdent es el usuario como lo cuenta el resumen, igual que UserIdent.
	UserIdent      string
	OrganizationID string
	// Summary es «Tipo: valor» de la excepción lanzada, o el mensaje si no hay excepción.
	Summary string
}

// EncodeEvent prepara ev para guardarlo. No cambia ev.
func EncodeEvent(ev *event.Event) (EncodedEvent, error) {
	stored := *ev
	stored.Breadcrumbs = canonicalRaw(ev.Breadcrumbs)
	stored.Contexts = canonicalRaw(ev.Contexts)
	stored.DebugMeta = canonicalRaw(ev.DebugMeta)
	b, err := json.Marshal(&stored)
	if err != nil {
		return EncodedEvent{}, fmt.Errorf("serialize event: %w", err)
	}
	e := EncodedEvent{
		Data:           zstdEncoder().EncodeAll(b, make([]byte, 0, len(b)/4)),
		UserLabel:      UserLabel(ev.User),
		UserIdent:      UserIdent(ev.User),
		OrganizationID: ev.Tags["organization_id"],
		Summary:        EventSummaryLine(ev),
	}
	return e, nil
}

func canonicalRaw(b json.RawMessage) json.RawMessage {
	if len(b) == 0 {
		return b
	}
	c, err := canonicalJSON(b)
	if err != nil {
		return b
	}
	return c
}

// DecodeEvent lee un evento guardado con EncodeEvent.
func DecodeEvent(data []byte) (event.Event, error) {
	var ev event.Event
	b, err := zstdDecoder().DecodeAll(data, nil)
	if err != nil {
		return ev, fmt.Errorf("decompress event: %w", err)
	}
	if err := json.Unmarshal(b, &ev); err != nil {
		return ev, err
	}
	return ev, nil
}

// decodeStored lee el evento de una fila en cualquiera de los dos formatos.
func decodeStored(compressed, legacy []byte) (event.Event, error) {
	if compressed != nil {
		return DecodeEvent(compressed)
	}
	var ev event.Event
	if legacy == nil {
		return ev, errors.New("event without data")
	}
	err := json.Unmarshal(legacy, &ev)
	return ev, err
}

// UserLabel es el primero que haya de id, email, username o IP, tal cual.
func UserLabel(u *event.User) string {
	if u == nil {
		return ""
	}
	for _, v := range []string{u.ID, u.Email, u.Username, u.IPAddress} {
		if v != "" {
			return v
		}
	}
	return ""
}

// EventSummaryLine es el resumen de una línea de la lista de eventos.
func EventSummaryLine(ev *event.Event) string {
	if n := len(ev.Exceptions); n > 0 {
		last := ev.Exceptions[n-1]
		var parts []string
		for _, s := range []string{last.Type, last.Value} {
			if s != "" {
				parts = append(parts, s)
			}
		}
		return strings.Join(parts, ": ")
	}
	return ev.Message
}

type execQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (db.Result, error)
	QueryRow(ctx context.Context, sql string, args ...any) db.Row
}

// NewEvent es una fila de events por guardar.
type NewEvent struct {
	IssueID    int64
	ProjectID  int64
	Event      *event.Event
	ReceivedAt time.Time
	Encoded    EncodedEvent
}

// InsertEvent guarda el evento ya codificado. Devuelve su id, o 0 si ese
// event_id ya estaba en el proyecto.
func InsertEvent(ctx context.Context, q execQuerier, e NewEvent) (int64, error) {
	ev, enc := e.Event, e.Encoded
	var id int64
	err := q.QueryRow(ctx, `
		INSERT INTO events (issue_id, project_id, event_id, occurred_at, received_at, release, environment,
			data_zstd, user_label, user_ident, organization_id, summary)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), NULLIF($7, ''), $8, NULLIF($9, ''), NULLIF($10, ''), NULLIF($11, ''), NULLIF($12, ''))
		ON CONFLICT (project_id, event_id) DO NOTHING
		RETURNING id`,
		e.IssueID, e.ProjectID, ev.ID, ev.Timestamp, e.ReceivedAt, ev.Release, ev.Environment,
		enc.Data, enc.UserLabel, enc.UserIdent, enc.OrganizationID, enc.Summary).Scan(&id)
	if errors.Is(err, db.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("save event: %w", err)
	}
	return id, nil
}

// InsertEventsQuery es la sentencia que guarda varios eventos ya codificados,
// en su orden, para mandarla en un db.Batch. Los que ya estaban en su
// proyecto no se guardan ni cuentan en las filas afectadas.
func InsertEventsQuery(d db.Dialect, es []NewEvent) (string, []any) {
	n := len(es)
	issues, projects := make([]int64, n), make([]int64, n)
	ids, releases, envs := make([]string, n), make([]string, n), make([]string, n)
	occurred, received := make([]time.Time, n), make([]time.Time, n)
	data := make([][]byte, n)
	labels, idents, orgs, summaries := make([]string, n), make([]string, n), make([]string, n), make([]string, n)
	for i, e := range es {
		ev, enc := e.Event, e.Encoded
		issues[i], projects[i], ids[i] = e.IssueID, e.ProjectID, ev.ID
		occurred[i], received[i] = ev.Timestamp, e.ReceivedAt
		releases[i], envs[i] = ev.Release, ev.Environment
		data[i] = enc.Data
		labels[i], idents[i], orgs[i], summaries[i] = enc.UserLabel, enc.UserIdent, enc.OrganizationID, enc.Summary
	}
	return `
		INSERT INTO events (issue_id, project_id, event_id, occurred_at, received_at, release, environment,
			data_zstd, user_label, user_ident, organization_id, summary)
		SELECT issue_id, project_id, event_id, occurred_at, received_at, NULLIF(release, ''), NULLIF(environment, ''),
			data_zstd, NULLIF(user_label, ''), NULLIF(user_ident, ''), NULLIF(organization_id, ''), NULLIF(summary, '')
		FROM ` + d.UnnestNumbered("e", "n", 1, "issue_id bigint", "project_id bigint", "event_id uuid", "occurred_at timestamptz",
			"received_at timestamptz", "release text", "environment text", "data_zstd bytea", "user_label text", "user_ident text",
			"organization_id text", "summary text") + `
		ORDER BY n
		ON CONFLICT (project_id, event_id) DO NOTHING`,
		[]any{issues, projects, ids, occurred, received, releases, envs, data, labels, idents, orgs, summaries}
}
