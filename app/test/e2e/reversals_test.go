//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Refunds and rollbacks
//
//	As the platform, I want a reversal to undo exactly its reference and only once,
//	so that a returned bet can never be returned twice.
//
//	Every reversal names the provider's own id for the transaction it undoes. The database closes the
//	last door with a unique index, and these scenarios check the rule above it, which is the one that
//	names the failure.
//
//	Scenarios:
//	  - A refund of a processed bet credits the exact amount
//	  - A rollback of a bet credits the wallet
//	  - A rollback of a win debits the wallet
//	  - A rollback of a refund debits the wallet
//	  - A rollback that would overdraw is rejected with its own failure code
//	  - A second reversal of the same reference is rejected
//	  - A rollback of a bet that was already refunded is rejected
//	  - A reversal disagreeing with its reference is rejected
//	  - A reversal whose amount differs from the reference is rejected
//	  - A reversal without a reference id is refused
//	  - A reversal of a rejected reference is rejected

// reversal builds a reversal of the given kind that undoes the provider's transaction.
func (w wallet) reversal(kind, amount, reference string) submitBody {
	body := w.operation(kind, amount)
	body.ReferenceExternalTransactionID = reference
	return body
}

// applied submits an operation that is expected to be processed and returns what it came to.
func applied(t *testing.T, body submitBody) transactionResponse {
	t.Helper()
	return core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusOK))
}

// rejected submits an operation that is expected to be refused by a business rule.
func rejected(t *testing.T, body submitBody) transactionResponse {
	t.Helper()
	return core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusUnprocessableEntity))
}

func lastEntry(t *testing.T, walletID string) (string, int64, int64, int64) {
	t.Helper()

	var direction string
	var amount, before, after int64
	if err := stack.DB(t).QueryRow(context.Background(), `
		SELECT direction, amount_minor, balance_before_minor, balance_after_minor
		FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY seq DESC LIMIT 1`, walletID).
		Scan(&direction, &amount, &before, &after); err != nil {
		t.Fatalf("read the last ledger entry: %v", err)
	}
	return direction, amount, before, after
}

