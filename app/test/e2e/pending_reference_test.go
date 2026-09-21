//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Operations waiting for a reference
//
//	As the platform, I want a reversal that arrives early to wait durably,
//	so that out-of-order delivery costs nothing.
//
//	The wait is stored on the transaction itself: how many times it looked, when it looks next and
//	when it gives up. Nothing about it lives in memory, so any instance can take it over.
//
//	Scenarios:
//	  - A refund arriving before its bet is held as pending reference
//	  - Replaying a pending operation returns the pending outcome
//	  - The inbox message of a pending reference is completed once the pendency is durable
//	  - A pending reference is resolved when its bet finally arrives
//	  - A pending reference expires and is rejected
//	  - A reference that is itself pending keeps the reversal waiting
//	  - The retry state of a pending reference is stored on the transaction
//	  - Three workers resolving pending references apply each reversal once

const resolveTimeout = 30 * time.Second

type pendingRow struct {
	status      string
	failureCode string
	attempts    int
	nextAttempt *time.Time
	expiresAt   *time.Time
}

func pendingState(t *testing.T, external string) pendingRow {
	t.Helper()

	var row pendingRow
	var code *string
	if err := stack.DB(t).QueryRow(context.Background(), `
		SELECT status, failure_code, reference_attempts, reference_next_attempt_at, reference_expires_at
		FROM wager_transactions WHERE provider_id = $1 AND external_transaction_id = $2`, core.ProviderA, external).
		Scan(&row.status, &code, &row.attempts, &row.nextAttempt, &row.expiresAt); err != nil {
		t.Fatalf("read %s: %v", external, err)
	}
	if code != nil {
		row.failureCode = *code
	}
	return row
}

func waitForStatus(t *testing.T, external, status string) pendingRow {
	t.Helper()

	deadline := time.Now().Add(resolveTimeout)
	for time.Now().Before(deadline) {
		if row := pendingState(t, external); row.status == status {
			return row
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("%s did not reach %s within %s, it is %s", external, status, resolveTimeout, pendingState(t, external).status)
	return pendingRow{}
}

// Scenario: A refund arriving before its bet is held as pending reference
//
//	Given no bet exists for the referenced external id
//	When the provider posts a REFUND referencing it
//	Then the response is 202 with status PENDING_REFERENCE
//	And WagerTransactionPendingReference is in the outbox
//	And the balance and the ledger are unchanged
func TestARefundArrivingBeforeItsBetIsHeldAsPendingReference(t *testing.T) {
	w := newWallet(t, "1000.00")
	before := walletState(t, w.ID)

	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, w.reversal("REFUND", "25.00", "bet-"+uuid.NewString())), http.StatusAccepted))

	if got.Status != "PENDING_REFERENCE" || got.IdempotentReplay || got.Balance != nil {
		t.Fatalf("response = %+v", got)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("a pending reversal changed the wallet: before %+v, after %+v", before, after)
	}
	if events := eventsOf(t, got.TransactionID); events["WagerTransactionPendingReference"] != 1 {
		t.Fatalf("events = %v", events)
	}
}

// Scenario: Replaying a pending operation returns the pending outcome
//
//	Given a refund held as PENDING_REFERENCE
//	When the same request is posted again
//	Then the response is 202 with idempotentReplay true and the same transaction id
//	And no second event is created
func TestReplayingAPendingOperationReturnsThePendingOutcome(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.reversal("REFUND", "25.00", "bet-"+uuid.NewString())
	first := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusAccepted))

	replay := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusAccepted))

	if !replay.IdempotentReplay || replay.TransactionID != first.TransactionID || replay.Status != "PENDING_REFERENCE" {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	if events := eventsOf(t, first.TransactionID); events["WagerTransactionPendingReference"] != 1 {
		t.Fatalf("events = %v", events)
	}
}

// Scenario: The inbox message of a pending reference is completed once the pendency is durable
//
//	Given a refund delivered over SQS whose reference is missing
//	When the pendency has been committed
//	Then the message is removed from the queue and the resolver owns the continuation
func TestTheInboxMessageOfAPendingReferenceIsCompletedOnceThePendencyIsDurable(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("REFUND", "25.00")

	stack.PublishMessage(t, w.ID, "dedup-"+uuid.NewString(),
		withReference(t, message.body, "bet-"+uuid.NewString()))

	got := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if got.status != "PENDING_REFERENCE" {
		t.Fatalf("transaction = %+v", got)
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("inbox rows %d, completed %v: the message must be handled once the wait is stored", rows, completed)
	}
	if dead := stack.DeadLettersContaining(message.external); len(dead) != 0 {
		t.Fatal("a pending reversal reached the dead letter queue")
	}
}

