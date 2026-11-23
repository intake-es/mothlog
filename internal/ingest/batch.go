package ingest

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"slices"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/intake-es/intake-errors/internal/event"
	"github.com/intake-es/intake-errors/internal/store"
)

// BatchOptions dicen cómo se juntan en un lote los eventos que llegan a la vez.
type BatchOptions struct {
	// MaxEvents es cuántos eventos lleva un lote como mucho. Los de un mismo
	// envío van siempre en el mismo lote, aunque pasen de aquí.
	MaxEvents int
	// MaxWait es cuánto espera un lote a los envíos que ya se están leyendo
	// antes de escribirse. Sin ninguno a medias, no espera.
	MaxWait time.Duration
}

func DefaultBatchOptions() BatchOptions {
	return BatchOptions{MaxEvents: 100, MaxWait: 2 * time.Millisecond}
}

// batchTimeout es lo más que puede tardar en escribirse un lote, aunque se
// hayan ido los clientes que lo esperan.
const batchTimeout = time.Minute

// pending es un evento ya preparado que espera a su lote.
type pending struct {
	ctx       context.Context
	projectID int64
	ev        *event.Event
	g         Grouping
	data      store.EncodedEvent
	now       time.Time
	lim       Limits

	res Result
	err error
}

type submission struct {
	items []*pending
	done  chan struct{}
}

// batcher escribe los eventos de uno en uno lote a lote: mientras se escribe
// un lote, los que llegan esperan y van juntos en el siguiente. Una sola
// goroutine escribe, y solo mientras hay cola.
type batcher struct {
	in   *Ingester
	mu   sync.Mutex
	opts BatchOptions
	// queue son los envíos que esperan lote; arriving, los que se están
	// leyendo y aún no han llegado a la cola.
	queue    []*submission
	arriving int
	running  bool
	wake     chan struct{}
}

func newBatcher(in *Ingester) *batcher {
	return &batcher{in: in, opts: DefaultBatchOptions(), wake: make(chan struct{}, 1)}
}

// SetBatching cambia el tamaño y la espera de los lotes.
func (in *Ingester) SetBatching(o BatchOptions) {
	def := DefaultBatchOptions()
	if o.MaxEvents <= 0 {
		o.MaxEvents = def.MaxEvents
	}
	if o.MaxWait < 0 {
		o.MaxWait = 0
	}
	in.batch.mu.Lock()
	in.batch.opts = o
	in.batch.mu.Unlock()
}

func (b *batcher) signal() {
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

// announce avisa de que llega un envío; el lote puede esperarlo hasta
// MaxWait. La función que devuelve lo retira si al final no trae eventos.
func (b *batcher) announce() func() {
	b.mu.Lock()
	b.arriving++
	b.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			b.mu.Lock()
			b.arriving--
			b.mu.Unlock()
			b.signal()
		})
	}
}

// submit pone los eventos de un envío en la cola y espera a que su lote se
// confirme o falle.
func (b *batcher) submit(items []*pending, arrived func()) {
	s := &submission{items: items, done: make(chan struct{})}
	b.mu.Lock()
	b.queue = append(b.queue, s)
	start := !b.running
	b.running = true
	b.mu.Unlock()
	if arrived != nil {
		arrived()
	}
	b.signal()
	if start {
		go b.run()
	}
	<-s.done
}

func (b *batcher) run() {
	for {
		batch := b.next()
		if batch == nil {
			return
		}
		var items []*pending
		for _, s := range batch {
			items = append(items, s.items...)
		}
		b.write(items)
		for _, s := range batch {
			close(s.done)
		}
	}
}

// write guarda el lote. Un pánico al guardarlo falla los eventos del lote en
// vez de tumbar el proceso, como pasaba cuando cada evento se guardaba en su
// petición; el cliente los reintenta y los ya guardados salen como repetidos.
func (b *batcher) write(items []*pending) {
	defer func() {
		if r := recover(); r != nil {
			b.in.log.Error("pánico al guardar un lote", "eventos", len(items), "pánico", r, "pila", string(debug.Stack()))
			for _, it := range items {
				it.res, it.err = Result{}, fmt.Errorf("guardar evento: pánico: %v", r)
			}
		}
	}()
	b.in.writeBatch(items)
}

