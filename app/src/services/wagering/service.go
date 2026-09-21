package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	inboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/inbox"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

type service struct {
	uow      persistenceiface.UnitOfWork
	wallets  walletiface.Repository
	wagering wageringiface.Repository
	outbox   outboxiface.Repository
	inbox    inboxiface.Repository
	cfg      config.Reference
	obs      *observability.Observer

	// now, newID and jitter are fields so a test can fix the clock, the identities and the spread
	// of a retry.
	now    func() time.Time
	newID  func() uuid.UUID
	jitter func(time.Duration) time.Duration
}

// NewService builds the wagering service.
func NewService(
	uow persistenceiface.UnitOfWork,
	wallets walletiface.Repository,
	wagering wageringiface.Repository,
	outbox outboxiface.Repository,
	inbox inboxiface.Repository,
	cfg config.Reference,
	obs *observability.Observer,
) wageringiface.Service {
	return newService(uow, wallets, wagering, outbox, inbox, cfg, obs)
}

// NewReferenceResolver builds the resolver of pending references. It is the same rules as the
// service, reached by a periodic job instead of a request, and shares its code and not its state:
// the service holds none.
func NewReferenceResolver(
	uow persistenceiface.UnitOfWork,
	wallets walletiface.Repository,
	wagering wageringiface.Repository,
	outbox outboxiface.Repository,
	inbox inboxiface.Repository,
	cfg config.Reference,
	obs *observability.Observer,
) wageringiface.ReferenceResolver {
	return newService(uow, wallets, wagering, outbox, inbox, cfg, obs)
}

func newService(
	uow persistenceiface.UnitOfWork,
	wallets walletiface.Repository,
	wagering wageringiface.Repository,
	outbox outboxiface.Repository,
	inbox inboxiface.Repository,
	cfg config.Reference,
	obs *observability.Observer,
) *service {
	return &service{
		uow:      uow,
		wallets:  wallets,
		wagering: wagering,
		outbox:   outbox,
		inbox:    inbox,
		cfg:      cfg,
		obs:      obs,
		now:      time.Now,
		newID:    func() uuid.UUID { return uuid.Must(uuid.NewV7()) },
		jitter:   spread,
	}
}

// Submit applies an operation once, and answers a repeat of it with the stored outcome.
//
// Idempotency has two layers and only the second one is a guarantee. Looking the operation up
// first is a shortcut that spares a repeat from taking the wallet lock. What makes it safe when
// two copies arrive together is the unique indexes on the transaction: both pass the lookup, one
// wins the insert, and the other is refused by the database and resolves as a replay.
func (s *service) Submit(ctx context.Context, operation entities.ExternalOperation) (structs.WagerOutcome, error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.Submit",
		observability.String("providerId", operation.ProviderID),
		observability.String("walletId", operation.WalletID),
	)
	started := time.Now()
	outcome, err := s.submit(ctx, operation)
	end(err)
	s.record(ctx, sourceHTTP, started, operation.Kind, outcome, err)
	return outcome, err
}

func (s *service) submit(ctx context.Context, operation entities.ExternalOperation) (structs.WagerOutcome, error) {
	candidate, err := entities.NewExternalTransaction(s.newID(), operation, s.now())
	if err != nil {
		return structs.WagerOutcome{}, err
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
		s.obs.Count(ctx, "wager_concurrent_duplicates_total", observability.NewTag("source", sourceHTTP))
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

// apply is the write path of an operation that arrived over HTTP: one unit of work that applies the
// operation. It returns the transaction as stored.
func (s *service) apply(ctx context.Context, candidate *entities.WagerTransaction, correlationID string) (*entities.WagerTransaction, error) {
	err := s.uow.Atomic(ctx, func(ctx context.Context) error {
		return s.applyOperation(ctx, candidate, correlationID)
	})
	if err != nil {
		return nil, err
	}
	return candidate, nil
}

// applyOperation locks the wallet, decides what the operation does to it and stores everything
// that follows from that decision. It has to run inside a unit of work, and it is the body that
// both entry points share: the queue wraps it in the inbox, HTTP does not.
func (s *service) applyOperation(ctx context.Context, candidate *entities.WagerTransaction, correlationID string) error {
	// The row lock is what serialises two writers of one wallet: the second waits here, then reads
	// the balance the first one left. Wallets that are not this one are not touched.
	lockStarted := time.Now()
	wallet, err := s.wallets.GetForUpdate(ctx, candidate.WalletID())
	// How long the lock took is the contention on the wallet: near zero when nobody else is writing
	// it, and the wait behind the other writers when somebody is.
	s.obs.Measure(ctx, "wallet_lock_wait_seconds", time.Since(lockStarted))
	if errors.Is(err, walletiface.ErrNotFound) {
		return entities.Reject(entities.FailureWalletNotFound, "wallet %s does not exist", candidate.WalletID())
	}
	if err != nil {
		return err
	}
	// What a reversal refers to is read after the lock, so it is the state the decision is made on
	// and not one another writer is about to change.
	resolution, err := s.resolve(ctx, candidate)
	if err != nil {
		return err
	}

	decision, err := s.decide(wallet, candidate, resolution, correlationID, s.now())
	if err != nil {
		return err
	}
	return s.store(ctx, wallet, candidate, decision, insert)
}

// correlationID is the one the request or message carries, or a fresh one for a caller that set
// none, so an event is never published without something to trace it by.
func (s *service) correlationID(ctx context.Context) string {
	if id := observability.CorrelationID(ctx); id != "" {
		return id
	}
	return s.newID().String()
}
