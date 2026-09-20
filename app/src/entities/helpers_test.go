package entities

import (
	"testing"
	"time"

	"github.com/google/uuid"
)

var t0 = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

func id(n int) uuid.UUID {
	return uuid.MustParse("00000000-0000-7000-8000-" + padHex(n))
}

func padHex(n int) string {
	const digits = "0123456789abcdef"
	out := []byte("000000000000")
	for i := len(out) - 1; i >= 0 && n > 0; i-- {
		out[i] = digits[n%16]
		n /= 16
	}
	return string(out)
}

func brl(t *testing.T, amount string) Money {
	t.Helper()
	return mustMoney(t, amount, "BRL")
}

// operation is a well formed BET of 25.00 that individual tests then adapt.
func operation(t *testing.T) ExternalOperation {
	t.Helper()
	return ExternalOperation{
		ProviderID:            "provider-a",
		ExternalTransactionID: "transaction-123",
		IdempotencyKey:        "provider-a:transaction-123",
		PlayerID:              id(1).String(),
		WalletID:              id(2).String(),
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  "BET",
		Money:                 brl(t, "25.00"),
	}
}

func newTransaction(t *testing.T, mutate func(*ExternalOperation)) *WagerTransaction {
	t.Helper()
	op := operation(t)
	if mutate != nil {
		mutate(&op)
	}
	tx, err := NewExternalTransaction(id(10), op, t0)
	if err != nil {
		t.Fatalf("NewExternalTransaction: %v", err)
	}
	return tx
}

func processed(t *testing.T, tx *WagerTransaction) *WagerTransaction {
	t.Helper()
	if err := tx.MarkProcessed(brl(t, "975.00"), 2, t0); err != nil {
		t.Fatalf("MarkProcessed: %v", err)
	}
	return tx
}

func newWallet(t *testing.T, balance string) *Wallet {
	t.Helper()
	opening, err := OpenWallet(OpeningIDs{Wallet: id(2), Transaction: id(3), Entry: id(4)}, id(1), brl(t, balance), t0)
	if err != nil {
		t.Fatalf("OpenWallet: %v", err)
	}
	return opening.Wallet
}