// next saca de la cola el siguiente lote, o nil si no queda nada y la
// goroutine debe acabar.
func (b *batcher) next() []*submission {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.queue) == 0 {
		b.running = false
		return nil
	}
	if b.opts.MaxWait > 0 && b.arriving > 0 && b.queued() < b.opts.MaxEvents {
		deadline := time.NewTimer(b.opts.MaxWait)
		defer deadline.Stop()
	wait:
		for b.arriving > 0 && b.queued() < b.opts.MaxEvents {
			b.mu.Unlock()
			select {
			case <-b.wake:
				b.mu.Lock()
			case <-deadline.C:
				b.mu.Lock()
				break wait
			}
		}
	}
	n, events := 0, 0
	for n < len(b.queue) && (n == 0 || events+len(b.queue[n].items) <= b.opts.MaxEvents) {
		events += len(b.queue[n].items)
		n++
	}
	batch := slices.Clone(b.queue[:n])
	b.queue = slices.Delete(b.queue, 0, n)
	return batch
}

func (b *batcher) queued() int {
	n := 0
	for _, s := range b.queue {
		n += len(s.items)
	}
	return n
}

// errConflict es un evento que otro ha guardado entre la búsqueda de
// repetidos y la escritura del lote.
var errConflict = errors.New("evento guardado a la vez por otro")

// writeBatch guarda el lote en una transacción. Si falla, guarda sus eventos
// de uno en uno, para que el que falla no tumbe a los demás.
func (in *Ingester) writeBatch(items []*pending) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(items[0].ctx), batchTimeout)
	defer cancel()
	err := pgx.BeginFunc(ctx, in.pool, func(tx pgx.Tx) error { return in.saveBatch(ctx, tx, items) })
	if err == nil {
		return
	}
	if len(items) == 1 && !errors.Is(err, errConflict) {
		items[0].res, items[0].err = Result{}, err
		return
	}
	if len(items) > 1 {
		in.log.WarnContext(ctx, "lote fallido, se guarda evento a evento", "eventos", len(items), "err", err)
	}
	for _, it := range items {
		it.res, it.err = in.ingestOne(it.ctx, it)
	}
}

type eventKey struct {
	projectID int64
	id        string
}

type issueKey struct {
	projectID   int64
	fingerprint string
}

type releaseKey struct {
	projectID int64
	version   string
}

// batchIssue es un issue del lote: su estado tras los eventos ya vistos y
// lo que hay que sumarle al acabar.
type batchIssue struct {
	lockedIssue
	// events son los eventos que se suman con el UPDATE del final; el que
	// crea el issue ya va en su INSERT.
	events      int64
	first, last time.Time
	regressedAt *time.Time
	storedAt    *time.Time
}

type regression struct {
	issueID       int64
	from, release string
}

type tagKey struct {
	issueID    int64
	key, value string
}

type tagCount struct {
	count int64
	last  time.Time
}

type statKey struct {
	issueID              int64
	release, environment string
}

type userKey struct {
	issueID int64
	hash    string
}

