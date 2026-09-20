//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Idempotent replay
//
//	As a game provider retrying after a timeout, I want the stored outcome back,
//	so that a retry can never move money twice.
//
//	The idempotency lives in the database, in the unique identities of the transaction and the
//	hash of its business content, so it does not depend on which instance answers or on anything
//	held in memory.
//
//	Scenarios:
//	  - A replay with the same key and the same payload returns the stored result
//	  - A replay returns the balance observed at the original processing
//	  - Replaying a rejection returns the same rejection
//	  - The same key with a different payload returns conflict
//	  - The same operation under a different key returns conflict
//	  - A key that another provider used is a different operation

// Scenario: A replay with the same key and the same payload returns the stored result
//
//	Given a bet of 25.00 BRL already processed
//	When the identical request is posted again
//	Then the response is 200 with idempotentReplay true and the original transaction id
//	And there is still exactly one debit in the ledger
func TestAReplayWithTheSameKeyAndTheSamePayloadReturnsTheStoredResult(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	first := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusOK))

	replay := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusOK))

	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID || replay.Status != "PROCESSED" {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	if replay.Balance == nil || replay.Balance.Amount != "975.00" {
		t.Fatalf("replay balance = %+v", replay.Balance)
	}
	if s := walletState(t, w.ID); s.debits != 2500 || s.balanceMinor != 97500 || s.version != 2 {
		t.Fatalf("the bet was applied more than once: %+v", s)
	}
	if got := transactionsOf(t, w.ID); got != 1 {
		t.Fatalf("%d transactions were stored for one operation", got)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A replay returns the balance observed at the original processing
//
//	Given a bet of 25.00 BRL processed when the balance became 975.00
//	And a later win that moved the balance to 1200.00
//	When the original bet is replayed
//	Then the response carries balance 975.00, not 1200.00
func TestAReplayReturnsTheBalanceObservedAtTheOriginalProcessing(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	core.RequireStatus(t, submit(t, bet), http.StatusOK)
	core.RequireStatus(t, submit(t, w.operation("WIN", "225.00")), http.StatusOK)
	if s := walletState(t, w.ID); s.balanceMinor != 120000 {
		t.Fatalf("setup: the wallet holds %d", s.balanceMinor)
	}

	replay := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, bet), http.StatusOK))

	if !replay.IdempotentReplay || replay.Balance == nil || replay.Balance.Amount != "975.00" {
		t.Fatalf("replay = %+v, want the 975.00 of the original processing", replay)
	}
}

// Scenario: Replaying a rejection returns the same rejection
//
//	Given a bet rejected for insufficient funds
//	And the wallet later credited enough to cover it
//	When the same request is posted again
//	Then the response is the same 422 rejection, marked as a replay
//	And nothing is applied
func TestReplayingARejectionReturnsTheSameRejection(t *testing.T) {
	w := newWallet(t, "10.00")
	bet := w.operation("BET", "25.00")
	first := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, bet), http.StatusUnprocessableEntity))
	core.RequireStatus(t, submit(t, w.operation("WIN", "500.00")), http.StatusOK)

	replay := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, bet), http.StatusUnprocessableEntity))

	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID ||
		replay.Status != "REJECTED" || replay.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("replay = %+v, first = %+v: a rejection must be replayed, not re-evaluated", replay, first)
	}
	if s := walletState(t, w.ID); s.debits != 0 {
		t.Fatalf("the rejected bet was applied on replay: %+v", s)
	}
}

// Scenario: The same key with a different payload returns conflict
//
//	Given a bet of 25.00 BRL already processed under a key
//	When a bet of 30.00 BRL is posted under the same key
//	Then the response is 409 and nothing is applied
func TestTheSameKeyWithADifferentPayloadReturnsConflict(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	key := body.ProviderID + ":" + body.ExternalTransactionID
	core.RequireStatus(t, submitAs(t, core.ProviderA, key, body), http.StatusOK)

	changed := body
	changed.Money.Amount = "30.00"
	core.RequireStatus(t, submitAs(t, core.ProviderA, key, changed), http.StatusConflict)

	if s := walletState(t, w.ID); s.balanceMinor != 97500 || s.version != 2 || s.debits != 2500 {
		t.Fatalf("the conflicting request changed the wallet: %+v", s)
	}
}

// Scenario: The same operation under a different key returns conflict
//
//	Given a bet already processed for a provider and external transaction id
//	When the same pair is posted under a different idempotency key
//	Then the response is 409 and nothing is applied
func TestTheSameOperationUnderADifferentKeyReturnsConflict(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	core.RequireStatus(t, submit(t, body), http.StatusOK)

	core.RequireStatus(t, submitAs(t, core.ProviderA, "another-key-"+uuid.NewString(), body), http.StatusConflict)

	if s := walletState(t, w.ID); s.balanceMinor != 97500 || s.debits != 2500 {
		t.Fatalf("the operation was applied under a second key: %+v", s)
	}
}

// Scenario: A key that another provider used is a different operation
//
//	Given a bet processed for provider-a under a key
//	When provider-b posts a bet under the very same key string
//	Then it is processed on its own, because keys are scoped by provider
func TestAKeyThatAnotherProviderUsedIsADifferentOperation(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	sharedKey := "shared-key-" + uuid.NewString()
	core.RequireStatus(t, submitAs(t, core.ProviderA, sharedKey, body), http.StatusOK)

	other := w.operation("BET", "25.00")
	other.ProviderID = core.ProviderB
	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submitAs(t, core.ProviderB, sharedKey, other), http.StatusOK))

	if got.IdempotentReplay {
		t.Fatal("provider-b was answered with provider-a's stored result")
	}
	if s := walletState(t, w.ID); s.debits != 5000 {
		t.Fatalf("both providers' bets must apply: debits %d", s.debits)
	}
}
