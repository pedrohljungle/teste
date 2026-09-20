//go:build e2e

package e2e

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
)

// The helpers below build domain objects for the scenarios that talk to storage directly. They
// go through the domain constructors, never around them, so a row a scenario stores is one the
// application could have produced.

func money(t *testing.T, amount string) entities.Money {
	t.Helper()

	m, err := entities.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatalf("parse %q: %v", amount, err)
	}
	return m
}

// openWallet builds, without storing, a wallet opening for a new player.
func openWallet(t *testing.T, balance string) entities.WalletOpening {
	t.Helper()

	opening, err := entities.OpenWallet(
		entities.OpeningIDs{Wallet: uuid.New(), Transaction: uuid.New(), Entry: uuid.New()},
		uuid.New(), money(t, balance), time.Now())
	if err != nil {
		t.Fatalf("open a wallet: %v", err)
	}
	return opening
}

// storeOpening writes everything an opening produces and returns the event it wrote, nil for an
// opening with nothing to publish. It has to run inside a unit of work.
func storeOpening(t *testing.T, ctx context.Context, opening entities.WalletOpening) *entities.OutboxEvent {
	t.Helper()

	if err := stack.Repos.Wallets.Insert(ctx, opening.Wallet); err != nil {
		t.Fatalf("store the wallet: %v", err)
	}
	if opening.Transaction == nil {
		return nil
	}
	if err := stack.Repos.Wagering.Insert(ctx, opening.Transaction); err != nil {
		t.Fatalf("store the opening transaction: %v", err)
	}
	if err := stack.Repos.Wallets.InsertEntry(ctx, *opening.Entry); err != nil {
		t.Fatalf("store the ledger entry: %v", err)
	}
	event := processedEvent(t, opening.Transaction)
	if err := stack.Repos.Outbox.Insert(ctx, event); err != nil {
		t.Fatalf("store the event: %v", err)
	}
	return event
}

// writeOpening stores an opening in one committed unit of work and returns the event it wrote.
func writeOpening(t *testing.T, opening entities.WalletOpening) *entities.OutboxEvent {
	t.Helper()

	var event *entities.OutboxEvent
	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		event = storeOpening(t, ctx, opening)
		return nil
	})
	if err != nil {
		t.Fatalf("write the opening: %v", err)
	}
	return event
}

// requireNothingStored asserts that no trace of an opening reached the database.
func requireNothingStored(t *testing.T, opening entities.WalletOpening) {
	t.Helper()

	ctx := context.Background()
	if _, err := stack.Repos.Wallets.Get(ctx, opening.Wallet.ID()); !errors.Is(err, walletiface.ErrNotFound) {
		t.Errorf("the wallet survived: error = %v", err)
	}
	if opening.Transaction == nil {
		return
	}
	if _, err := stack.Repos.Wagering.Get(ctx, opening.Transaction.ID()); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Errorf("the transaction survived: error = %v", err)
	}
	db := stack.DB(t)
	if got := count(t, db, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", opening.Wallet.ID()); got != 0 {
		t.Errorf("%d ledger entries survived", got)
	}
	if got := count(t, db, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", opening.Transaction.ID()); got != 0 {
		t.Errorf("%d events survived", got)
	}
}

func processedEvent(t *testing.T, transaction *entities.WagerTransaction) *entities.OutboxEvent {
	t.Helper()

	event, err := entities.NewWagerTransactionProcessedEvent(uuid.New(), transaction, "corr-"+uuid.NewString(), "", time.Now())
	if err != nil {
		t.Fatalf("build the event: %v", err)
	}
	return event
}

// debit applies a real bet to a wallet inside the caller's unit of work: the transaction, the
// ledger entry, the new balance and the outcome, exactly as the application does.
func debit(t *testing.T, ctx context.Context, wallet *entities.Wallet, amount string) error {
	t.Helper()

	bet := betFor(t, wallet.ID(), wallet.PlayerID(), "ext-"+uuid.NewString(), "key-"+uuid.NewString(), amount)
	if err := stack.Repos.Wagering.Insert(ctx, bet); err != nil {
		return err
	}
	entry, err := wallet.Debit(uuid.New(), bet.ID(), money(t, amount), time.Now())
	if err != nil {
		return err
	}
	if err := stack.Repos.Wallets.InsertEntry(ctx, entry); err != nil {
		return err
	}
	if err := stack.Repos.Wallets.Update(ctx, wallet); err != nil {
		return err
	}
	if err := bet.MarkProcessed(wallet.Balance(), wallet.Version(), time.Now()); err != nil {
		return err
	}
	return stack.Repos.Wagering.Update(ctx, bet)
}

func betFor(t *testing.T, walletID, playerID uuid.UUID, external, key, amount string) *entities.WagerTransaction {
	t.Helper()

	bet, err := entities.NewExternalTransaction(uuid.New(), entities.ExternalOperation{
		ProviderID:            "provider-a",
		ExternalTransactionID: external,
		IdempotencyKey:        key,
		PlayerID:              playerID.String(),
		WalletID:              walletID.String(),
		RoundID:               "round-1",
		GameID:                "game-1",
		Kind:                  "BET",
		Money:                 money(t, amount),
	}, time.Now())
	if err != nil {
		t.Fatalf("build a bet: %v", err)
	}
	return bet
}

// betOn builds, without storing, a bet on the opened wallet.
func betOn(t *testing.T, opening entities.WalletOpening, external, key, amount string) *entities.WagerTransaction {
	t.Helper()
	return betFor(t, opening.Wallet.ID(), opening.Wallet.PlayerID(), external, key, amount)
}

// storeBet stores a pending bet on the opened wallet.
func storeBet(t *testing.T, opening entities.WalletOpening, external, amount string) *entities.WagerTransaction {
	t.Helper()

	bet := betOn(t, opening, external, "key-"+uuid.NewString(), amount)
	if err := stack.Repos.Wagering.Insert(context.Background(), bet); err != nil {
		t.Fatalf("store a bet: %v", err)
	}
	return bet
}

// reversalOf builds, without storing, a REFUND or ROLLBACK of the given bet.
func reversalOf(t *testing.T, opening entities.WalletOpening, bet *entities.WagerTransaction, kind string) *entities.WagerTransaction {
	t.Helper()

	reversal, err := entities.NewExternalTransaction(uuid.New(), entities.ExternalOperation{
		ProviderID:                     bet.ProviderID(),
		ExternalTransactionID:          "rev-" + uuid.NewString(),
		IdempotencyKey:                 "key-" + uuid.NewString(),
		PlayerID:                       opening.Wallet.PlayerID().String(),
		WalletID:                       opening.Wallet.ID().String(),
		RoundID:                        bet.RoundID(),
		GameID:                         bet.GameID(),
		Kind:                           kind,
		Money:                          bet.Money(),
		ReferenceExternalTransactionID: bet.ExternalTransactionID(),
	}, time.Now())
	if err != nil {
		t.Fatalf("build a reversal: %v", err)
	}
	return reversal
}

func count(t *testing.T, db *pgxpool.Pool, query string, args ...any) int {
	t.Helper()

	var n int
	if err := db.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	return n
}
