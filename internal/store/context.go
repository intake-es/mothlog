package store

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/intake-es/intake-errors/internal/db"
)

// IssueEnvironments son los entornos del issue con sus eventos, también los
// que no se guardaron por el tope o el muestreo, del de más eventos al de menos.
func (s *Store) IssueEnvironments(ctx context.Context, issueID int64) ([]ValueCount, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT environment, CAST(sum(event_count) AS bigint) AS n FROM issue_stats
		WHERE issue_id = $1 AND environment <> ''
		GROUP BY environment ORDER BY n DESC, environment`, issueID)
	if err != nil {
		return nil, fmt.Errorf("entornos del issue: %w", err)
	}
	envs, err := db.CollectRows(rows, func(r db.CollectableRow) (ValueCount, error) {
		var v ValueCount
		return v, r.Scan(&v.Value, &v.Count)
	})
	if err != nil {
		return nil, fmt.Errorf("entornos del issue: %w", err)
	}
	if envs == nil {
		envs = []ValueCount{}
	}
	return envs, nil
}

// IssueEvent busca un evento del issue por su id interno o su event_id.
func (s *Store) IssueEvent(ctx context.Context, issueID int64, ref string) (StoredEvent, error) {
	e, err := s.Event(ctx, ref)
	if err != nil {
		return StoredEvent{}, err
	}
	if e.IssueID != issueID {
		return StoredEvent{}, fmt.Errorf("%w: el evento %s no es del issue %s", ErrNotFound, ref, strconv.FormatInt(issueID, 10))
	}
	return e, nil
}

// IssueContext es lo que lleva el context.md de un issue.
type IssueContext struct {
	IssueDetail
	// Environment es el entorno elegido; vacío, todos.
	Environment string
	// EventChosen: el evento no es el último, sino el que se pidió.
	EventChosen  bool
	Tags         IssueTags
	Environments []ValueCount
	History      []HistoryEntry
}

// IssueContextIn reúne el detalle del issue (con los recuentos de env), el
// evento ref (vacío, el último), sus etiquetas, sus entornos y su historial.
func (s *Store) IssueContextIn(ctx context.Context, id int64, env, ref string, viewer *Actor, now time.Time) (IssueContext, error) {
	d, err := s.IssueDetailIn(ctx, id, env, now)
	if err != nil {
		return IssueContext{}, err
	}
	c := IssueContext{IssueDetail: d, Environment: env}
	if ref != "" {
		e, err := s.IssueEvent(ctx, id, ref)
		if err != nil {
			return IssueContext{}, err
		}
		c.LatestEvent, c.EventChosen = &e, true
	}
	if c.Tags, err = s.IssueTags(ctx, id); err != nil {
		return IssueContext{}, err
	}
	if c.Environments, err = s.IssueEnvironments(ctx, id); err != nil {
		return IssueContext{}, err
	}
	if c.History, err = s.History(ctx, id, viewer); err != nil {
		return IssueContext{}, err
	}
	return c, nil
}
