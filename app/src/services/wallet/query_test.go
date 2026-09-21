package wallet

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// walletWithLedger opens a wallet through the service and then appends n more movements straight to
// the double, which is what the reads are about.
func walletWithLedger(t *testing.T, extraEntries int) (*service, *memory, uuid.UUID) {
	t.Helper()
	m := newMemory()
	svc := newTestService(m)
	wallet, err := svc.Open(context.Background(), player, brl(t, "1000.00"))
	if err != nil {
		t.Fatal(err)
	}
	for range extraEntries {
		appendDebit(t, m, wallet.ID(), 100)
	}
	return svc, m, wallet.ID()
}

// appendDebit adds a debit of the given minor units to the wallet's ledger in the double.
func appendDebit(t *testing.T, m *memory, walletID uuid.UUID, minor int64) {
	t.Helper()
	last := m.entries[len(m.entries)-1]
	entry := last
	entry.ID = uuid.New()
	entry.Direction = "DEBIT"
	entry.AmountMinor = minor
	entry.BalanceBeforeMinor = last.BalanceAfterMinor
	entry.BalanceAfterMinor = last.BalanceAfterMinor - minor
	entry.WalletID = walletID
	m.entries = append(m.entries, entry)
}

func TestReadingAWalletReturnsItAndReportsAMissingOne(t *testing.T) {
	svc, _, id := walletWithLedger(t, 0)

	got, err := svc.Get(context.Background(), id)
	if err != nil || got.Balance().Amount() != "1000.00" {
		t.Fatalf("Get = %v, %v", got, err)
	}
	if _, err := svc.Get(context.Background(), uuid.New()); !errors.Is(err, walletiface.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}

func TestTheLedgerIsReadOldestFirstInPagesWithACursor(t *testing.T) {
	svc, _, id := walletWithLedger(t, 6)

	first, err := svc.Ledger(context.Background(), id, "", 3)
	if err != nil || len(first.Entries) != 3 || !first.HasMore {
		t.Fatalf("first page: %d entries, hasMore %v, error %v", len(first.Entries), first.HasMore, err)
	}
	if first.Entries[0].Direction() != entities.DirectionCredit || first.Entries[0].BalanceBefore().Amount() != "0.00" {
		t.Fatalf("the ledger must start with the opening: %+v", first.Entries[0])
	}

	second, err := svc.Ledger(context.Background(), id, structs.EncodeCursor(first.NextAfter), 3)
	if err != nil || len(second.Entries) != 3 || !second.HasMore {
		t.Fatalf("second page: %d entries, hasMore %v, error %v", len(second.Entries), second.HasMore, err)
	}
	third, err := svc.Ledger(context.Background(), id, structs.EncodeCursor(second.NextAfter), 3)
	if err != nil || len(third.Entries) != 1 || third.HasMore {
		t.Fatalf("last page: %d entries, hasMore %v, error %v", len(third.Entries), third.HasMore, err)
	}

	seen := map[int64]bool{}
	for _, page := range []structs.LedgerPage{first, second, third} {
		for _, entry := range page.Entries {
			if seen[entry.Seq()] {
				t.Fatalf("entry %d appeared on two pages", entry.Seq())
			}
			seen[entry.Seq()] = true
		}
	}
	if len(seen) != 7 {
		t.Fatalf("the pages held %d entries, want the opening and six more", len(seen))
	}
}

func TestAnEntryWrittenWhileReadingIsFoundOnALaterPageAndNothingIsRepeated(t *testing.T) {
	svc, m, id := walletWithLedger(t, 3)
	first, err := svc.Ledger(context.Background(), id, "", 2)
	if err != nil {
		t.Fatal(err)
	}

	appendDebit(t, m, id, 50)
	second, err := svc.Ledger(context.Background(), id, structs.EncodeCursor(first.NextAfter), 50)
	if err != nil {
		t.Fatal(err)
	}

	if len(second.Entries) != 3 {
		t.Fatalf("the second page holds %d entries, want the 2 that were there and the new one", len(second.Entries))
	}
	if second.Entries[0].Seq() <= first.Entries[len(first.Entries)-1].Seq() {
		t.Fatal("an entry already seen came back")
	}
}

func TestThePageSizeDefaultsAndIsCapped(t *testing.T) {
	svc, _, id := walletWithLedger(t, 249)

	defaults, err := svc.Ledger(context.Background(), id, "", 0)
	if err != nil || len(defaults.Entries) != defaultPageSize {
		t.Fatalf("a request naming no limit got %d entries, want %d (%v)", len(defaults.Entries), defaultPageSize, err)
	}
	capped, err := svc.Ledger(context.Background(), id, "", 10_000)
	if err != nil || len(capped.Entries) != maxPageSize || !capped.HasMore {
		t.Fatalf("a huge limit got %d entries, want the cap %d (%v)", len(capped.Entries), maxPageSize, err)
	}
	if _, err := svc.Ledger(context.Background(), id, "", -1); !errors.Is(err, walletiface.ErrInvalidPage) {
		t.Fatalf("a negative limit: error = %v, want ErrInvalidPage", err)
	}
}

func TestALedgerReadRefusesABadCursorAndAMissingWallet(t *testing.T) {
	svc, _, id := walletWithLedger(t, 1)

	if _, err := svc.Ledger(context.Background(), id, "not-a-cursor", 10); !errors.Is(err, structs.ErrInvalidCursor) {
		t.Errorf("a bad cursor: error = %v, want ErrInvalidCursor", err)
	}
	if _, err := svc.Ledger(context.Background(), uuid.New(), "", 10); !errors.Is(err, walletiface.ErrNotFound) {
		t.Errorf("a missing wallet: error = %v, want ErrNotFound", err)
	}
}

func TestAnEmptyLedgerIsAnEmptyPageAndNotAnError(t *testing.T) {
	m := newMemory()
	svc := newTestService(m)
	wallet, err := svc.Open(context.Background(), player, brl(t, "0.00"))
	if err != nil {
		t.Fatal(err)
	}

	page, err := svc.Ledger(context.Background(), wallet.ID(), "", 10)

	if err != nil || len(page.Entries) != 0 || page.HasMore || page.NextAfter != 0 {
		t.Fatalf("page = %+v, error %v", page, err)
	}
}

func TestReconcilingAHealthyWalletReportsNoDifferenceAndCountsTheOpening(t *testing.T) {
	svc, _, id := walletWithLedger(t, 0)

	got, err := svc.Reconcile(context.Background(), id)

	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !got.Consistent || got.StoredBalance.Amount() != "1000.00" || got.CalculatedBalance.Amount() != "1000.00" ||
		got.Difference.Amount() != "0.00" || got.CheckedEntries != 1 || got.WalletID != id.String() {
		t.Fatalf("reconciliation = %+v", got)
	}
}

func TestReconcilingRebuildsTheBalanceFromCreditsMinusDebits(t *testing.T) {
	svc, m, id := walletWithLedger(t, 2)
	stored := m.wallets[id]
	stored.BalanceMinor = 99800
	m.wallets[id] = stored

	got, err := svc.Reconcile(context.Background(), id)

	if err != nil || !got.Consistent || got.CalculatedBalance.Amount() != "998.00" || got.CheckedEntries != 3 {
		t.Fatalf("reconciliation = %+v, error %v", got, err)
	}
}

func TestADivergenceIsReportedWithTheExactDifferenceAndChangesNothing(t *testing.T) {
	svc, m, id := walletWithLedger(t, 0)
	stored := m.wallets[id]
	stored.BalanceMinor += 500
	m.wallets[id] = stored
	entriesBefore := len(m.entries)

	got, err := svc.Reconcile(context.Background(), id)

	if err != nil {
		t.Fatalf("finding a divergence is what a reconciliation is for, not a failure: %v", err)
	}
	if got.Consistent || got.StoredBalance.Amount() != "1005.00" || got.CalculatedBalance.Amount() != "1000.00" ||
		got.Difference.Amount() != "5.00" {
		t.Fatalf("reconciliation = %+v", got)
	}
	if m.wallets[id].BalanceMinor != 100500 || len(m.entries) != entriesBefore {
		t.Fatal("the reconciliation altered the balance or the ledger")
	}
}

func TestANegativeDifferenceMeansTheWalletHoldsLessThanItsLedger(t *testing.T) {
	svc, m, id := walletWithLedger(t, 0)
	stored := m.wallets[id]
	stored.BalanceMinor -= 250
	m.wallets[id] = stored

	got, err := svc.Reconcile(context.Background(), id)

	if err != nil || got.Consistent || got.Difference.Amount() != "-2.50" {
		t.Fatalf("reconciliation = %+v, error %v", got, err)
	}
}

func TestReconcilingAMissingWalletReportsItAndRunsInOneSnapshot(t *testing.T) {
	svc, _, _ := walletWithLedger(t, 0)

	if _, err := svc.Reconcile(context.Background(), uuid.New()); !errors.Is(err, walletiface.ErrNotFound) {
		t.Fatalf("error = %v, want ErrNotFound", err)
	}
}
