package store

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
	"github.com/intake-es/intake-errors/internal/event"
)

// ConvertOptions dice cómo convertir los eventos guardados en events.data.
type ConvertOptions struct {
	// Batch es cuántos eventos van en cada transacción.
	Batch int
	// MaxBatchBytes corta el lote antes si sus eventos pasan de ese tamaño.
	MaxBatchBytes int
	// Pause es la espera entre lotes, para no acaparar la base.
	Pause time.Duration
	// Log avisa de los eventos que no se pueden leer; nil, nada.
	Log *slog.Logger
}

func DefaultConvertOptions(log *slog.Logger) ConvertOptions {
	return ConvertOptions{Batch: 200, MaxBatchBytes: 8 << 20, Pause: 20 * time.Millisecond, Log: log}
}

// ConvertLegacyEvents pasa los eventos de events.data (jsonb) al formato
// comprimido con sus columnas, por lotes de una transacción cada uno, hasta
// que no queda ninguno o se cancela ctx. Cada lote bloquea sus filas con SKIP
// LOCKED, así que varios procesos a la vez se reparten las filas sin
// convertir ninguna dos veces, y cortarlo a medias deja convertido solo lo de
// los lotes ya confirmados. Devuelve cuántos ha convertido.
func (s *Store) ConvertLegacyEvents(ctx context.Context, o ConvertOptions) (int64, error) {
	var total int64
	for {
		// Una pasada recorre los eventos por id; si ha convertido alguno, otra
		// más recoge los que tenía bloqueados un proceso que no llegó a acabar.
		var pass int64
		after := int64(0)
		for {
			n, last, err := s.convertBatch(ctx, after, o)
			if err != nil {
				return total + pass, err
			}
			if last == 0 {
				break
			}
			pass += int64(n)
			after = last
			if o.Pause > 0 {
				select {
				case <-ctx.Done():
					return total + pass, ctx.Err()
				case <-time.After(o.Pause):
				}
			}
		}
		total += pass
		if pass == 0 {
			return total, nil
		}
	}
}

// convertBatch convierte los siguientes eventos sin convertir con id mayor
// que after. last es el id del último que ha visto; 0 si no quedaba ninguno.
func (s *Store) convertBatch(ctx context.Context, after int64, o ConvertOptions) (n int, last int64, err error) {
	err = db.BeginFunc(ctx, s.pool, func(tx db.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id, CAST(data AS text) FROM events
			WHERE id > $1 AND data_zstd IS NULL
			ORDER BY id LIMIT $2`+tx.Dialect().ForUpdateSkipLocked(), after, o.Batch)
		if err != nil {
			return err
		}
		var (
			ids                         []int64
			blobs                       [][]byte
			labels, idents, orgs, lines []string
			size                        int
		)
		for rows.Next() {
			var id int64
			var data []byte
			if err := rows.Scan(&id, &data); err != nil {
				rows.Close()
				return err
			}
			enc, err := encodeLegacy(data)
			if err != nil && o.Log != nil {
				o.Log.WarnContext(ctx, "convertir evento: se guarda tal cual", "id", id, "err", err)
			}
			ids, blobs = append(ids, id), append(blobs, enc.Data)
			labels, idents = append(labels, enc.UserLabel), append(idents, enc.UserIdent)
			orgs, lines = append(orgs, enc.OrganizationID), append(lines, enc.Summary)
			last = id
			// Las filas que quedan del lote siguen bloqueadas hasta el commit y
			// las coge el lote siguiente.
			if size += len(data); o.MaxBatchBytes > 0 && size >= o.MaxBatchBytes {
				break
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(ids) == 0 {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE events e SET data_zstd = v.data, user_label = NULLIF(v.label, ''), user_ident = NULLIF(v.ident, ''),
				organization_id = NULLIF(v.org, ''), summary = NULLIF(v.summary, ''), data = NULL
			FROM `+tx.Dialect().Unnest("v", 1, "id bigint", "data bytea", "label text", "ident text", "org text", "summary text")+`
			WHERE e.id = v.id`, ids, blobs, labels, idents, orgs, lines)
		n = len(ids)
		return err
	})
	if err != nil {
		return 0, 0, fmt.Errorf("convertir eventos: %w", err)
	}
	return n, last, nil
}

// encodeLegacy codifica un evento de events.data. Si no se lee como evento
// (tampoco se leía antes), guarda el JSON tal cual, sin sus columnas.
func encodeLegacy(data []byte) (EncodedEvent, error) {
	var ev event.Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return EncodedEvent{Data: zstdEncoder().EncodeAll(data, nil)}, err
	}
	return EncodeEvent(&ev)
}

// RunEventConversion convierte en segundo plano los eventos de events.data.
// Repite cada every mientras quede alguno: en un despliegue gradual, una
// réplica de la versión anterior puede seguir guardando en jsonb, y otra puede
// tener bloqueadas filas que suelta si muere. Acaba cuando no queda ninguno o
// al cancelarse ctx; si falla, reintenta cada retry.
func (s *Store) RunEventConversion(ctx context.Context, log *slog.Logger, retry, every time.Duration) {
	for {
		start := time.Now()
		n, err := s.ConvertLegacyEvents(ctx, DefaultConvertOptions(log))
		if n > 0 {
			log.InfoContext(ctx, "eventos convertidos al formato comprimido", "eventos", n, "duración", time.Since(start).Round(time.Millisecond))
		}
		if ctx.Err() != nil {
			return
		}
		if err == nil && n == 0 {
			left, lerr := s.hasLegacyEvents(ctx)
			if lerr == nil && !left {
				return
			}
			err = lerr
		}
		wait := every
		if err != nil {
			log.ErrorContext(ctx, "convertir eventos", "err", err)
			wait = retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

// hasLegacyEvents dice si queda algún evento en events.data, también los que
// tiene bloqueados otra réplica.
func (s *Store) hasLegacyEvents(ctx context.Context) (bool, error) {
	var left bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM events WHERE data IS NOT NULL)`).Scan(&left)
	if err != nil {
		return false, fmt.Errorf("buscar eventos sin convertir: %w", err)
	}
	return left, nil
}
