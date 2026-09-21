package wagering

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
)

func TestAProviderReadsItsOwnTransactionByIdAndByItsExternalId(t *testing.T) {
	f := newFixture(t, "1000.00")
	stored := f.must(t, "BET", "bet-1", "25.00", "")

	byID, err := f.svc.Get(context.Background(), "provider-a", stored.ID())
	if err != nil || byID.ID() != stored.ID() || byID.Status() != entities.StatusProcessed {
		t.Fatalf("Get = %v, %v", byID, err)
	}
	byExternal, err := f.svc.GetByExternal(context.Background(), "provider-a", "bet-1")
	if err != nil || byExternal.ID() != stored.ID() {
		t.Fatalf("GetByExternal = %v, %v", byExternal, err)
	}
}

func TestAProviderCannotReadAnotherProvidersTransactionAndCannotTellItExists(t *testing.T) {
	f := newFixture(t, "1000.00")
	stored := f.must(t, "BET", "bet-1", "25.00", "")

	_, foreign := f.svc.Get(context.Background(), "provider-b", stored.ID())
	_, missing := f.svc.Get(context.Background(), "provider-b", uuid.New())

	if !errors.Is(foreign, wageringiface.ErrNotFound) || !errors.Is(missing, wageringiface.ErrNotFound) {
		t.Fatalf("errors = %v and %v, want ErrNotFound for both", foreign, missing)
	}
	if foreign.Error() != missing.Error() {
		t.Fatalf("a transaction of another provider answers %q and a missing one %q: the difference tells which exists",
			foreign, missing)
	}
	if _, err := f.svc.GetByExternal(context.Background(), "provider-b", "bet-1"); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Fatalf("GetByExternal for another provider: error = %v, want ErrNotFound", err)
	}
}

func TestAnInternalTransactionIsNeverReadThroughTheProviderReads(t *testing.T) {
	f := newFixture(t, "1000.00")
	// the fixture stores the wallet without its opening: store one the way the wallet service does
	wallet, err := entities.RehydrateWallet(f.m.wallets[f.wallet])
	if err != nil {
		t.Fatal(err)
	}
	internal, err := entities.NewOpeningTransaction(uuid.New(), wallet.ID(), wallet.PlayerID(), money(t, "1000.00"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	f.m.transactions[internal.ID()] = internal.Snapshot()

	for _, provider := range []string{"provider-a", ""} {
		if _, err := f.svc.Get(context.Background(), provider, internal.ID()); !errors.Is(err, wageringiface.ErrNotFound) {
			t.Errorf("provider %q read an internal transaction: error = %v", provider, err)
		}
	}
	if _, err := f.svc.GetByExternal(context.Background(), "", "anything"); !errors.Is(err, wageringiface.ErrNotFound) {
		t.Errorf("an empty provider must find nothing: error = %v", err)
	}
}

func TestAPendingTransactionExposesItsWaitAndARejectedOneItsCode(t *testing.T) {
	f := newFixture(t, "10.00")
	pending := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	rejected := f.must(t, "BET", "bet-2", "25.00", "")

	gotPending, err := f.svc.Get(context.Background(), "provider-a", pending.ID())
	if err != nil || gotPending.Status() != entities.StatusPendingReference ||
		gotPending.ReferenceNextAttemptAt().IsZero() || gotPending.ReferenceExpiresAt().IsZero() {
		t.Fatalf("pending = %v, %v", gotPending, err)
	}
	gotRejected, err := f.svc.Get(context.Background(), "provider-a", rejected.ID())
	if err != nil || gotRejected.FailureCode() != entities.FailureInsufficientFunds {
		t.Fatalf("rejected = %v, %v", gotRejected, err)
	}
}
