package outbox

import (
	"context"
	"fmt"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

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
		`INSERT INTO outbox_events
			(event_id, aggregate_type, aggregate_id, event_type, event_version, correlation_id,
			 causation_id, payload, occurred_at, status, attempts, next_attempt_at,
			 locked_by, locked_at, published_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)`,
		s.EventID, s.AggregateType, s.AggregateID, s.EventType, s.EventVersion, s.CorrelationID,
		s.CausationID, s.Payload, s.OccurredAt, s.Status, s.Attempts, s.NextAttemptAt,
		s.LockedBy, s.LockedAt, s.PublishedAt)
	if err != nil {
		return fmt.Errorf("insert outbox event: %w", db.Classify(err))
	}
	return nil
}
