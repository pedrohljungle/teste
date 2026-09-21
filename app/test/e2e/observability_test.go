//go:build e2e

package e2e

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Observability
//
//	As an operator, I want the metrics and the logs to say what the system is doing,
//	so that a failure can be found by the identifiers of what failed.
//
//	The suite reads the metrics and the log lines the application produces, in memory, and asserts on
//	them: a metric nobody proves is emitted is a dashboard that is empty on the day it is needed.
//
//	Scenarios:
//	  - Results are counted by kind, status and failure code
//	  - Replays and duplicates are counted
//	  - Payload conflicts are counted
//	  - Dead letters and retries are counted
//	  - Outbox publications and their delay are recorded
//	  - A reconciliation divergence is counted
//	  - Processing latency and the wait for the wallet lock are recorded
//	  - Logs carry the correlation id and the identifiers
//	  - Logs carry neither credentials nor amounts

// counted returns how much a metric moved while fn ran.
func counted(t *testing.T, name string, tags map[string]string, fn func()) float64 {
	t.Helper()
	before := stack.Metric(t, name, tags)
	fn()
	return stack.Metric(t, name, tags) - before
}

// Scenario: Results are counted by kind, status and failure code
//
//	When a bet is processed and another is rejected for insufficient funds
//	Then wager_transactions_total moved for BET PROCESSED and for BET REJECTED with INSUFFICIENT_FUNDS
func TestResultsAreCountedByKindStatusAndFailureCode(t *testing.T) {
	w := newWallet(t, "10.00")

	processed := counted(t, "wager_transactions_total", map[string]string{"kind": "BET", "status": "PROCESSED"}, func() {
		applied(t, w.operation("BET", "5.00"))
	})
	refused := counted(t, "wager_transactions_total",
		map[string]string{"kind": "BET", "status": "REJECTED", "failure_code": "INSUFFICIENT_FUNDS"}, func() {
			rejected(t, w.operation("BET", "25.00"))
		})

	if processed != 1 || refused != 1 {
		t.Fatalf("counted %v processed and %v rejected, want 1 and 1", processed, refused)
	}
}

// Scenario: Replays and duplicates are counted
//
//	When an operation is replayed over HTTP and a message is delivered twice over SQS
//	Then wager_idempotent_replays_total moved for http
//	And inbox_duplicates_total moved by one
func TestReplaysAndDuplicatesAreCounted(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := w.operation("BET", "25.00")
	applied(t, bet)

	replays := counted(t, "wager_idempotent_replays_total", map[string]string{"source": "http"}, func() {
		applied(t, bet)
	})

	message := w.queuedOperation("BET", "25.00")
	duplicates := counted(t, "inbox_duplicates_total", nil, func() {
		w.send(t, message)
		waitForTransaction(t, message.external)
		stack.WaitForEmptyQueue(t, consumeTimeout)
		w.send(t, message)
		sentinel := w.queuedOperation("LOSS", "0.00")
		w.send(t, sentinel)
		waitForTransaction(t, sentinel.external)
	})

	if replays != 1 || duplicates != 1 {
		t.Fatalf("counted %v replays and %v duplicates, want 1 and 1", replays, duplicates)
	}
}

// Scenario: Payload conflicts are counted
//
//	When the same key arrives with another payload
//	Then wager_payload_conflicts_total moved for http
func TestPayloadConflictsAreCounted(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	key := body.ProviderID + ":" + body.ExternalTransactionID
	core.RequireStatus(t, submitAs(t, core.ProviderA, key, body), http.StatusOK)

	conflicts := counted(t, "wager_payload_conflicts_total", map[string]string{"source": "http"}, func() {
		changed := body
		changed.Money.Amount = "30.00"
		core.RequireStatus(t, submitAs(t, core.ProviderA, key, changed), http.StatusConflict)
	})

	if conflicts != 1 {
		t.Fatalf("counted %v conflicts, want 1", conflicts)
	}
}

// Scenario: Dead letters and retries are counted
//
//	When a malformed message is given up on and another is left for redelivery
//	Then sqs_dead_letters_total moved for malformed
//	And sqs_message_retries_total moved
func TestDeadLettersAndRetriesAreCounted(t *testing.T) {
	deadLetters := counted(t, "sqs_dead_letters_total", map[string]string{"reason": "malformed"}, func() {
		marker := "not-json-" + uuid.NewString()
		stack.PublishMessage(t, "malformed", "dedup-"+uuid.NewString(), "{"+marker)
		stack.DeadLetterContaining(t, marker, consumeTimeout)
	})

	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	stack.Faults.FailStorageFor(message.key, 1)
	retries := counted(t, "sqs_message_retries_total", nil, func() {
		w.send(t, message)
		waitForTransaction(t, message.external)
	})

	if deadLetters != 1 || retries < 1 {
		t.Fatalf("counted %v dead letters and %v retries", deadLetters, retries)
	}
}

// Scenario: Outbox publications and their delay are recorded
//
//	When an event is refused by the broker once and then published
//	Then outbox_publish_attempts_total moved for failure and for success
//	And outbox_publish_delay_seconds recorded the delay
func TestOutboxPublicationsAndTheirDelayAreRecorded(t *testing.T) {
	opening := openWallet(t, "10.00")
	event := processedEvent(t, opening.Transaction)
	stack.Faults.FailPublishing(event.ID().String(), 1)

	var failures, successes, delays float64
	failures = counted(t, "outbox_publish_attempts_total", map[string]string{"result": "failure"}, func() {
		successes = counted(t, "outbox_publish_attempts_total", map[string]string{"result": "success"}, func() {
			delays = counted(t, "outbox_publish_delay_seconds", nil, func() {
				writeEvent(t, opening, event)
				stack.WaitForEvent(t, event.ID().String(), publishTimeout)
				waitUntilPublished(t, event.ID().String())
			})
		})
	})

	if failures < 1 || successes < 1 || delays < 1 {
		t.Fatalf("counted %v failures, %v successes and %v delays", failures, successes, delays)
	}
}