// saveBatch hace con el lote lo mismo que save evento a evento y en su orden,
// pero bloquea cada issue una vez y escribe los contadores, los eventos, las
// etiquetas y los usuarios de todo el lote de una vez.
func (in *Ingester) saveBatch(ctx context.Context, tx pgx.Tx, items []*pending) error {
	for _, it := range items {
		it.res, it.err = Result{}, nil
	}
	seen, issues, releases, err := readBatch(ctx, tx, items)
	if err != nil {
		return err
	}
	var (
		inserts     []store.NewEvent
		regressions []regression
		order       []int64
		byID        = map[int64]*batchIssue{}
		stats       = map[statKey]*store.TrackedEvents{}
		statOrder   []statKey
		tags        = map[tagKey]*tagCount{}
		users       = map[userKey]bool{}
		hashKey     []byte
	)
	for _, it := range items {
		ev := it.ev
		ek := eventKey{it.projectID, ev.ID}
		if seen[ek] {
			it.res = Result{Duplicate: true}
			continue
		}
		hour := it.now.Truncate(time.Hour)
		newRelease := false
		if ev.Release != "" {
			rk := releaseKey{it.projectID, ev.Release}
			created, ok := releases[rk]
			if !ok {
				if _, created, err = store.EnsureRelease(ctx, tx, it.projectID, ev.Release, it.now); err != nil {
					return err
				}
			}
			newRelease = created
			releases[rk] = false
		}
		res := Result{ProjectID: it.projectID}
		ik := issueKey{it.projectID, it.g.Fingerprint}
		is := issues[ik]
		if is == nil {
			id, err := createIssue(ctx, tx, it, hour)
			if err != nil {
				return err
			}
			if id != 0 {
				is = &batchIssue{lockedIssue: lockedIssue{id: id, status: "open", storedHour: &hour, storedInHour: 1, lastStoredAt: &it.now}}
				res.Outcome = New
			} else {
				found, err := lockIssue(ctx, tx, it.projectID, it.g.Fingerprint)
				if err != nil {
					return err
				}
				if found == nil {
					return errors.New("el issue desapareció tras un conflicto")
				}
				is = &batchIssue{lockedIssue: *found}
			}
			issues[ik] = is
		}
		if _, ok := byID[is.id]; !ok {
			byID[is.id] = is
			order = append(order, is.id)
		}
		res.IssueID = is.id

		if res.Outcome != New {
			status, regressed, err := transition(is.status, is.resolvedIn, is.fixedIn, ev.Release, newRelease,
				func(a, b string) (int, bool, bool, error) { return store.CompareReleases(ctx, tx, it.projectID, a, b) })
			if err != nil {
				return err
			}
			if regressed {
				res.Outcome = Regressed
				res.ResolvedAt = is.resolvedAt
				regressions = append(regressions, regression{is.id, is.status, ev.Release})
				is.resolvedIn, is.fixedIn, is.resolvedAt = "", "", nil
				is.regressedAt = &it.now
			}
			is.status = status
			stored := 0
			if is.storedHour != nil && is.storedHour.Equal(hour) {
				stored = is.storedInHour
			}
			firstToday := is.lastStoredAt == nil || is.lastStoredAt.Before(it.now.Truncate(24*time.Hour))
			if !regressed {
				switch {
				case stored >= it.lim.PerIssuePerHour:
					res.Dropped = store.DropIssueLimit
				case !firstToday && it.lim.SampleRate < 1 && in.random() >= it.lim.SampleRate:
					res.Dropped = store.DropSampled
				}
			}
			if res.Dropped == "" {
				stored++
				is.lastStoredAt, is.storedAt = &it.now, &it.now
			}
			is.storedHour, is.storedInHour = &hour, stored
			if is.events == 0 {
				is.first, is.last = ev.Timestamp, ev.Timestamp
			}
			is.first, is.last = minTime(is.first, ev.Timestamp), maxTime(is.last, ev.Timestamp)
			is.events++
		}

		sk := statKey{is.id, ev.Release, ev.Environment}
		st := stats[sk]
		if st == nil {
			st = &store.TrackedEvents{ProjectID: it.projectID, IssueID: is.id, Release: ev.Release, Environment: ev.Environment,
				First: ev.Timestamp, Last: ev.Timestamp}
			stats[sk] = st
			statOrder = append(statOrder, sk)
		}
		st.Count++
		st.First, st.Last = minTime(st.First, ev.Timestamp), maxTime(st.Last, ev.Timestamp)
		st.Regressed = st.Regressed || res.Outcome == Regressed

		for _, t := range Tags(ev) {
			tk := tagKey{is.id, t.Key, t.Value}
			if tc := tags[tk]; tc != nil {
				tc.count++
				tc.last = maxTime(tc.last, ev.Timestamp)
			} else {
				tags[tk] = &tagCount{1, ev.Timestamp}
			}
		}
		if ident := store.UserIdent(ev.User); ident != "" {
			if hashKey == nil {
				if hashKey, err = in.userHashKey(ctx, tx); err != nil {
					return err
				}
			}
			users[userKey{is.id, string(store.UserHash(hashKey, ident))}] = true
		}

		if res.Dropped == "" {
			inserts = append(inserts, store.NewEvent{IssueID: is.id, ProjectID: it.projectID, Event: ev, ReceivedAt: it.now, Encoded: it.data})
			seen[ek] = true
		}
		it.res = res
	}

	// Las escrituras van juntas en una sola ida y vuelta a la base.
	writes := &pgx.Batch{}
	if len(inserts) > 0 {
		sql, args := store.InsertEventsQuery(inserts)
		writes.Queue(sql, args...).Exec(func(tag pgconn.CommandTag) error {
			if tag.RowsAffected() != int64(len(inserts)) {
				return errConflict
			}
			return nil
		})
	}
	if sql, args, ok := updateIssuesQuery(order, byID); ok {
		writes.Queue(sql, args...)
	}
	tracked := make([]store.TrackedEvents, len(statOrder))
	for i, k := range statOrder {
		tracked[i] = *stats[k]
	}
	if len(tracked) > 0 {
		sql, args := store.TrackEventsQuery(tracked)
		writes.Queue(sql, args...)
	}
	if sql, args, ok := tagsQuery(tags, users); ok {
		writes.Queue(sql, args...)
	}
	if err := tx.SendBatch(ctx, writes).Close(); err != nil {
		if errors.Is(err, errConflict) {
			return err
		}
		return fmt.Errorf("guardar el lote: %w", err)
	}
	for _, r := range regressions {
		if err := store.RecordRegression(ctx, tx, r.issueID, r.from, r.release); err != nil {
			return err
		}
	}
	for _, it := range items {
		if it.res.Duplicate {
			continue
		}
		if err := in.notifier.IssueChanged(ctx, tx, it.ev, it.res); err != nil {
			return fmt.Errorf("avisar: %w", err)
		}
	}
	return nil
}

