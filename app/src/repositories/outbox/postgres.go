package outbox

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// outboxColumns are every column of the snapshot. RowToStructByName needs the returned columns to
// be exactly the fields of the snapshot.
var outboxColumns = strings.Join([]string{
	"event_id", "aggregate_type", "aggregate_id", "event_type", "event_version", "correlation_id",
	"causation_id", "payload", "occurred_at", "status", "attempts", "next_attempt_at",
	"locked_by", "locked_at", "published_at",
}, ", ")

var _ outboxiface.Repository = (*postgresRepository)(nil)

type postgresRepository struct {
	db  *db.Accessor
	obs *observability.Observer
}

// NewPostgresRepository builds the repository over the shared accessor.
func NewPostgresRepository(accessor *db.Accessor, obs *observability.Observer) outboxiface.Repository {
	return &postgresRepository{db: accessor, obs: obs}
}

func (r *postgresRepository) Insert(ctx context.Context, event *entities.OutboxEvent) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "outbox.Repository.Insert")
	defer func() { end(err) }()

	// An event outside a transaction could outlive the change it describes, which is exactly
	// the publication before commit that the outbox exists to prevent.
	if err := r.db.RequireTransaction(ctx); err != nil {
		return err
	}
	s := event.Snapshot()
	_, err = r.db.Q(ctx).Exec(ctx,
		`INSERT INTO outbox_events (`+outboxColumns+`)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		s.EventID, s.AggregateType, s.AggregateID, s.EventType, s.EventVersion, s.CorrelationID,
		s.CausationID, s.Payload, s.OccurredAt, s.Status, s.Attempts, s.NextAttemptAt,
		s.LockedBy, s.LockedAt, s.PublishedAt)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", db.Classify(err))
	}
	return nil
}

// Claim is one UPDATE that picks its rows with SKIP LOCKED, which is what lets any number of
// publishers claim at once without waiting for each other or for a coordinator: a row another
// claim is taking is skipped, not waited for. A row still reserved by a live publisher is not
// due; one whose lease ran out is, and that is the recovery of abandoned work.
//
// An event is also not due while an earlier event of the same aggregate is still PENDING, whether
// it is waiting for a retry or reserved by another publisher. That is what keeps the events of one
// aggregate in order when several publishers run: without it, two of them could take two events of
// the same wallet and send the later one first. The price is that a failing event holds back the
// ones behind it, for as long as its backoff lasts, and only those of its own aggregate.
//
// It commits on its own, before anything is published. The network call that follows must not
// hold a transaction open, and a publisher that dies right after this statement leaves rows that
// come back when the lease expires.
func (r *postgresRepository) Claim(ctx context.Context, publisher string, limit int, lease time.Duration, now time.Time) (events []*entities.OutboxEvent, err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "outbox.Repository.Claim")
	defer func() { end(err) }()

	rows, err := r.db.Q(ctx).Query(ctx,
		`UPDATE outbox_events SET locked_by = $1, locked_at = $2, attempts = attempts + 1
		 WHERE event_id IN (
		     SELECT o.event_id FROM outbox_events o
		     WHERE o.status = 'PENDING'
		       AND o.next_attempt_at <= $2
		       AND (o.locked_at IS NULL OR o.locked_at <= $2 - make_interval(secs => $3::double precision))
		       AND NOT EXISTS (
		           SELECT 1 FROM outbox_events earlier
		           WHERE earlier.aggregate_type = o.aggregate_type
		             AND earlier.aggregate_id = o.aggregate_id
		             AND earlier.status = 'PENDING'
		             AND (earlier.occurred_at, earlier.event_id) < (o.occurred_at, o.event_id))
		     ORDER BY o.occurred_at
		     FOR UPDATE OF o SKIP LOCKED
		     LIMIT $4)
		 RETURNING `+outboxColumns,
		publisher, now, lease.Seconds(), limit)
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", db.Classify(err))
	}
	snapshots, err := pgx.CollectRows(rows, pgx.RowToStructByName[entities.OutboxEventSnapshot])
	if err != nil {
		return nil, fmt.Errorf("claim outbox events: %w", db.Classify(err))
	}

	events = make([]*entities.OutboxEvent, 0, len(snapshots))
	for _, snapshot := range snapshots {
		event, err := entities.RehydrateOutboxEvent(snapshot)
		if err != nil {
			return nil, err
		}
		events = append(events, event)
	}
	// RETURNING does not promise the order of the subquery.
	sort.SliceStable(events, func(i, j int) bool { return events[i].OccurredAt().Before(events[j].OccurredAt()) })
	return events, nil
}

func (r *postgresRepository) Complete(ctx context.Context, event *entities.OutboxEvent) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "outbox.Repository.Complete")
	defer func() { end(err) }()

	_, err = r.db.Q(ctx).Exec(ctx,
		`UPDATE outbox_events
		 SET status = 'PUBLISHED', published_at = $2, locked_by = NULL, locked_at = NULL
		 WHERE event_id = $1 AND status = 'PENDING'`,
		event.ID(), event.PublishedAt())
	if err != nil {
		return fmt.Errorf("complete outbox event: %w", db.Classify(err))
	}
	return nil
}

func (r *postgresRepository) Release(ctx context.Context, event *entities.OutboxEvent, publisher string) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "outbox.Repository.Release")
	defer func() { end(err) }()

	_, err = r.db.Q(ctx).Exec(ctx,
		`UPDATE outbox_events
		 SET next_attempt_at = $2, locked_by = NULL, locked_at = NULL
		 WHERE event_id = $1 AND status = 'PENDING' AND locked_by = $3`,
		event.ID(), event.NextAttemptAt(), publisher)
	if err != nil {
		return fmt.Errorf("release outbox event: %w", db.Classify(err))
	}
	return nil
}