// Scenario: A pending reference is resolved when its bet finally arrives
//
//	Given a refund held as PENDING_REFERENCE
//	When the referenced bet is processed
//	Then the refund becomes PROCESSED and credits the wallet exactly once
func TestAPendingReferenceIsResolvedWhenItsBetFinallyArrives(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	refund := w.reversal("REFUND", "25.00", bet.ExternalTransactionID)
	core.RequireStatus(t, submit(t, refund), http.StatusAccepted)

	applied(t, bet)

	row := waitForStatus(t, refund.ExternalTransactionID, "PROCESSED")
	if row.failureCode != "" {
		t.Fatalf("failureCode = %q", row.failureCode)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 100000 || s.version != 3 || s.credits != 102500 {
		t.Fatalf("wallet = %+v: the refund must credit exactly once", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A pending reference expires and is rejected
//
//	Given a refund held as PENDING_REFERENCE whose wait is over
//	When the resolver looks again
//	Then it becomes REJECTED with failureCode REFERENCE_NOT_FOUND
//	And WagerTransactionRejected is in the outbox
func TestAPendingReferenceExpiresAndIsRejected(t *testing.T) {
	w := newWallet(t, "1000.00")
	refund := w.reversal("REFUND", "25.00", "bet-"+uuid.NewString())
	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, refund), http.StatusAccepted))

	row := waitForStatus(t, refund.ExternalTransactionID, "REJECTED")

	if row.failureCode != "REFERENCE_NOT_FOUND" {
		t.Fatalf("failureCode = %q", row.failureCode)
	}
	if events := eventsOf(t, got.TransactionID); events["WagerTransactionRejected"] != 1 {
		t.Fatalf("events = %v", events)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 100000 || s.version != 1 {
		t.Fatalf("an expired reversal changed the wallet: %+v", s)
	}
}

// Scenario: A reference that is itself pending keeps the reversal waiting
//
//	Given a refund held as PENDING_REFERENCE because its bet is missing
//	And a rollback of that refund
//	Then the rollback stays PENDING_REFERENCE while the refund is pending
//	When the bet arrives
//	Then the refund and then the rollback are applied
func TestAReferenceThatIsItselfPendingKeepsTheReversalWaiting(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	refund := w.reversal("REFUND", "25.00", bet.ExternalTransactionID)
	rollback := w.reversal("ROLLBACK", "25.00", refund.ExternalTransactionID)
	core.RequireStatus(t, submit(t, refund), http.StatusAccepted)
	core.RequireStatus(t, submit(t, rollback), http.StatusAccepted)

	time.Sleep(1500 * time.Millisecond)
	if row := pendingState(t, rollback.ExternalTransactionID); row.status != "PENDING_REFERENCE" || row.attempts == 0 {
		t.Fatalf("the rollback is %s with %d looks: its reference is only pending, it has to keep waiting", row.status, row.attempts)
	}

	applied(t, bet)

	waitForStatus(t, refund.ExternalTransactionID, "PROCESSED")
	waitForStatus(t, rollback.ExternalTransactionID, "PROCESSED")
	// bet -25, the refund +25, the rollback of the refund -25
	if s := walletState(t, w.ID); s.balanceMinor != 97500 {
		t.Fatalf("wallet = %+v", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: The retry state of a pending reference is stored on the transaction
//
//	Given a refund held as PENDING_REFERENCE
//	Then its attempts, its next look and its expiry are columns of the transaction
//	And they advance on their own while the reference is missing
func TestTheRetryStateOfAPendingReferenceIsStoredOnTheTransaction(t *testing.T) {
	w := newWallet(t, "1000.00")
	refund := w.reversal("REFUND", "25.00", "bet-"+uuid.NewString())
	core.RequireStatus(t, submit(t, refund), http.StatusAccepted)

	first := pendingState(t, refund.ExternalTransactionID)
	if first.nextAttempt == nil || first.expiresAt == nil || !first.expiresAt.After(*first.nextAttempt) {
		t.Fatalf("the wait was not stored: %+v", first)
	}

	time.Sleep(2 * time.Second)
	later := pendingState(t, refund.ExternalTransactionID)
	if later.status == "PENDING_REFERENCE" && (later.attempts <= first.attempts || !later.nextAttempt.After(*first.nextAttempt)) {
		t.Fatalf("the stored wait did not advance: first %+v, later %+v", first, later)
	}
}

// Scenario: Three workers resolving pending references apply each reversal once
//
//	Given three independent workers, each with its own connections and memory
//	And thirty refunds held as PENDING_REFERENCE
//	When their bets arrive
//	Then every refund is applied, and each one credited the wallet exactly once
func TestThreeWorkersResolvingPendingReferencesApplyEachReversalOnce(t *testing.T) {
	stack.StartPublisher(t, "worker-2")
	stack.StartPublisher(t, "worker-3")
	w := newWallet(t, "10000.00")

	const reversals = 30
	bets := make([]submitBody, 0, reversals)
	refunds := make([]submitBody, 0, reversals)
	for range reversals {
		bet := w.operation("BET", "10.00")
		refund := w.reversal("REFUND", "10.00", bet.ExternalTransactionID)
		core.RequireStatus(t, submit(t, refund), http.StatusAccepted)
		bets, refunds = append(bets, bet), append(refunds, refund)
	}
	for _, bet := range bets {
		applied(t, bet)
	}

	for _, refund := range refunds {
		waitForStatus(t, refund.ExternalTransactionID, "PROCESSED")
	}
	// every bet took 10.00 and every refund gave it back, so the wallet is where it started
	s := walletState(t, w.ID)
	if s.balanceMinor != 1000000 || s.credits != 1000000+reversals*1000 || s.debits != reversals*1000 {
		t.Fatalf("wallet = %+v: some refund was applied more or less than once", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// withReference adds the referenced transaction to the body of a queued message.
func withReference(t *testing.T, body, reference string) string {
	t.Helper()

	var envelope map[string]any
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("decode the message: %v", err)
	}
	data, _ := envelope["data"].(map[string]any)
	data["referenceExternalTransactionId"] = reference
	encoded, err := json.Marshal(envelope)
	if err != nil {
		t.Fatalf("encode the message: %v", err)
	}
	return string(encoded)
}