// readBatch lee en una sola ida y vuelta lo que el lote necesita saber
// antes de decidir: qué event_id ya están guardados, los issues que ya
// existen, que bloquea por orden de id, y qué releases existen ya.
func readBatch(ctx context.Context, tx pgx.Tx, items []*pending) (map[eventKey]bool, map[issueKey]*batchIssue, map[releaseKey]bool, error) {
	var (
		evProjects, isProjects, relProjects []int64
		evIDs, fps, versions                []string
		issueKeys                           = map[issueKey]bool{}
		releaseKeys                         = map[releaseKey]bool{}
	)
	for _, it := range items {
		evProjects, evIDs = append(evProjects, it.projectID), append(evIDs, it.ev.ID)
		if k := (issueKey{it.projectID, it.g.Fingerprint}); !issueKeys[k] {
			issueKeys[k] = true
			isProjects, fps = append(isProjects, k.projectID), append(fps, k.fingerprint)
		}
		if k := (releaseKey{it.projectID, it.ev.Release}); k.version != "" && !releaseKeys[k] {
			releaseKeys[k] = true
			relProjects, versions = append(relProjects, k.projectID), append(versions, k.version)
		}
	}
	seen := map[eventKey]bool{}
	issues := map[issueKey]*batchIssue{}
	// Una release que ya estaba no es nueva para ningún evento del lote.
	releases := map[releaseKey]bool{}

	reads := &pgx.Batch{}
	var ek eventKey
	reads.Queue(`
		SELECT k.project_id, k.event_id FROM unnest($1::bigint[], $2::text[]) AS k(project_id, event_id)
		JOIN events e ON e.project_id = k.project_id AND e.event_id = k.event_id::uuid`, evProjects, evIDs).
		Query(func(rows pgx.Rows) error {
			_, err := pgx.ForEachRow(rows, []any{&ek.projectID, &ek.id}, func() error {
				seen[ek] = true
				return nil
			})
			return err
		})
	var ik issueKey
	var li lockedIssue
	reads.Queue(`
		SELECT i.id, i.project_id, i.fingerprint, i.status, COALESCE(i.resolved_in_release, ''), COALESCE(i.fixed_in_release, ''),
			i.resolved_at, i.stored_hour, i.stored_in_hour, i.last_stored_at
		FROM issues i
		JOIN unnest($1::bigint[], $2::text[]) AS k(project_id, fingerprint)
			ON i.project_id = k.project_id AND i.fingerprint = k.fingerprint
		ORDER BY i.id
		FOR UPDATE OF i`, isProjects, fps).
		Query(func(rows pgx.Rows) error {
			_, err := pgx.ForEachRow(rows, []any{&li.id, &ik.projectID, &ik.fingerprint, &li.status, &li.resolvedIn, &li.fixedIn,
				&li.resolvedAt, &li.storedHour, &li.storedInHour, &li.lastStoredAt}, func() error {
				issues[ik] = &batchIssue{lockedIssue: li}
				return nil
			})
			return err
		})
	if len(versions) > 0 {
		var rk releaseKey
		reads.Queue(`
			SELECT r.project_id, r.version FROM releases r
			JOIN unnest($1::bigint[], $2::text[]) AS k(project_id, version)
				ON r.project_id = k.project_id AND r.version = k.version`, relProjects, versions).
			Query(func(rows pgx.Rows) error {
				_, err := pgx.ForEachRow(rows, []any{&rk.projectID, &rk.version}, func() error {
					releases[rk] = false
					return nil
				})
				return err
			})
	}
	if err := tx.SendBatch(ctx, reads).Close(); err != nil {
		return nil, nil, nil, fmt.Errorf("leer el lote: %w", err)
	}
	return seen, issues, releases, nil
}

