package entities

import (
	"errors"
	"math"
	"testing"
	"time"
)

func TestOpeningAWalletWithABalanceCreatesTheOpeningTransactionAndTheCredit(t *testing.T) {
	opening, err := OpenWallet(OpeningIDs{Wallet: id(2), Transaction: id(3), Entry: id(4)}, id(1), brl(t, "1000.00"), t0)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}

	wallet := opening.Wallet
	if wallet.Balance().Amount() != "1000.00" || wallet.Version() != 1 || wallet.Currency() != "BRL" {
		t.Fatalf("wallet = %s at version %d in %s", wallet.Balance(), wallet.Version(), wallet.Currency())
	}
	if wallet.ExpectedVersion() != 0 {
		t.Fatalf("a wallet that was never stored must expect version 0, got %d", wallet.ExpectedVersion())
	}

	tx := opening.Transaction
	if tx == nil || tx.Kind() != KindOpening || tx.Origin() != OriginInternal || tx.Status() != StatusProcessed {
		t.Fatalf("opening transaction = %+v", tx)
	}
	if tx.ProviderID() != "" || tx.ExternalTransactionID() != "" || tx.IdempotencyKey() != "" ||
		len(tx.PayloadHash()) != 0 || tx.RoundID() != "" || tx.GameID() != "" ||
		tx.ReferenceExternalTransactionID() != "" {
		t.Fatalf("an internal transaction must carry no external metadata: %+v", tx.Snapshot())
	}
	if tx.ResultBalance().Amount() != "1000.00" || tx.ResultWalletVersion() != 1 {
		t.Fatalf("result = %s at version %d", tx.ResultBalance(), tx.ResultWalletVersion())
	}

	entry := opening.Entry
	if entry == nil || entry.Direction() != DirectionCredit ||
		entry.BalanceBefore().Amount() != "0.00" || entry.BalanceAfter().Amount() != "1000.00" ||
		entry.TransactionID() != tx.ID() {
		t.Fatalf("opening entry = %+v", entry)
	}
}

func TestOpeningAWalletWithAZeroBalanceCreatesNeitherTransactionNorLedger(t *testing.T) {
	opening, err := OpenWallet(OpeningIDs{Wallet: id(2)}, id(1), brl(t, "0.00"), t0)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	if opening.Transaction != nil || opening.Entry != nil {
		t.Fatalf("a zero opening must create no transaction and no entry: %+v", opening)
	}
	if opening.Wallet.Version() != 1 || !opening.Wallet.Balance().IsZero() {
		t.Fatalf("wallet = %s at version %d", opening.Wallet.Balance(), opening.Wallet.Version())
	}
}

func TestOpeningAWalletRejectsInvalidInput(t *testing.T) {
	ids := OpeningIDs{Wallet: id(2), Transaction: id(3), Entry: id(4)}
	negative := mustMinor(t, -1, "BRL")

	cases := []struct {
		name    string
		ids     OpeningIDs
		player  int
		initial Money
	}{
		{"missing wallet id", OpeningIDs{}, 1, brl(t, "1.00")},
		{"missing player id", ids, 0, brl(t, "1.00")},
		{"uninitialised balance", ids, 1, Money{}},
		{"negative balance", ids, 1, negative},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			player := id(tc.player)
			if tc.player == 0 {
				player = [16]byte{}
			}
			if _, err := OpenWallet(tc.ids, player, tc.initial, t0); !errors.Is(err, ErrInvalidWallet) {
				t.Fatalf("error = %v, want ErrInvalidWallet", err)
			}
		})
	}
}

