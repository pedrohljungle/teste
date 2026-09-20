//go:build e2e

package e2e

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
)

// Feature: The unit of work and the repositories
//
//	As a service that changes a wallet, I want its writes to land together or not at all,
//	so that a balance can never exist without its ledger entry and its event.
//
//	These scenarios drive the real adapters against the real database, because that is the only
//	thing that can prove atomicity: a double would agree with itself.
//
//	Scenarios:
//	  - A commit makes every write of the unit visible together
//	  - An error rolls every write of the unit back
//	  - A panic rolls the unit back and is raised again
//	  - A nested unit of work joins the outer one
//	  - A write that must land with others is refused outside a unit of work
//	  - The row lock makes two writers of one wallet run one after the other
//	  - An update of a wallet that changed meanwhile is refused
//	  - A second wallet for the same player and currency is refused
//	  - A second ledger entry for a transaction is refused
//	  - A transaction that collides on a unique identity is refused
//	  - A settled transaction is never rewritten
//	  - A second successful reversal of one reference is refused
//	  - Reading what does not exist reports it as not found
//	  - A statement that cannot reach the database is reported as unavailable

// Scenario: A commit makes every write of the unit visible together
//
//	Given a wallet opening built by the domain
//	When the wallet, its transaction, its ledger entry and its event are written in one unit
//	Then all four exist afterwards
func TestACommitMakesEveryWriteOfTheUnitVisibleTogether(t *testing.T) {
	opening := openWallet(t, "1000.00")

	writeOpening(t, opening)

	if _, err := stack.Repos.Wallets.Get(context.Background(), opening.Wallet.ID()); err != nil {
		t.Fatalf("the wallet is missing: %v", err)
	}
	if _, err := stack.Repos.Wagering.Get(context.Background(), opening.Transaction.ID()); err != nil {
		t.Fatalf("the transaction is missing: %v", err)
	}
	db := stack.DB(t)
	if got := count(t, db, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", opening.Wallet.ID()); got != 1 {
		t.Fatalf("expected 1 ledger entry, found %d", got)
	}
	if got := count(t, db, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", opening.Transaction.ID()); got != 1 {
		t.Fatalf("expected 1 event, found %d", got)
	}
}

// Scenario: An error rolls every write of the unit back
//
//	Given a wallet opening built by the domain
//	When the writes are made and the unit then fails
//	Then nothing was stored
func TestAnErrorRollsEveryWriteOfTheUnitBack(t *testing.T) {
	opening := openWallet(t, "1000.00")
	boom := errors.New("the unit failed after its writes")

	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		storeOpening(t, ctx, opening)
		return boom
	})

	if !errors.Is(err, boom) {
		t.Fatalf("error = %v, want the error of the unit", err)
	}
	requireNothingStored(t, opening)
}

// Scenario: A panic rolls the unit back and is raised again
//
//	Given a wallet opening built by the domain
//	When the writes are made and the unit panics
//	Then the panic reaches the caller
//	And nothing was stored
func TestAPanicRollsTheUnitBackAndIsRaisedAgain(t *testing.T) {
	opening := openWallet(t, "1000.00")

	func() {
		defer func() {
			if recovered := recover(); recovered == nil {
				t.Error("the panic was swallowed")
			}
		}()
		_ = stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
			storeOpening(t, ctx, opening)
			panic("the unit blew up")
		})
	}()

	requireNothingStored(t, opening)
}

// Scenario: A nested unit of work joins the outer one
//
//	Given a wallet opening built by the domain
//	When an inner unit writes it and returns nil, and the outer unit then fails
//	Then the inner writes are rolled back with the outer
func TestANestedUnitOfWorkJoinsTheOuterOne(t *testing.T) {
	opening := openWallet(t, "1000.00")

	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(outer context.Context) error {
		if err := stack.Repos.UnitOfWork.Atomic(outer, func(inner context.Context) error {
			storeOpening(t, inner, opening)
			return nil
		}); err != nil {
			t.Errorf("the inner unit failed: %v", err)
		}
		return errors.New("the outer unit failed")
	})

	if err == nil {
		t.Fatal("the outer unit was expected to fail")
	}
	requireNothingStored(t, opening)
}