// createIssue abre el issue con el evento; devuelve 0 si otro lo ha abierto
// a la vez.
func createIssue(ctx context.Context, tx pgx.Tx, it *pending, hour time.Time) (int64, error) {
	var id int64
	g, ev := it.g, it.ev
	err := tx.QueryRow(ctx, `
		INSERT INTO issues (project_id, fingerprint, title, culprit, type, level, first_seen, last_seen, event_count,
			stored_hour, stored_in_hour, last_stored_at, first_release)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $7, 1, $8, 1, $9, $10)
		ON CONFLICT (project_id, fingerprint) DO NOTHING
		RETURNING id`,
		it.projectID, g.Fingerprint, g.Title, g.Culprit, g.Type, ev.Level, ev.Timestamp, hour, it.now, nullable(ev.Release)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("crear issue: %w", err)
	}
	return id, nil
}

// updateIssuesQuery suma a cada issue sus eventos del lote y deja su estado
// como lo ha dejado el último.
func updateIssuesQuery(order []int64, byID map[int64]*batchIssue) (string, []any, bool) {
	var (
		ids, counts, storedIn []int64
		first, last, hours    []time.Time
		statuses              []string
		regressed, stored     []bool
		regressedAt, storedAt []time.Time
	)
	for _, id := range order {
		is := byID[id]
		if is.events == 0 {
			continue
		}
		ids = append(ids, id)
		counts = append(counts, is.events)
		first, last = append(first, is.first), append(last, is.last)
		statuses = append(statuses, is.status)
		regressed = append(regressed, is.regressedAt != nil)
		regressedAt = append(regressedAt, deref(is.regressedAt))
		hours = append(hours, *is.storedHour)
		storedIn = append(storedIn, int64(is.storedInHour))
		stored = append(stored, is.storedAt != nil)
		storedAt = append(storedAt, deref(is.storedAt))
	}
	if len(ids) == 0 {
		return "", nil, false
	}
	return `
		UPDATE issues i SET
			event_count = i.event_count + u.n,
			first_seen = LEAST(i.first_seen, u.first_seen),
			last_seen = GREATEST(i.last_seen, u.last_seen),
			status = u.status,
			resolved_in_release = CASE WHEN u.regressed THEN NULL ELSE i.resolved_in_release END,
			fixed_in_release = CASE WHEN u.regressed THEN NULL ELSE i.fixed_in_release END,
			resolved_at = CASE WHEN u.regressed THEN NULL ELSE i.resolved_at END,
			last_regressed_at = CASE WHEN u.regressed THEN u.regressed_at ELSE i.last_regressed_at END,
			stored_hour = u.stored_hour,
			stored_in_hour = u.stored_in_hour,
			last_stored_at = CASE WHEN u.stored THEN u.stored_at ELSE i.last_stored_at END
		FROM unnest($1::bigint[], $2::bigint[], $3::timestamptz[], $4::timestamptz[], $5::text[], $6::boolean[],
			$7::timestamptz[], $8::timestamptz[], $9::bigint[], $10::boolean[], $11::timestamptz[])
			AS u(id, n, first_seen, last_seen, status, regressed, regressed_at, stored_hour, stored_in_hour, stored, stored_at)
		WHERE i.id = u.id`,
		[]any{ids, counts, first, last, statuses, regressed, regressedAt, hours, storedIn, stored, storedAt}, true
}

