package entities

import (
	"errors"
	"fmt"
	"testing"
)

func TestARejectionCarriesItsCodeAndMatchesTheSentinel(t *testing.T) {
	err := fmt.Errorf("applying: %w", Reject(FailureAmountMismatch, "amounts differ by %d", 5))

	if !errors.Is(err, ErrRejected) {
		t.Fatal("a wrapped Rejection must match ErrRejected")
	}
	code, ok := RejectionCode(err)
	if !ok || code != FailureAmountMismatch {
		t.Fatalf("RejectionCode = %q, %v", code, ok)
	}
	if got := Reject(FailureInvalidAmount, "bad").Error(); got != "INVALID_AMOUNT: bad" {
		t.Fatalf("Error() = %q", got)
	}
	if _, ok := RejectionCode(errors.New("plain")); ok {
		t.Fatal("a plain error is not a rejection")
	}
}

func TestInsufficientFundsBecomesADifferentCodeForAReversal(t *testing.T) {
	err := fmt.Errorf("%w: balance 1.00", ErrInsufficientFunds)

	bet, ok := FailureCodeOf(KindBet, err)
	if !ok || bet != FailureInsufficientFunds {
		t.Fatalf("BET: %q, %v", bet, ok)
	}
	rollback, ok := FailureCodeOf(KindRollback, err)
	if !ok || rollback != FailureRollbackInsufficientFunds {
		t.Fatalf("ROLLBACK: %q, %v", rollback, ok)
	}
	// The whole point: a reversal that would overdraw must not read as a bet without funds.
	if bet == rollback {
		t.Fatal("the two insufficient funds codes must differ")
	}
}

func TestFailureCodeOfClassifiesBusinessOutcomesOnly(t *testing.T) {
	if code, ok := FailureCodeOf(KindBet, ErrCurrencyMismatch); !ok || code != FailureCurrencyMismatch {
		t.Errorf("currency mismatch: %q, %v", code, ok)
	}
	if code, ok := FailureCodeOf(KindRefund, Reject(FailureReferenceMismatch, "x")); !ok || code != FailureReferenceMismatch {
		t.Errorf("rejection passthrough: %q, %v", code, ok)
	}
	if _, ok := FailureCodeOf(KindBet, errors.New("connection reset")); ok {
		t.Error("an infrastructure error is not a business outcome")
	}
}

func TestOnlyInputTheProviderCanFixIsCorrectable(t *testing.T) {
	correctable := []FailureCode{FailureAmountMismatch, FailureCurrencyMismatch, FailureWalletNotFound, FailurePlayerMismatch, FailureInvalidAmount}
	definitive := []FailureCode{
		FailureInsufficientFunds, FailureRollbackInsufficientFunds, FailureReferenceNotFound,
		FailureReferenceNotProcessed, FailureReferenceAlreadyReversed, FailureReferenceMismatch,
		FailureOpeningNotAllowed, FailureInternal,
	}
	for _, code := range correctable {
		if !code.Correctable() {
			t.Errorf("%s must be correctable", code)
		}
	}
	for _, code := range definitive {
		if code.Correctable() {
			t.Errorf("%s must be definitive", code)
		}
	}
}