// Scenario: A write that must land with others is refused outside a unit of work
//
//	Given a stored wallet
//	When it is locked, updated, given a ledger entry or an event outside a unit of work
//	Then each of them is refused, because a write like that could survive without the others
func TestAWriteThatMustLandWithOthersIsRefusedOutsideAUnitOfWork(t *testing.T) {
	opening := openWallet(t, "1000.00")
	writeOpening(t, opening)
	ctx := context.Background()

	if _, err := stack.Repos.Wallets.GetForUpdate(ctx, opening.Wallet.ID()); !errors.Is(err, persistenceiface.ErrNoTransaction) {
		t.Errorf("GetForUpdate: error = %v", err)
	}
	if err := stack.Repos.Wallets.Update(ctx, opening.Wallet); !errors.Is(err, persistenceiface.ErrNoTransaction) {
		t.Errorf("Update: error = %v", err)
	}
	if err := stack.Repos.Wallets.InsertEntry(ctx, *opening.Entry); !errors.Is(err, persistenceiface.ErrNoTransaction) {
		t.Errorf("InsertEntry: error = %v", err)
	}
	event := processedEvent(t, opening.Transaction)
	if err := stack.Repos.Outbox.Insert(ctx, event); !errors.Is(err, persistenceiface.ErrNoTransaction) {
		t.Errorf("Insert an event: error = %v", err)
	}
}

// Scenario: The row lock makes two writers of one wallet run one after the other
//
//	Given a wallet with a balance of 1000.00
//	And a first unit that locks it and holds the lock for a while
//	When a second unit asks for the same lock meanwhile
//	Then the second only gets the wallet after the first committed
//	And it reads the balance the first one left
//	And no update is lost
func TestTheRowLockMakesTwoWritersOfOneWalletRunOneAfterTheOther(t *testing.T) {
	opening := openWallet(t, "1000.00")
	writeOpening(t, opening)
	walletID := opening.Wallet.ID()

	firstHasTheLock := make(chan struct{})
	var wg sync.WaitGroup
	var secondSaw entities.Money
	var secondWaited time.Duration

	wg.Add(2)
	go func() {
		defer wg.Done()
		err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
			wallet, err := stack.Repos.Wallets.GetForUpdate(ctx, walletID)
			if err != nil {
				return err
			}
			close(firstHasTheLock)
			time.Sleep(600 * time.Millisecond)
			return debit(t, ctx, wallet, "80.00")
		})
		if err != nil {
			t.Errorf("first writer: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		<-firstHasTheLock
		started := time.Now()
		err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
			wallet, err := stack.Repos.Wallets.GetForUpdate(ctx, walletID)
			if err != nil {
				return err
			}
			secondWaited = time.Since(started)
			secondSaw = wallet.Balance()
			return debit(t, ctx, wallet, "20.00")
		})
		if err != nil {
			t.Errorf("second writer: %v", err)
		}
	}()
	wg.Wait()

	if secondWaited < 400*time.Millisecond {
		t.Errorf("the second writer did not wait for the lock: it waited %s", secondWaited)
	}
	if secondSaw.Amount() != "920.00" {
		t.Errorf("the second writer read %s, want the 920.00 the first left", secondSaw)
	}
	final, err := stack.Repos.Wallets.Get(context.Background(), walletID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Balance().Amount() != "900.00" || final.Version() != 3 {
		t.Fatalf("final wallet = %s at version %d, want 900.00 at version 3: an update was lost", final.Balance(), final.Version())
	}
}