// tagsQuery es recordTags para todo el lote: suma cada valor de etiqueta una
// vez con lo que suma en el lote y cuenta los usuarios nuevos.
func tagsQuery(tags map[tagKey]*tagCount, users map[userKey]bool) (string, []any, bool) {
	tkeys := slices.SortedFunc(func(yield func(tagKey) bool) {
		for k := range tags {
			if !yield(k) {
				return
			}
		}
	}, func(a, b tagKey) int {
		return cmp.Or(cmp.Compare(a.issueID, b.issueID), cmp.Compare(a.key, b.key), cmp.Compare(a.value, b.value))
	})
	issues, keys, values := make([]int64, len(tkeys)), make([]string, len(tkeys)), make([]string, len(tkeys))
	counts, lasts := make([]int64, len(tkeys)), make([]time.Time, len(tkeys))
	for i, k := range tkeys {
		issues[i], keys[i], values[i] = k.issueID, k.key, k.value
		counts[i], lasts[i] = tags[k].count, tags[k].last
	}
	var userIssues []int64
	var hashes [][]byte
	for k := range users {
		userIssues = append(userIssues, k.issueID)
		hashes = append(hashes, []byte(k.hash))
	}
	if len(tkeys) == 0 && len(hashes) == 0 {
		return "", nil, false
	}
	return `
		WITH t AS (
			INSERT INTO issue_tag_values (issue_id, key, value, count, last_seen)
			SELECT * FROM unnest($1::bigint[], $2::text[], $3::text[], $4::bigint[], $5::timestamptz[])
			ON CONFLICT (issue_id, key, value) DO UPDATE SET
				count = issue_tag_values.count + EXCLUDED.count,
				last_seen = GREATEST(issue_tag_values.last_seen, EXCLUDED.last_seen)
		), u AS (
			INSERT INTO issue_users (issue_id, user_hash)
			SELECT * FROM unnest($6::bigint[], $7::bytea[])
			ON CONFLICT DO NOTHING
			RETURNING issue_id
		)
		UPDATE issues SET user_count = user_count + c.n
		FROM (SELECT issue_id, count(*) AS n FROM u GROUP BY issue_id) c
		WHERE issues.id = c.issue_id`,
		[]any{issues, keys, values, counts, lasts, userIssues, hashes}, true
}

func minTime(a, b time.Time) time.Time {
	if b.Before(a) {
		return b
	}
	return a
}

func maxTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}