// Scenario: A refund of a processed bet credits the exact amount
//
//	Given a processed BET of 25.00 BRL leaving the balance at 975.00
//	When the provider posts a REFUND referencing it
//	Then the balance returns to 1000.00 with a CREDIT entry of 25.00
func TestARefundOfAProcessedBetCreditsTheExactAmount(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)

	got := applied(t, w.reversal("REFUND", "25.00", bet.ExternalTransactionID))

	if got.Status != "PROCESSED" || got.Balance == nil || got.Balance.Amount != "1000.00" {
		t.Fatalf("response = %+v", got)
	}
	direction, amount, before, after := lastEntry(t, w.ID)
	if direction != "CREDIT" || amount != 2500 || before != 97500 || after != 100000 {
		t.Fatalf("entry = %s %d from %d to %d", direction, amount, before, after)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 100000 || s.version != 3 {
		t.Fatalf("wallet = %+v", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A rollback of a bet credits the wallet
//
//	Given a processed BET of 25.00 BRL
//	When the provider posts a ROLLBACK referencing it
//	Then the balance is credited by 25.00
func TestARollbackOfABetCreditsTheWallet(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)

	got := applied(t, w.reversal("ROLLBACK", "25.00", bet.ExternalTransactionID))

	if got.Balance == nil || got.Balance.Amount != "1000.00" {
		t.Fatalf("response = %+v", got)
	}
	if direction, _, _, _ := lastEntry(t, w.ID); direction != "CREDIT" {
		t.Fatalf("direction = %s", direction)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A rollback of a win debits the wallet
//
//	Given a processed WIN of 40.00 BRL
//	When the provider posts a ROLLBACK referencing it
//	Then the balance is debited by 40.00
func TestARollbackOfAWinDebitsTheWallet(t *testing.T) {
	w := newWallet(t, "1000.00")
	win := w.operation("WIN", "40.00")
	applied(t, win)

	got := applied(t, w.reversal("ROLLBACK", "40.00", win.ExternalTransactionID))

	if got.Balance == nil || got.Balance.Amount != "1000.00" {
		t.Fatalf("response = %+v", got)
	}
	if direction, _, _, _ := lastEntry(t, w.ID); direction != "DEBIT" {
		t.Fatalf("direction = %s", direction)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A rollback of a refund debits the wallet
//
//	Given a processed REFUND of 25.00 BRL
//	When the provider posts a ROLLBACK referencing it
//	Then the balance is debited by 25.00
func TestARollbackOfARefundDebitsTheWallet(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)
	refund := w.reversal("REFUND", "25.00", bet.ExternalTransactionID)
	applied(t, refund)

	got := applied(t, w.reversal("ROLLBACK", "25.00", refund.ExternalTransactionID))

	if got.Balance == nil || got.Balance.Amount != "975.00" {
		t.Fatalf("response = %+v", got)
	}
	if direction, _, _, _ := lastEntry(t, w.ID); direction != "DEBIT" {
		t.Fatalf("direction = %s", direction)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A rollback that would overdraw is rejected with its own failure code
//
//	Given a processed WIN of 40.00 BRL and a balance of 5.00 BRL
//	When the provider posts a ROLLBACK referencing that win
//	Then the response is 422 with failureCode ROLLBACK_INSUFFICIENT_FUNDS
//	And the code differs from the one a bet without funds produces
//	And the balance and the ledger are unchanged
func TestARollbackThatWouldOverdrawIsRejectedWithItsOwnFailureCode(t *testing.T) {
	w := newWallet(t, "0.00")
	win := w.operation("WIN", "40.00")
	applied(t, win)
	applied(t, w.operation("BET", "35.00"))
	before := walletState(t, w.ID)

	got := rejected(t, w.reversal("ROLLBACK", "40.00", win.ExternalTransactionID))

	if got.FailureCode != "ROLLBACK_INSUFFICIENT_FUNDS" || got.FailureCode == "INSUFFICIENT_FUNDS" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("a rejected rollback changed the wallet: before %+v, after %+v", before, after)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A second reversal of the same reference is rejected
//
//	Given a bet already refunded
//	When a second REFUND referencing the same bet arrives
//	Then the response is 422 with failureCode REFERENCE_ALREADY_REVERSED
//	And the balance is credited only once
func TestASecondReversalOfTheSameReferenceIsRejected(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)
	applied(t, w.reversal("REFUND", "25.00", bet.ExternalTransactionID))

	got := rejected(t, w.reversal("REFUND", "25.00", bet.ExternalTransactionID))

	if got.FailureCode != "REFERENCE_ALREADY_REVERSED" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 100000 || s.version != 3 {
		t.Fatalf("the bet was given back twice: %+v", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A rollback of a bet that was already refunded is rejected
//
//	Given a bet already refunded
//	When a ROLLBACK referencing the same bet arrives
//	Then it is rejected with REFERENCE_ALREADY_REVERSED
func TestARollbackOfABetThatWasAlreadyRefundedIsRejected(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)
	applied(t, w.reversal("REFUND", "25.00", bet.ExternalTransactionID))

	got := rejected(t, w.reversal("ROLLBACK", "25.00", bet.ExternalTransactionID))

	if got.FailureCode != "REFERENCE_ALREADY_REVERSED" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 100000 {
		t.Fatalf("the balance moved twice: %+v", s)
	}
}

// Scenario: A reversal disagreeing with its reference is rejected
//
//	Given a processed bet of round-987
//	When a REFUND referencing it declares round-988
//	Then the response is 422 with failureCode REFERENCE_MISMATCH
func TestAReversalDisagreeingWithItsReferenceIsRejected(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)
	refund := w.reversal("REFUND", "25.00", bet.ExternalTransactionID)
	refund.RoundID = "round-988"

	got := rejected(t, refund)

	if got.FailureCode != "REFERENCE_MISMATCH" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 97500 {
		t.Fatalf("the wallet changed: %+v", s)
	}
}

// Scenario: A reversal whose amount differs from the reference is rejected
//
//	Given a processed BET of 25.00 BRL
//	When a REFUND of 20.00 BRL referencing it arrives
//	Then the response is 422 with failureCode AMOUNT_MISMATCH
func TestAReversalWhoseAmountDiffersFromTheReferenceIsRejected(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)

	got := rejected(t, w.reversal("REFUND", "20.00", bet.ExternalTransactionID))

	if got.FailureCode != "AMOUNT_MISMATCH" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 97500 {
		t.Fatalf("a partial reversal was applied: %+v", s)
	}
}

// Scenario: A reversal without a reference id is refused
//
//	When a REFUND arrives with no referenceExternalTransactionId
//	Then the response is 400 and nothing is persisted
func TestAReversalWithoutAReferenceIdIsRefused(t *testing.T) {
	w := newWallet(t, "1000.00")

	core.RequireStatus(t, submit(t, w.reversal("REFUND", "25.00", "")), http.StatusBadRequest)

	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d transactions were stored for a reversal that names nothing", got)
	}
}

// Scenario: A reversal of a rejected reference is rejected
//
//	Given a BET rejected for insufficient funds
//	When a REFUND referencing it arrives
//	Then the response is 422 with failureCode REFERENCE_NOT_PROCESSED
func TestAReversalOfARejectedReferenceIsRejected(t *testing.T) {
	w := newWallet(t, "10.00")
	bet := w.operation("BET", "25.00")
	rejected(t, bet)

	got := rejected(t, w.reversal("REFUND", "25.00", bet.ExternalTransactionID))

	if got.FailureCode != "REFERENCE_NOT_PROCESSED" {
		t.Fatalf("failureCode = %q", got.FailureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 1000 {
		t.Fatalf("the wallet changed: %+v", s)
	}
}
