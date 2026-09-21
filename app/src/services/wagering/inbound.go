package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// consumerName identifies this consumer in the inbox. The inbox deduplicates on the pair of it and
// the message id, so another consumer of the same queue would keep its own record of every
// message.
const consumerName = "wager-transactions"

// Receive handles an operation that arrived as a queue message.
//
// It is the same use case as Submit, wrapped in the inbox: the record of the message, everything
// the operation does and the completion of the handling are one commit. That is what makes
// deleting the message from the queue safe afterwards, and a crash before the deletion harmless:
// the message comes back, the inbox already holds it, and nothing is done twice.
func (s *service) Receive(ctx context.Context, message structs.WagerMessage) (structs.WagerOutcome, error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.Receive",
		observability.String("messageId", message.ID),
		observability.String("providerId", message.Operation.ProviderID),
	)
	started := time.Now()
	outcome, err := s.receiveMessage(ctx, message)
	end(err)
	s.record(ctx, sourceSQS, started, message.Operation.Kind, outcome, err)
	return outcome, err
}

func (s *service) receiveMessage(ctx context.Context, message structs.WagerMessage) (structs.WagerOutcome, error) {
	correlationID := s.correlationID(ctx)

	// Every attempt builds its own transaction and record: an attempt that applied the operation in
	// memory before the database refused it leaves them in a state that no other attempt should see.
	attempt := func() (structs.WagerOutcome, error) {
		candidate, err := entities.NewExternalTransaction(s.newID(), message.Operation, s.now())
		if err != nil {
			return structs.WagerOutcome{}, err
		}
		record, err := entities.NewInboxMessage(consumerName, message.ID, message.Hash, s.now())
		if err != nil {
			return structs.WagerOutcome{}, fmt.Errorf("%w: %w", entities.ErrInvalidTransaction, err)
		}
		return s.receive(ctx, candidate, record, correlationID)
	}

	outcome, err := attempt()
	if errors.Is(err, wageringiface.ErrDuplicate) {
		s.obs.Count(ctx, "wager_concurrent_duplicates_total", observability.NewTag("source", sourceSQS))
		// The same operation arrived under another message id at the same moment and won the race
		// for the unique index. Everything of this attempt, the inbox record included, was rolled
		// back, so trying again is safe, and this time the operation is found and resolved as the
		// replay it is.
		outcome, err = attempt()
	}
	return outcome, err
}

// receive is one attempt at handling the message, in one unit of work.
func (s *service) receive(ctx context.Context, candidate *entities.WagerTransaction, record *entities.InboxMessage, correlationID string) (structs.WagerOutcome, error) {
	var outcome structs.WagerOutcome
	err := s.uow.Atomic(ctx, func(ctx context.Context) error {
		inserted, err := s.inbox.Insert(ctx, record)
		if err != nil {
			return err
		}
		if !inserted {
			duplicate, err := s.duplicate(ctx, record)
			outcome = duplicate
			return err
		}

		handled, err := s.handle(ctx, candidate, correlationID)
		if err != nil {
			return err
		}
		outcome = handled
		if err := record.Complete(s.now()); err != nil {
			return err
		}
		return s.inbox.Complete(ctx, record)
	})
	if err != nil {
		return structs.WagerOutcome{}, err
	}
	return outcome, nil
}

// duplicate decides what a message the inbox already holds is: a redelivery, which is done with,
// or another message wearing the same id, which is refused.
func (s *service) duplicate(ctx context.Context, record *entities.InboxMessage) (structs.WagerOutcome, error) {
	existing, err := s.inbox.Find(ctx, record.ConsumerName(), record.MessageID())
	if err != nil {
		return structs.WagerOutcome{}, err
	}
	if !existing.MatchesPayload(record.Snapshot().PayloadHash) {
		return structs.WagerOutcome{}, fmt.Errorf("%w: message %s", wageringiface.ErrMessageConflict, record.MessageID())
	}
	return structs.WagerOutcome{Duplicate: true}, nil
}

// handle applies the operation, or resolves it against the one it repeats. It looks the operation
// up first, inside the unit of work, because a unique violation here would abort the transaction
// and take the inbox record with it.
func (s *service) handle(ctx context.Context, candidate *entities.WagerTransaction, correlationID string) (structs.WagerOutcome, error) {
	existing, found, err := s.lookup(ctx, candidate)
	if err != nil {
		return structs.WagerOutcome{}, err
	}
	if found {
		return s.replay(existing, candidate)
	}
	if err := s.applyOperation(ctx, candidate, correlationID); err != nil {
		return structs.WagerOutcome{}, err
	}
	return structs.WagerOutcome{Transaction: candidate}, nil
}
