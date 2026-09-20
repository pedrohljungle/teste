package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

var _ wageringiface.Service = (*service)(nil)

type service struct {
	uow      persistenceiface.UnitOfWork
	wallets  walletiface.Repository
	wagering wageringiface.Repository
	outbox   outboxiface.Repository
	obs      *observability.Observer

	// now and newID are fields so a test can fix the clock and the identities.
	now   func() time.Time
	newID func() uuid.UUID
}

// NewService builds the wagering service.
func NewService(
	uow persistenceiface.UnitOfWork,
	wallets walletiface.Repository,
	wagering wageringiface.Repository,
	outbox outboxiface.Repository,
	obs *observability.Observer,
) wageringiface.Service {
	return &service{
		uow:      uow,
		wallets:  wallets,
		wagering: wagering,
		outbox:   outbox,
		obs:      obs,
		now:      time.Now,
		newID:    func() uuid.UUID { return uuid.Must(uuid.NewV7()) },
	}
}

// Submit applies an operation once, and answers a repeat of it with the stored outcome.
//
// Idempotency has two layers and only the second one is a guarantee. Looking the operation up
// first is a shortcut that spares a repeat from taking the wallet lock. What makes it safe when
// two copies arrive together is the unique indexes on the transaction: both pass the lookup, one
// wins the insert, and the other is refused by the database and resolves as a replay.
func (s *service) Submit(ctx context.Context, operation entities.ExternalOperation) (outcome structs.WagerOutcome, err error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.Submit",
		observability.String("providerId", operation.ProviderID),
		observability.String("walletId", operation.WalletID),
	)
	defer func() { end(err) }()

	candidate, err := entities.NewExternalTransaction(s.newID(), operation, s.now())
	if err != nil {
		return structs.WagerOutcome{}, err
	}
	if candidate.Kind().IsReversal() {
		return structs.WagerOutcome{}, wageringiface.ErrKindNotSupported
	}

	if existing, found, err := s.lookup(ctx, candidate); err != nil {
		return structs.WagerOutcome{}, err
	} else if found {
		return s.replay(existing, candidate)
	}

	correlationID := s.correlationID(ctx)
	stored, err := s.apply(ctx, candidate, correlationID)
	if errors.Is(err, wageringiface.ErrDuplicate) {
		// Another copy of the operation committed between the lookup and the insert. The
		// database refused this one, which is exactly its job: resolve it as the replay it is.
		existing, found, lookupErr := s.lookup(ctx, candidate)
		if lookupErr != nil {
			return structs.WagerOutcome{}, lookupErr
		}
		if !found {
			return structs.WagerOutcome{}, err
		}
		return s.replay(existing, candidate)
	}
	if err != nil {
		return structs.WagerOutcome{}, err
	}
	return structs.WagerOutcome{Transaction: stored}, nil
}

// lookup finds the operation this one repeats or contradicts. The idempotency key and the
// provider's own transaction id are two identities of the same operation, so both are checked.
func (s *service) lookup(ctx context.Context, candidate *entities.WagerTransaction) (*entities.WagerTransaction, bool, error) {
	byKey, err := s.wagering.FindByKey(ctx, candidate.ProviderID(), candidate.IdempotencyKey())
	if err == nil {
		return byKey, true, nil
	}
	if !errors.Is(err, wageringiface.ErrNotFound) {
		return nil, false, err
	}

	byExternal, err := s.wagering.FindByExternal(ctx, candidate.ProviderID(), candidate.ExternalTransactionID())
	if err == nil {
		return byExternal, true, nil
	}
	if !errors.Is(err, wageringiface.ErrNotFound) {
		return nil, false, err
	}
	return nil, false, nil
}

// replay decides what a repeated operation is. The same key with the same content is a replay of
// the stored outcome. Anything else that matched, another content under the key or another key
// for the same provider transaction id, is a conflict, and nothing is applied.
func (s *service) replay(existing, candidate *entities.WagerTransaction) (structs.WagerOutcome, error) {
	sameKey := existing.IdempotencyKey() == candidate.IdempotencyKey()
	if !sameKey || !existing.MatchesPayload(candidate.PayloadHash()) {
		return structs.WagerOutcome{}, fmt.Errorf("%w: provider %s, transaction %s",
			wageringiface.ErrIdempotencyConflict, candidate.ProviderID(), candidate.ExternalTransactionID())
	}
	return structs.WagerOutcome{Transaction: existing, Replay: true}, nil
}

// apply is the write path: one unit of work that locks the wallet, decides what the operation
// does to it and stores everything that follows from that decision. It returns the transaction
// as stored.
func (s *service) apply(ctx context.Context, candidate *entities.WagerTransaction, correlationID string) (*entities.WagerTransaction, error) {
	walletID := candidate.WalletID()

	err := s.uow.Atomic(ctx, func(ctx context.Context) error {
		// The row lock is what serialises two writers of one wallet: the second waits here, then
		// reads the balance the first one left. Wallets that are not this one are not touched.
		wallet, err := s.wallets.GetForUpdate(ctx, walletID)
		if errors.Is(err, walletiface.ErrNotFound) {
			return entities.Reject(entities.FailureWalletNotFound, "wallet %s does not exist", walletID)
		}
		if err != nil {
			return err
		}

		decision, err := s.decide(wallet, candidate, correlationID, s.now())
		if err != nil {
			return err
		}
		return s.store(ctx, wallet, candidate, decision)
	})
	if err != nil {
		return nil, err
	}
	return candidate, nil
}

// correlationID is the one the request or message carries, or a fresh one for a caller that set
// none, so an event is never published without something to trace it by.
func (s *service) correlationID(ctx context.Context) string {
	if id := observability.CorrelationID(ctx); id != "" {
		return id
	}
	return s.newID().String()
}
