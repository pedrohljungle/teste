//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Recovery after interruption
//
//	As an operator, I want a killed process to cost nothing but a retry,
//	so that no restart invents or loses money.
//
//	Scenarios:
//	  - A consumer killed after the commit and before the ack does not apply twice
//	  - A pending transaction is resumed by another instance
//	  - A restart preserves idempotency, pendencies and financial consistency
//	  - SIGTERM stops fetching and finishes the message in flight

// Scenario: A consumer killed after the commit and before the ack does not apply twice
//
//	Given a bet consumed and committed
//	When the worker is killed before deleting the message
//	And the message is redelivered
//	Then the inbox refuses it and no second ledger entry exists
//	And the message is finally removed from the queue
func TestAConsumerKilledAfterTheCommitAndBeforeTheAckDoesNotApplyTwice(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	// The commit happens, the delete does not: what a process killed in between leaves behind.
	stack.Faults.FailAcknowledging(message.messageID, 1)

	duplicates := counted(t, "inbox_duplicates_total", nil, func() {
		w.send(t, message)
		waitForTransaction(t, message.external)

		deadline := time.Now().Add(consumeTimeout)
		for stack.Faults.Deliveries(message.messageID) < 2 && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		stack.WaitForEmptyQueue(t, consumeTimeout)
	})

	if got := stack.Faults.Deliveries(message.messageID); got < 2 {
		t.Fatalf("the message was delivered %d times, the queue never gave it back", got)
	}
	if duplicates < 1 {
		t.Fatalf("the inbox refused %v redeliveries, want at least 1", duplicates)
	}
	state := walletState(t, w.ID)
	if state.balanceMinor != 97500 || state.entries != 2 || transactionsOf(t, w.ID) != 1 {
		t.Fatalf("state = %+v, transactions = %d: the redelivery was applied again", state, transactionsOf(t, w.ID))
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("inbox rows %d, completed %v", rows, completed)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// holdExpiryFar moves the deadline of a pending reference out of the way of the test: the suite
// runs with a TTL of seconds so that expiry scenarios are quick, and a scenario about surviving
// something slow must not race it.
func holdExpiryFar(t *testing.T, external string) {
	t.Helper()

	if _, err := stack.DB(t).Exec(context.Background(), `
		UPDATE wager_transactions SET reference_expires_at = now() + interval '1 hour'
		WHERE provider_id = $1 AND external_transaction_id = $2`, core.ProviderA, external); err != nil {
		t.Fatalf("move the expiry: %v", err)
	}
}

// Scenario: A pending transaction is resumed by another instance
//
//	Given a refund committed as PENDING_REFERENCE by an instance that then died
//	When its bet arrives and another instance sweeps the pending work
//	Then the refund reaches a terminal state exactly once
func TestAPendingTransactionIsResumedByAnotherInstance(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	refund := w.reversal("REFUND", "25.00", bet.ExternalTransactionID)
	token := stack.ClientToken(t, core.ProviderA)

	dying := stack.StartServer(t, "instance-that-dies")
	first := call(dying.BaseURL, token, refund)
	if first.err != nil || first.status != http.StatusAccepted || first.response.Status != "PENDING_REFERENCE" {
		t.Fatalf("the refund answered %d %+v (%v)", first.status, first.response, first.err)
	}
	holdExpiryFar(t, refund.ExternalTransactionID)
	dying.Stop()

	stack.StartPublisher(t, "instance-that-resumes")
	applied(t, bet)

	row := waitForStatus(t, refund.ExternalTransactionID, "PROCESSED")
	if row.failureCode != "" {
		t.Fatalf("the refund ended as %+v", row)
	}
	state := walletState(t, w.ID)
	// The opening, the bet debit and the refund credit, each once.
	if state.entries != 3 || state.balanceMinor != 100000 {
		t.Fatalf("state = %+v: the refund was not applied exactly once", state)
	}
	if events := eventsOf(t, first.response.TransactionID); events["WagerTransactionProcessed"] != 1 {
		t.Fatalf("events = %v", events)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A restart preserves idempotency, pendencies and financial consistency
//
//	Given a mixed workload of processed, rejected and pending-reference operations
//	When every process is restarted
//	Then replays still return the original results
//	And the pendency is still there with its attempts and next attempt, and still resolves
//	And every wallet balance still equals its ledger sum
func TestARestartPreservesIdempotencyPendenciesAndFinancialConsistency(t *testing.T) {
	w := newWallet(t, "100.00")
	processed := w.operation("BET", "30.00")
	refused := w.operation("BET", "500.00")
	awaited := w.operation("BET", "20.00")
	refund := w.reversal("REFUND", "20.00", awaited.ExternalTransactionID)

	original := applied(t, processed)
	turnedDown := rejected(t, refused)
	pending := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, refund), http.StatusAccepted))
	holdExpiryFar(t, refund.ExternalTransactionID)
	before := walletState(t, w.ID)
	// Let the resolver look at it a few times, so there is a retry state worth preserving.
	waitUntil(t, "the resolver to look at the pendency", func() bool {
		return pendingState(t, refund.ExternalTransactionID).attempts >= 2
	})
	attemptsBefore := pendingState(t, refund.ExternalTransactionID).attempts

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if err := stack.Restart(ctx); err != nil {
		t.Fatalf("restart: %v", err)
	}

	replayed := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, processed), http.StatusOK))
	if !replayed.IdempotentReplay || replayed.TransactionID != original.TransactionID || *replayed.Balance != *original.Balance {
		t.Fatalf("the replay after the restart = %+v, the original = %+v", replayed, original)
	}
	repeated := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, refused), http.StatusUnprocessableEntity))
	if !repeated.IdempotentReplay || repeated.TransactionID != turnedDown.TransactionID || repeated.FailureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("the replayed rejection = %+v, the original = %+v", repeated, turnedDown)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("replaying after the restart changed the wallet: before %+v, after %+v", before, after)
	}
	if row := pendingState(t, refund.ExternalTransactionID); row.status != "PENDING_REFERENCE" || row.expiresAt == nil ||
		row.nextAttempt == nil || row.attempts < attemptsBefore {
		t.Fatalf("the pendency did not survive the restart with its retry state (%d attempts before): %+v", attemptsBefore, row)
	}
	if again := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, refund), http.StatusAccepted)); again.TransactionID != pending.TransactionID {
		t.Fatalf("the pending refund is another transaction after the restart: %+v", again)
	}

	applied(t, awaited)
	waitForStatus(t, refund.ExternalTransactionID, "PROCESSED")

	// 100 - 30 (processed) - 20 (awaited bet) + 20 (its refund).
	if state := walletState(t, w.ID); state.balanceMinor != 7000 {
		t.Fatalf("state = %+v, want balance 7000", state)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: SIGTERM stops fetching and finishes the message in flight
//
//	Given a worker process consuming a queue, with a message being processed
//	When the worker receives SIGTERM
//	Then it stops polling: a message that arrives afterwards is left in the queue
//	And it finishes the message in flight, exits with status 0 and has not been killed
func TestSIGTERMStopsFetchingAndFinishesTheMessageInFlight(t *testing.T) {
	worker := stack.StartWorkerProcess(t)

	// The message is held in flight by a writer that owns the wallet: the handler waits for the
	// lock, which is exactly what a slow message looks like.
	held := newWallet(t, "100.00")
	tx, err := stack.DB(t).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), "SELECT id FROM wallets WHERE id = $1 FOR UPDATE", held.ID); err != nil {
		t.Fatalf("lock the wallet: %v", err)
	}
	inFlight := held.queuedOperation("BET", "10.00")
	worker.Send(t, held.ID, inFlight.body)
	waitUntil(t, "the message to be received", func() bool {
		_, hidden := worker.QueueState(t)
		return hidden == 1
	})

	worker.Terminate(t)
	// The signal takes a moment to be handled; a message sent in that instant races the shutdown
	// and proves nothing about it.
	time.Sleep(1500 * time.Millisecond)

	// A message that arrives after the signal has to stay where it is. The poll window of the worker
	// is a second, so a couple of them is more than enough for a live consumer to have taken it.
	later := newWallet(t, "100.00")
	afterwards := later.queuedOperation("BET", "10.00")
	worker.Send(t, later.ID, afterwards.body)
	time.Sleep(4 * time.Second)
	if !worker.Running() {
		t.Fatalf("the worker exited while a message was still in flight\n%s", worker.Output())
	}
	if visible, _ := worker.QueueState(t); visible != 1 {
		t.Fatalf("%d messages waiting: the worker kept fetching after SIGTERM\n%s", visible, worker.Output())
	}
	if state := walletState(t, held.ID); state.entries != 1 {
		t.Fatalf("the message in flight was applied while its wallet was locked: %+v", state)
	}

	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	if code := worker.WaitForExit(t, 30*time.Second); code != 0 {
		t.Fatalf("the worker exited with status %d\n%s", code, worker.Output())
	}

	if state := walletState(t, held.ID); state.balanceMinor != 9000 || state.entries != 2 {
		t.Fatalf("the message in flight was not finished before the exit: %+v\n%s", state, worker.Output())
	}
	if state := walletState(t, later.ID); state.entries != 1 {
		t.Fatalf("the message that arrived after SIGTERM was consumed: %+v", state)
	}
	if visible, hidden := worker.QueueState(t); visible != 1 || hidden != 0 {
		t.Fatalf("queue = %d visible, %d in flight: the finished message was not deleted", visible, hidden)
	}
	if strings.Contains(worker.Output(), "panic") {
		t.Fatalf("the worker panicked on the way out\n%s", worker.Output())
	}
	requireLedgerMatchesBalance(t, held.ID)
}

// waitUntil polls a condition, failing the test when it does not hold in time.
func waitUntil(t *testing.T, what string, condition func() bool) {
	t.Helper()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}
