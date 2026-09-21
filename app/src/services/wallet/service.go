package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

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

// NewService builds the wallet service.
func NewService(
	uow persistenceiface.UnitOfWork,
	wallets walletiface.Repository,
	wagering wageringiface.Repository,
	outbox outboxiface.Repository,
	obs *observability.Observer,
) walletiface.Service {
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

func (s *service) Open(ctx context.Context, playerID uuid.UUID, initialBalance entities.Money) (wallet *entities.Wallet, err error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wallet.Service.Open")
	defer func() { end(err) }()

	now := s.now()
	opening, err := entities.OpenWallet(
		entities.OpeningIDs{Wallet: s.newID(), Transaction: s.newID(), Entry: s.newID()},
		playerID, initialBalance, now)
	if err != nil {
		return nil, err
	}

	correlationID := s.correlationID(ctx)
	// Everything an opening produces lands in one commit. The events are written here, before
	// it, and never after: an event that outlived a commit that failed would announce a wallet
	// that does not exist.
	err = s.uow.Atomic(ctx, func(ctx context.Context) error {
		return s.store(ctx, opening, correlationID, now)
	})
	if err != nil {
		return nil, fmt.Errorf("open wallet: %w", err)
	}
	return opening.Wallet, nil
}

func (s *service) store(ctx context.Context, opening entities.WalletOpening, correlationID string, now time.Time) error {
	if err := s.wallets.Insert(ctx, opening.Wallet); err != nil {
		return err
	}
	// A zero balance leaves nothing to audit: no transaction, no ledger entry, no event.
	if opening.Transaction == nil {
		return nil
	}
	if err := s.wagering.Insert(ctx, opening.Transaction); err != nil {
		return err
	}
	if err := s.wallets.InsertEntry(ctx, *opening.Entry); err != nil {
		return err
	}

	processed, err := entities.NewWagerTransactionProcessedEvent(s.newID(), opening.Transaction, correlationID, "", now)
	if err != nil {
		return err
	}
	changed, err := entities.NewWalletBalanceChangedEvent(s.newID(), *opening.Entry, opening.Wallet.Version(),
		correlationID, opening.Transaction.ID().String(), now)
	if err != nil {
		return err
	}
	for _, event := range []*entities.OutboxEvent{processed, changed} {
		if err := s.outbox.Insert(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

// correlationID is the one the request or message carries, or a fresh one for a caller that set
// none, so an event is never published without something to trace it by.
func (s *service) correlationID(ctx context.Context) string {
	if id := observability.CorrelationID(ctx); id != "" {
		return id
	}
	return s.newID().String()
}
