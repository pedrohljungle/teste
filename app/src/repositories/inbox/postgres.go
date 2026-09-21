package inbox

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	inboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/inbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

type postgresRepository struct {
	db  *db.Accessor
	obs *observability.Observer
}

// NewPostgresRepository builds the repository over the shared accessor.
func NewPostgresRepository(accessor *db.Accessor, obs *observability.Observer) inboxiface.Repository {
	return &postgresRepository{db: accessor, obs: obs}
}

// Insert relies on the primary key (consumer, message id) and not on a lookup first, so two
// deliveries of one message that arrive together cannot both be recorded: the second one waits for
// the first to commit and then finds the record there.
func (r *postgresRepository) Insert(ctx context.Context, message *entities.InboxMessage) (bool, error) {
	return observability.Trace(ctx, r.obs, observability.LayerRepository, "inbox.Repository.Insert", func(ctx context.Context) (bool, error) {
		if err := r.db.RequireTransaction(ctx); err != nil {
			return false, err
		}
		s := message.Snapshot()
		// ON CONFLICT DO NOTHING keeps the transaction alive on a duplicate. A plain INSERT would
		// abort it, and the handling of a redelivery could then not even read what it collided with.
		tag, err := r.db.Q(ctx).Exec(ctx,
			`INSERT INTO inbox_messages (consumer_name, message_id, payload_hash, received_at)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (consumer_name, message_id) DO NOTHING`,
			s.ConsumerName, s.MessageID, s.PayloadHash, s.ReceivedAt)
		if err != nil {
			return false, fmt.Errorf("insert inbox message: %w", db.Classify(err))
		}
		return tag.RowsAffected() == 1, nil
	})
}

func (r *postgresRepository) Complete(ctx context.Context, message *entities.InboxMessage) error {
	return observability.TraceErr(ctx, r.obs, observability.LayerRepository, "inbox.Repository.Complete", func(ctx context.Context) error {
		if err := r.db.RequireTransaction(ctx); err != nil {
			return err
		}
		s := message.Snapshot()
		_, err := r.db.Q(ctx).Exec(ctx,
			`UPDATE inbox_messages SET completed_at = $3 WHERE consumer_name = $1 AND message_id = $2`,
			s.ConsumerName, s.MessageID, s.CompletedAt)
		if err != nil {
			return fmt.Errorf("complete inbox message: %w", db.Classify(err))
		}
		return nil
	})
}

func (r *postgresRepository) Find(ctx context.Context, consumerName, messageID string) (*entities.InboxMessage, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "inbox.Repository.Find")
	message, err := r.find(ctx, consumerName, messageID)
	// A message that was never seen is the answer a first delivery expects, not a failure.
	if errors.Is(err, inboxiface.ErrNotFound) {
		end(nil)
	} else {
		end(err)
	}
	return message, err
}

func (r *postgresRepository) find(ctx context.Context, consumerName, messageID string) (*entities.InboxMessage, error) {
	rows, err := r.db.Q(ctx).Query(ctx,
		`SELECT consumer_name, message_id, payload_hash, received_at, completed_at
		 FROM inbox_messages WHERE consumer_name = $1 AND message_id = $2`,
		consumerName, messageID)
	if err != nil {
		return nil, fmt.Errorf("read inbox message: %w", db.Classify(err))
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[entities.InboxMessageSnapshot])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, inboxiface.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read inbox message: %w", db.Classify(err))
	}
	return entities.RehydrateInboxMessage(snapshot)
}