// Scenario: An update of a wallet that changed meanwhile is refused
//
//	Given a wallet read at version 1
//	And the same wallet changed and committed by someone else
//	When the stale copy is written
//	Then the write is refused as a concurrent update
//	And the committed change stays
func TestAnUpdateOfAWalletThatChangedMeanwhileIsRefused(t *testing.T) {
	opening := openWallet(t, "1000.00")
	writeOpening(t, opening)
	ctx := context.Background()

	stale, err := stack.Repos.Wallets.Get(ctx, opening.Wallet.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := stack.Repos.UnitOfWork.Atomic(ctx, func(ctx context.Context) error {
		current, err := stack.Repos.Wallets.GetForUpdate(ctx, opening.Wallet.ID())
		if err != nil {
			return err
		}
		return debit(t, ctx, current, "10.00")
	}); err != nil {
		t.Fatal(err)
	}

	err = stack.Repos.UnitOfWork.Atomic(ctx, func(ctx context.Context) error {
		if _, err := stale.Debit(uuid.New(), uuid.New(), money(t, "500.00"), time.Now()); err != nil {
			return err
		}
		return stack.Repos.Wallets.Update(ctx, stale)
	})

	if !errors.Is(err, walletiface.ErrConcurrentUpdate) {
		t.Fatalf("error = %v, want ErrConcurrentUpdate", err)
	}
	final, err := stack.Repos.Wallets.Get(ctx, opening.Wallet.ID())
	if err != nil {
		t.Fatal(err)
	}
	if final.Balance().Amount() != "990.00" || final.Version() != 2 {
		t.Fatalf("final wallet = %s at version %d, want the committed 990.00 at version 2", final.Balance(), final.Version())
	}
}

// Scenario: A second wallet for the same player and currency is refused
//
//	Given a stored wallet for a player in BRL
//	When another wallet is inserted for the same player in BRL
//	Then the repository reports that the wallet already exists
func TestASecondWalletForTheSamePlayerAndCurrencyIsRefusedByTheRepository(t *testing.T) {
	first := openWallet(t, "0.00")
	writeOpening(t, first)

	second, err := entities.OpenWallet(
		entities.OpeningIDs{Wallet: uuid.New()}, first.Wallet.PlayerID(), money(t, "0.00"), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	err = stack.Repos.Wallets.Insert(context.Background(), second.Wallet)

	if !errors.Is(err, walletiface.ErrAlreadyExists) {
		t.Fatalf("error = %v, want ErrAlreadyExists", err)
	}
}

// Scenario: A second ledger entry for a transaction is refused
//
//	Given a wallet with a ledger entry for its opening
//	When a second entry is written for the same transaction
//	Then the repository reports a duplicate movement
func TestASecondLedgerEntryForATransactionIsRefusedByTheRepository(t *testing.T) {
	opening := openWallet(t, "1000.00")
	writeOpening(t, opening)

	duplicate, err := entities.NewLedgerEntry(uuid.New(), opening.Wallet.ID(), opening.Transaction.ID(),
		entities.DirectionCredit, money(t, "1.00"), money(t, "1000.00"), money(t, "1001.00"), time.Now())
	if err != nil {
		t.Fatal(err)
	}

	err = stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		return stack.Repos.Wallets.InsertEntry(ctx, duplicate)
	})

	if !errors.Is(err, walletiface.ErrDuplicateMovement) {
		t.Fatalf("error = %v, want ErrDuplicateMovement", err)
	}
}

// Scenario: A transaction that collides on a unique identity is refused
//
//	Given a stored bet
//	When another transaction is inserted with the same provider and external id
//	Or with the same idempotency key
//	Then the repository reports a duplicate
//	And the stored bet can be found by either identity
func TestATransactionThatCollidesOnAUniqueIdentityIsRefused(t *testing.T) {
	wallet := openWallet(t, "1000.00")
	writeOpening(t, wallet)
	stored := storeBet(t, wallet, "ext-"+uuid.NewString(), "25.00")
	ctx := context.Background()

	sameExternal := betOn(t, wallet, stored.ExternalTransactionID(), "another-key-"+uuid.NewString(), "25.00")
	if err := stack.Repos.Wagering.Insert(ctx, sameExternal); !errors.Is(err, wageringiface.ErrDuplicate) {
		t.Errorf("same external id: error = %v, want ErrDuplicate", err)
	}
	sameKey := betOn(t, wallet, "another-ext-"+uuid.NewString(), stored.IdempotencyKey(), "25.00")
	if err := stack.Repos.Wagering.Insert(ctx, sameKey); !errors.Is(err, wageringiface.ErrDuplicate) {
		t.Errorf("same key: error = %v, want ErrDuplicate", err)
	}

	byExternal, err := stack.Repos.Wagering.FindByExternal(ctx, "provider-a", stored.ExternalTransactionID())
	if err != nil || byExternal.ID() != stored.ID() {
		t.Errorf("FindByExternal = %v, %v", byExternal, err)
	}
	byKey, err := stack.Repos.Wagering.FindByKey(ctx, "provider-a", stored.IdempotencyKey())
	if err != nil || byKey.ID() != stored.ID() {
		t.Errorf("FindByKey = %v, %v", byKey, err)
	}
	if !byKey.MatchesPayload(stored.PayloadHash()) {
		t.Error("the stored payload hash did not survive the round trip")
	}
}