func TestCreditingAWalletRaisesTheBalanceAndTheVersionAndProducesTheEntry(t *testing.T) {
	wallet := newWallet(t, "100.00")

	entry, err := wallet.Credit(id(5), id(6), brl(t, "40.00"), t0.Add(time.Minute))
	if err != nil {
		t.Fatalf("Credit: %v", err)
	}

	if wallet.Balance().Amount() != "140.00" || wallet.Version() != 2 {
		t.Fatalf("wallet = %s at version %d", wallet.Balance(), wallet.Version())
	}
	if !wallet.UpdatedAt().Equal(t0.Add(time.Minute)) {
		t.Fatalf("UpdatedAt = %s", wallet.UpdatedAt())
	}
	if entry.Direction() != DirectionCredit || entry.BalanceBefore().Amount() != "100.00" ||
		entry.BalanceAfter().Amount() != "140.00" || entry.Money().Amount() != "40.00" ||
		entry.WalletID() != wallet.ID() || entry.TransactionID() != id(6) {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestDebitingAWalletLowersTheBalanceAndTheVersionAndProducesTheEntry(t *testing.T) {
	wallet := newWallet(t, "100.00")

	entry, err := wallet.Debit(id(5), id(6), brl(t, "80.00"), t0)
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if wallet.Balance().Amount() != "20.00" || wallet.Version() != 2 {
		t.Fatalf("wallet = %s at version %d", wallet.Balance(), wallet.Version())
	}
	if entry.Direction() != DirectionDebit || entry.BalanceBefore().Amount() != "100.00" || entry.BalanceAfter().Amount() != "20.00" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestDebitingTheWholeBalanceIsAllowedAndLeavesZero(t *testing.T) {
	wallet := newWallet(t, "100.00")
	if _, err := wallet.Debit(id(5), id(6), brl(t, "100.00"), t0); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if !wallet.Balance().IsZero() {
		t.Fatalf("balance = %s, want 0.00", wallet.Balance())
	}
}

func TestDebitingMoreThanTheBalanceIsRefusedAndChangesNothing(t *testing.T) {
	wallet := newWallet(t, "10.00")

	_, err := wallet.Debit(id(5), id(6), brl(t, "10.01"), t0)

	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("error = %v, want ErrInsufficientFunds", err)
	}
	if wallet.Balance().Amount() != "10.00" || wallet.Version() != 1 {
		t.Fatalf("a refused debit changed the wallet: %s at version %d", wallet.Balance(), wallet.Version())
	}
}

func TestAWalletRefusesMovementsThatAreNotValid(t *testing.T) {
	cases := []struct {
		name   string
		amount Money
		want   error
	}{
		{"zero", brl(t, "0.00"), ErrNonPositiveAmount},
		{"negative", mustMinor(t, -100, "BRL"), ErrNonPositiveAmount},
		{"uninitialised", Money{}, ErrUninitializedMoney},
		{"another currency", mustMoney(t, "1.00", "USD"), ErrCurrencyMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wallet := newWallet(t, "100.00")
			if _, err := wallet.Credit(id(5), id(6), tc.amount, t0); !errors.Is(err, tc.want) {
				t.Errorf("Credit error = %v, want %v", err, tc.want)
			}
			if _, err := wallet.Debit(id(5), id(6), tc.amount, t0); !errors.Is(err, tc.want) {
				t.Errorf("Debit error = %v, want %v", err, tc.want)
			}
			if wallet.Balance().Amount() != "100.00" || wallet.Version() != 1 {
				t.Errorf("a refused movement changed the wallet: %s at version %d", wallet.Balance(), wallet.Version())
			}
		})
	}
}

func TestAWalletDoesNotChangeWhenTheLedgerEntryCannotBeBuilt(t *testing.T) {
	wallet := newWallet(t, "100.00")

	// A nil transaction id makes NewLedgerEntry fail after the arithmetic already succeeded.
	if _, err := wallet.Credit(id(5), [16]byte{}, brl(t, "1.00"), t0); !errors.Is(err, ErrInvalidLedgerEntry) {
		t.Fatalf("error = %v, want ErrInvalidLedgerEntry", err)
	}
	if wallet.Balance().Amount() != "100.00" || wallet.Version() != 1 {
		t.Fatalf("the wallet changed without its entry: %s at version %d", wallet.Balance(), wallet.Version())
	}
}

func TestAWalletReportsOverflowInsteadOfWrapping(t *testing.T) {
	wallet, err := RehydrateWallet(WalletSnapshot{
		ID: id(2), PlayerID: id(1), Currency: "BRL", BalanceMinor: math.MaxInt64, Version: 1,
	})
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}
	if _, err := wallet.Credit(id(5), id(6), brl(t, "0.01"), t0); !errors.Is(err, ErrMoneyOverflow) {
		t.Fatalf("error = %v, want ErrMoneyOverflow", err)
	}
}

func TestRehydratingAWalletRestoresItWithoutMovementOrVersionChange(t *testing.T) {
	stored := WalletSnapshot{
		ID: id(2), PlayerID: id(1), Currency: "BRL", BalanceMinor: 2000, Version: 7,
		CreatedAt: t0, UpdatedAt: t0.Add(time.Hour),
	}

	wallet, err := RehydrateWallet(stored)
	if err != nil {
		t.Fatalf("RehydrateWallet: %v", err)
	}

	if wallet.Balance().Minor() != 2000 || wallet.Version() != 7 || wallet.ExpectedVersion() != 7 {
		t.Fatalf("wallet = %s at version %d expecting %d", wallet.Balance(), wallet.Version(), wallet.ExpectedVersion())
	}
	if wallet.Snapshot() != stored {
		t.Fatalf("snapshot = %+v, want %+v", wallet.Snapshot(), stored)
	}

	// The version an UPDATE must find stays the loaded one while the aggregate moves on.
	if _, err := wallet.Debit(id(5), id(6), brl(t, "5.00"), t0); err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if wallet.Version() != 8 || wallet.ExpectedVersion() != 7 {
		t.Fatalf("after a debit: version %d, expected %d", wallet.Version(), wallet.ExpectedVersion())
	}
}

func TestRehydratingAWalletRejectsWhatCouldNotExist(t *testing.T) {
	valid := WalletSnapshot{ID: id(2), PlayerID: id(1), Currency: "BRL", BalanceMinor: 100, Version: 1}
	cases := []struct {
		name   string
		mutate func(*WalletSnapshot)
	}{
		{"no id", func(s *WalletSnapshot) { s.ID = [16]byte{} }},
		{"no player", func(s *WalletSnapshot) { s.PlayerID = [16]byte{} }},
		{"unknown currency", func(s *WalletSnapshot) { s.Currency = "ZZZ" }},
		{"negative balance", func(s *WalletSnapshot) { s.BalanceMinor = -1 }},
		{"version zero", func(s *WalletSnapshot) { s.Version = 0 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.mutate(&s)
			if _, err := RehydrateWallet(s); !errors.Is(err, ErrInvalidWallet) {
				t.Fatalf("error = %v, want ErrInvalidWallet", err)
			}
		})
	}
}