// Scenario: A reconciliation divergence is counted
//
//	When a wallet whose balance was tampered with is reconciled
//	Then reconciliation_divergences_total moved by one
func TestAReconciliationDivergenceIsCounted(t *testing.T) {
	w := newWallet(t, "1000.00")
	if _, err := stack.DB(t).Exec(context.Background(),
		"UPDATE wallets SET balance_minor = balance_minor + 100 WHERE id = $1", w.ID); err != nil {
		t.Fatal(err)
	}

	divergences := counted(t, "reconciliation_divergences_total", nil, func() { reconcile(t, w.ID) })
	healthy := counted(t, "reconciliations_total", map[string]string{"consistent": "true"}, func() {
		reconcile(t, newWallet(t, "5.00").ID)
	})

	if divergences != 1 || healthy != 1 {
		t.Fatalf("counted %v divergences and %v healthy reconciliations, want 1 and 1", divergences, healthy)
	}
}

// Scenario: Processing latency and the wait for the wallet lock are recorded
//
//	When a bet is processed
//	Then wager_processing_duration_seconds and wallet_lock_wait_seconds recorded an observation
func TestProcessingLatencyAndTheWaitForTheWalletLockAreRecorded(t *testing.T) {
	w := newWallet(t, "1000.00")

	latency := counted(t, "wager_processing_duration_seconds", map[string]string{"source": "http", "kind": "BET"}, func() {
		waits := counted(t, "wallet_lock_wait_seconds", nil, func() { applied(t, w.operation("BET", "1.00")) })
		if waits < 1 {
			t.Errorf("the wait for the wallet lock was not recorded")
		}
	})

	if latency != 1 {
		t.Fatalf("recorded %v latencies, want 1", latency)
	}
}

func linesNamed(message string) []core.LoggedLine {
	var found []core.LoggedLine
	for _, line := range stack.Logs() {
		if line.Message == message {
			found = append(found, line)
		}
	}
	return found
}

// Scenario: Logs carry the correlation id and the identifiers
//
//	When a bet is submitted under a request id and another arrives as a message
//	Then the line that concluded each carries the correlation id, the wallet, the provider and the
//	transaction, and the one from the queue carries the message id
func TestLogsCarryTheCorrelationIdAndTheIdentifiers(t *testing.T) {
	w := newWallet(t, "1000.00")
	requestID := "req-" + uuid.NewString()
	res := stack.RequestWithHeaders(t, http.MethodPost, "/wagering/transactions", stack.ClientToken(t, core.ProviderA),
		w.operation("BET", "25.00"), map[string]string{"Idempotency-Key": "k-" + uuid.NewString(), "X-Request-Id": requestID})
	stored := core.Decode[transactionResponse](t, core.KeepStatus(t, res, http.StatusOK))

	var concluded map[string]any
	for _, line := range linesNamed("wager transaction concluded") {
		if line.Fields["correlationId"] == requestID {
			concluded = line.Fields
		}
	}
	if concluded == nil {
		t.Fatalf("no concluding line carries the request id %s", requestID)
	}
	for field, want := range map[string]string{
		"walletId": w.ID, "providerId": core.ProviderA, "transactionId": stored.TransactionID, "status": "PROCESSED",
	} {
		if concluded[field] != want {
			t.Errorf("field %s = %v, want %s", field, concluded[field], want)
		}
	}

	message := w.queuedOperation("BET", "25.00")
	w.send(t, message)
	waitForTransaction(t, message.external)
	var queue map[string]any
	deadline := time.Now().Add(consumeTimeout)
	for queue == nil && time.Now().Before(deadline) {
		for _, line := range linesNamed("wager transaction concluded") {
			if line.Fields["messageId"] == message.messageID {
				queue = line.Fields
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	if queue == nil || queue["correlationId"] != message.messageID || queue["walletId"] != w.ID {
		t.Fatalf("the line of the queue message = %v", queue)
	}
}

// Scenario: Logs carry neither credentials nor amounts
//
//	When operations with a distinctive amount run under a real bearer token
//	Then no log line contains the token, the amount or the client secret
func TestLogsCarryNeitherCredentialsNorAmounts(t *testing.T) {
	w := newWallet(t, "1000.00")
	token := stack.ClientToken(t, core.ProviderA)
	amount := "123.45"
	applied(t, w.operation("BET", amount))
	rejected(t, w.operation("BET", "99999.99"))
	message := w.queuedOperation("BET", "777.77")
	w.send(t, message)
	waitForTransaction(t, message.external)

	secrets := []string{token[len(token)-24:], amount, "99999.99", "777.77", "provider-a-secret-local", "wallet-service-secret-local"}
	for _, line := range stack.Logs() {
		rendered := line.Message + " " + fmt.Sprint(line.Fields)
		for _, secret := range secrets {
			if strings.Contains(rendered, secret) {
				t.Fatalf("a log line contains %q: %s", secret, rendered)
			}
		}
	}
}