// Scenario: A settled transaction is never rewritten
//
//	Given a stored bet that was then processed
//	When a stale copy of the bet, still pending, is rejected and written
//	Then the repository reports the transaction is stale
//	And the processed outcome stays
func TestASettledTransactionIsNeverRewritten(t *testing.T) {
	wallet := openWallet(t, "1000.00")
	writeOpening(t, wallet)
	pending := storeBet(t, wallet, "ext-"+uuid.NewString(), "25.00")
	ctx := context.Background()

	stale, err := stack.Repos.Wagering.Get(ctx, pending.ID())
	if err != nil {
		t.Fatal(err)
	}
	if err := pending.MarkProcessed(money(t, "975.00"), 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := stack.Repos.Wagering.Update(ctx, pending); err != nil {
		t.Fatalf("settling the bet: %v", err)
	}

	if err := stale.MarkRejected(entities.FailureInsufficientFunds, time.Now()); err != nil {
		t.Fatal(err)
	}
	err = stack.Repos.Wagering.Update(ctx, stale)

	if !errors.Is(err, wageringiface.ErrStale) {
		t.Fatalf("error = %v, want ErrStale", err)
	}
	final, err := stack.Repos.Wagering.Get(ctx, pending.ID())
	if err != nil {
		t.Fatal(err)
	}
	if final.Status() != entities.StatusProcessed || final.ResultBalance().Amount() != "975.00" || final.ResultWalletVersion() != 2 {
		t.Fatalf("stored = %s, %s at version %d", final.Status(), final.ResultBalance(), final.ResultWalletVersion())
	}
}

// Scenario: A second successful reversal of one reference is refused
//
//	Given a processed bet
//	And a processed refund referencing it
//	When a rollback referencing the same bet is marked processed
//	Then the repository reports the reference was already reversed
func TestASecondSuccessfulReversalOfOneReferenceIsRefusedByTheRepository(t *testing.T) {
	wallet := openWallet(t, "1000.00")
	writeOpening(t, wallet)
	bet := storeBet(t, wallet, "ext-"+uuid.NewString(), "25.00")
	ctx := context.Background()

	refund := reversalOf(t, wallet, bet, "REFUND")
	if err := stack.Repos.Wagering.Insert(ctx, refund); err != nil {
		t.Fatal(err)
	}
	if err := refund.MarkProcessed(money(t, "1000.00"), 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := stack.Repos.Wagering.Update(ctx, refund); err != nil {
		t.Fatalf("the first reversal: %v", err)
	}

	rollback := reversalOf(t, wallet, bet, "ROLLBACK")
	if err := stack.Repos.Wagering.Insert(ctx, rollback); err != nil {
		t.Fatal(err)
	}
	if err := rollback.MarkProcessed(money(t, "1000.00"), 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	err := stack.Repos.Wagering.Update(ctx, rollback)

	if !errors.Is(err, wageringiface.ErrAlreadyReversed) {
		t.Fatalf("error = %v, want ErrAlreadyReversed", err)
	}
}

// Scenario: Reading what does not exist reports it as not found
//
//	Given nothing stored under a fresh id
//	When a wallet and a transaction are read by that id, and by unknown provider identities
//	Then each read reports not found
func TestReadingWhatDoesNotExistReportsItAsNotFound(t *testing.T) {
	ctx := context.Background()
	missing := uuid.New()

	if _, err := stack.Repos.Wallets.Get(ctx, missing); !errors.Is(err, walletiface.ErrNotFound) {
		t.Errorf("wallet: error = %v", err)
	}
	if _, err := stack.Repos.Wagering.Get(ctx, missing); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Errorf("transaction: error = %v", err)
	}
	if _, err := stack.Repos.Wagering.FindByExternal(ctx, "provider-a", "nope"); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Errorf("by external id: error = %v", err)
	}
	if _, err := stack.Repos.Wagering.FindByKey(ctx, "provider-a", "nope"); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Errorf("by key: error = %v", err)
	}
}

// Scenario: A statement that cannot reach the database is reported as unavailable
//
//	Given a context whose deadline already passed
//	When a wallet is read
//	Then the failure is classified as storage unavailable, which is what a caller retries
func TestAStatementThatCannotReachTheDatabaseIsReportedAsUnavailable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()

	_, err := stack.Repos.Wallets.Get(ctx, uuid.New())

	if !errors.Is(err, persistenceiface.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
}
