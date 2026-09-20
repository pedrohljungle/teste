package entities

import (
	"errors"
	"testing"
)

func TestALedgerEntryValidatesItsArithmeticOnConstruction(t *testing.T) {
	credit, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, brl(t, "40.00"), brl(t, "100.00"), brl(t, "140.00"), t0)
	if err != nil {
		t.Fatalf("credit: %v", err)
	}
	if credit.Direction() != DirectionCredit || credit.BalanceAfter().Amount() != "140.00" {
		t.Fatalf("credit = %+v", credit)
	}
	if _, err := NewLedgerEntry(id(1), id(2), id(3), DirectionDebit, brl(t, "80.00"), brl(t, "100.00"), brl(t, "20.00"), t0); err != nil {
		t.Fatalf("debit: %v", err)
	}
}

func TestALedgerEntryRefusesArithmeticThatDoesNotHold(t *testing.T) {
	cases := []struct {
		name      string
		direction Direction
		amount    string
		before    string
		after     string
	}{
		{"credit that adds too little", DirectionCredit, "40.00", "100.00", "139.99"},
		{"credit that subtracts", DirectionCredit, "40.00", "100.00", "60.00"},
		{"debit that adds", DirectionDebit, "40.00", "100.00", "140.00"},
		{"debit that subtracts too much", DirectionDebit, "40.00", "100.00", "59.99"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewLedgerEntry(id(1), id(2), id(3), tc.direction, brl(t, tc.amount), brl(t, tc.before), brl(t, tc.after), t0)
			if !errors.Is(err, ErrInvalidLedgerEntry) {
				t.Fatalf("error = %v, want ErrInvalidLedgerEntry", err)
			}
		})
	}
}

func TestALedgerEntryRefusesWhatIsNotWellFormed(t *testing.T) {
	ok := brl(t, "10.00")
	cases := []struct {
		name string
		make func() error
	}{
		{"no id", func() error {
			_, err := NewLedgerEntry([16]byte{}, id(2), id(3), DirectionCredit, ok, ok, brl(t, "20.00"), t0)
			return err
		}},
		{"no wallet", func() error {
			_, err := NewLedgerEntry(id(1), [16]byte{}, id(3), DirectionCredit, ok, ok, brl(t, "20.00"), t0)
			return err
		}},
		{"no transaction", func() error {
			_, err := NewLedgerEntry(id(1), id(2), [16]byte{}, DirectionCredit, ok, ok, brl(t, "20.00"), t0)
			return err
		}},
		{"unknown direction", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), "SIDEWAYS", ok, ok, brl(t, "20.00"), t0)
			return err
		}},
		{"zero amount", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, brl(t, "0.00"), ok, ok, t0)
			return err
		}},
		{"negative amount", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, mustMinor(t, -1, "BRL"), ok, ok, t0)
			return err
		}},
		{"uninitialised balance before", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, ok, Money{}, ok, t0)
			return err
		}},
		{"uninitialised balance after", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, ok, ok, Money{}, t0)
			return err
		}},
		{"negative balance after", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionDebit, ok, brl(t, "5.00"), mustMinor(t, -500, "BRL"), t0)
			return err
		}},
		{"mixed currencies", func() error {
			_, err := NewLedgerEntry(id(1), id(2), id(3), DirectionCredit, ok, mustMoney(t, "10.00", "USD"), mustMoney(t, "20.00", "USD"), t0)
			return err
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.make(); !errors.Is(err, ErrInvalidLedgerEntry) {
				t.Fatalf("error = %v, want ErrInvalidLedgerEntry", err)
			}
		})
	}
}

func TestRehydratingALedgerEntryRestoresItAsStoredWithItsPosition(t *testing.T) {
	stored := LedgerEntrySnapshot{
		ID: id(1), Seq: 42, WalletID: id(2), TransactionID: id(3), Direction: "DEBIT",
		AmountMinor: 8000, Currency: "BRL", BalanceBeforeMinor: 10000, BalanceAfterMinor: 2000, CreatedAt: t0,
	}

	entry, err := RehydrateLedgerEntry(stored)
	if err != nil {
		t.Fatalf("RehydrateLedgerEntry: %v", err)
	}
	if entry.Seq() != 42 || entry.Direction() != DirectionDebit || entry.Money().Amount() != "80.00" {
		t.Fatalf("entry = %+v", entry)
	}
	if entry.Snapshot() != stored {
		t.Fatalf("snapshot = %+v, want %+v", entry.Snapshot(), stored)
	}
}

func TestRehydratingALedgerEntryDoesNotRecheckTheArithmetic(t *testing.T) {
	// Checking it is the construction's job. A stored row that broke it is found by the
	// reconciliation, and refusing to read it here would only hide it from the listing.
	broken := LedgerEntrySnapshot{
		ID: id(1), WalletID: id(2), TransactionID: id(3), Direction: "CREDIT",
		AmountMinor: 100, Currency: "BRL", BalanceBeforeMinor: 100, BalanceAfterMinor: 999, CreatedAt: t0,
	}
	if _, err := RehydrateLedgerEntry(broken); err != nil {
		t.Fatalf("RehydrateLedgerEntry: %v", err)
	}
}

func TestRehydratingALedgerEntryRejectsAnUnknownDirectionOrCurrency(t *testing.T) {
	base := LedgerEntrySnapshot{ID: id(1), WalletID: id(2), TransactionID: id(3), Direction: "CREDIT", Currency: "BRL"}

	direction := base
	direction.Direction = "SIDEWAYS"
	if _, err := RehydrateLedgerEntry(direction); !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Errorf("unknown direction: error = %v", err)
	}
	currency := base
	currency.Currency = "ZZZ"
	if _, err := RehydrateLedgerEntry(currency); !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Errorf("unknown currency: error = %v", err)
	}
}
