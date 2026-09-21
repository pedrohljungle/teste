//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Consuming an operation from SQS
//
//	As the worker, I want a message applied once and removed only after its commit,
//	so that at-least-once delivery never becomes at-least-once money.
//
//	The queue is a real FIFO queue, the dead letter queue is a real queue, and the consumer is the
//	one cmd/worker registers. Redeliveries are provoked by sending the same body under different
//	deduplication ids, because the queue itself drops a repeated one inside its window and would
//	hide what the scenario is about: the application has to deduplicate on its own.
//
//	Scenarios:
//	  - A bet delivered over SQS is processed exactly once
//	  - The same message delivered twice produces a single movement
//	  - A redelivery carrying a different body under the same message id is refused
//	  - The message is removed from the queue only after the commit
//	  - A confirmed business rejection removes the message from the queue
//	  - A malformed body is sent to the dead letter queue and never retried
//	  - A transient storage failure leaves the message for redelivery
//	  - A message that fails every time lands in the dead letter queue
//	  - The OPENING kind delivered over SQS is refused
//	  - The same operation over HTTP and then over SQS is applied once
//	  - The same operation over SQS and then over HTTP is applied once

const consumeTimeout = 40 * time.Second

type messageEnvelope struct {
	MessageID  string      `json:"messageId"`
	Type       string      `json:"type"`
	OccurredAt string      `json:"occurredAt"`
	Data       messageData `json:"data"`
}

type messageData struct {
	ProviderID            string    `json:"providerId"`
	ExternalTransactionID string    `json:"externalTransactionId"`
	IdempotencyKey        string    `json:"idempotencyKey"`
	PlayerID              string    `json:"playerId"`
	WalletID              string    `json:"walletId"`
	RoundID               string    `json:"roundId"`
	GameID                string    `json:"gameId"`
	Kind                  string    `json:"kind"`
	Money                 moneyBody `json:"money"`
}

// queued is an operation together with the body that carries it over SQS.
type queued struct {
	messageID string
	external  string
	key       string
	body      string
}

// queuedOperation builds the message of an operation on the wallet, from provider-a.
func (w wallet) queuedOperation(kind, amount string) queued {
	external := "ext-" + uuid.NewString()
	return w.queuedWith("msg-"+uuid.NewString(), external, kind, amount)
}

func (w wallet) queuedWith(messageID, external, kind, amount string) queued {
	key := core.ProviderA + ":" + external
	body, err := json.Marshal(messageEnvelope{
		MessageID:  messageID,
		Type:       "WagerTransactionRequested",
		OccurredAt: time.Now().UTC().Format("2006-01-02T15:04:05.000Z"),
		Data: messageData{
			ProviderID: core.ProviderA, ExternalTransactionID: external, IdempotencyKey: key,
			PlayerID: w.PlayerID, WalletID: w.ID, RoundID: "round-987", GameID: "fortune-chimp",
			Kind: kind, Money: moneyBody{Amount: amount, Currency: "BRL"},
		},
	})
	if err != nil {
		panic(err)
	}
	return queued{messageID: messageID, external: external, key: key, body: string(body)}
}

// send publishes a message as a producer does: the group is the wallet, and the deduplication id is
// fresh, so the queue never hides a resend from the application.
func (w wallet) send(t *testing.T, message queued) {
	t.Helper()
	stack.PublishMessage(t, w.ID, "dedup-"+uuid.NewString(), message.body)
}

type storedTransaction struct {
	id, status, failureCode string
}

// waitForTransaction blocks until the consumer stored a transaction for the provider's operation.
func waitForTransaction(t *testing.T, external string) storedTransaction {
	t.Helper()

	deadline := time.Now().Add(consumeTimeout)
	for time.Now().Before(deadline) {
		var found storedTransaction
		var code *string
		err := stack.DB(t).QueryRow(context.Background(), `
			SELECT id::text, status, failure_code FROM wager_transactions
			WHERE provider_id = $1 AND external_transaction_id = $2`, core.ProviderA, external).
			Scan(&found.id, &found.status, &code)
		if err == nil {
			if code != nil {
				found.failureCode = *code
			}
			return found
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no transaction was stored for %s within %s", external, consumeTimeout)
	return storedTransaction{}
}

func inboxRows(t *testing.T, messageID string) (int, bool) {
	t.Helper()

	var rows, completedRows int
	if err := stack.DB(t).QueryRow(context.Background(), `
		SELECT count(*), count(completed_at) FROM inbox_messages
		WHERE consumer_name = 'wager-transactions' AND message_id = $1`, messageID).Scan(&rows, &completedRows); err != nil {
		t.Fatalf("read the inbox: %v", err)
	}
	return rows, completedRows == rows && rows > 0
}

// Scenario: A bet delivered over SQS is processed exactly once
//
//	Given a wallet with balance 1000.00 BRL
//	When a WagerTransactionRequested message carrying a BET of 25.00 BRL is published
//	Then the balance becomes 975.00 with exactly one DEBIT entry
//	And the inbox holds the message id with a completion
//	And the queue is empty
func TestABetDeliveredOverSQSIsProcessedExactlyOnce(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")

	w.send(t, message)

	got := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if got.status != "PROCESSED" {
		t.Fatalf("transaction = %+v", got)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 97500 || s.version != 2 || s.debits != 2500 {
		t.Fatalf("wallet = %+v", s)
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("inbox rows %d, completed %v", rows, completed)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: The same message delivered twice produces a single movement
//
//	Given a bet already consumed from the queue
//	When the very same message id is delivered again, under another deduplication id
//	Then the inbox refuses it as a duplicate
//	And no second ledger entry and no second event are created
func TestTheSameMessageDeliveredTwiceProducesASingleMovement(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	w.send(t, message)
	first := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)

	w.send(t, message)
	// The queue is FIFO per wallet, so once a message sent after the duplicate is processed the
	// duplicate has necessarily been received and handled before it. Waiting for the queue to look
	// empty would prove nothing: its count lags, and a duplicate not yet received looks the same.
	sentinel := w.queuedOperation("LOSS", "0.00")
	w.send(t, sentinel)
	waitForTransaction(t, sentinel.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)

	if s := walletState(t, w.ID); s.debits != 2500 || s.balanceMinor != 97500 || s.version != 2 {
		t.Fatalf("the redelivery moved the wallet: %+v", s)
	}
	if got := transactionsOf(t, w.ID); got != 2 {
		t.Fatalf("%d transactions exist, want the bet and the sentinel", got)
	}
	if rows, _ := inboxRows(t, message.messageID); rows != 1 {
		t.Fatalf("the inbox holds %d records of one message", rows)
	}
	if events := eventsOf(t, first.id); events["WagerTransactionProcessed"] != 1 {
		t.Fatalf("events = %v", events)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A redelivery carrying a different body under the same message id is refused
//
//	Given a bet already consumed from the queue
//	When a message with the same id and a different payload arrives
//	Then it is sent to the dead letter queue and applies nothing
//	And the inbox still holds the first message
func TestARedeliveryCarryingADifferentBodyUnderTheSameMessageIdIsRefused(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	w.send(t, message)
	waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)

	impostor := w.queuedWith(message.messageID, "ext-"+uuid.NewString(), "BET", "99.00")
	w.send(t, impostor)

	dead := stack.DeadLetterContaining(t, impostor.external, consumeTimeout)
	if !strings.Contains(dead.Attributes["failureReason"], "message id was already handled with other content") {
		t.Fatalf("failureReason = %q", dead.Attributes["failureReason"])
	}
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if s := walletState(t, w.ID); s.debits != 2500 || s.balanceMinor != 97500 {
		t.Fatalf("the impostor moved the wallet: %+v", s)
	}
	if got := transactionsOf(t, w.ID); got != 1 {
		t.Fatalf("%d transactions exist, want only the first", got)
	}
}

// Scenario: The message is removed from the queue only after the commit
//
//	Given a bet whose wallet is locked by another writer
//	When the message is published
//	Then it is received but not deleted, and nothing is stored
//	When the lock is released and the bet commits
//	Then the message is deleted
func TestTheMessageIsRemovedFromTheQueueOnlyAfterTheCommit(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	walletID := uuid.MustParse(w.ID)

	locked, release, finished := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		finished <- stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
			if _, err := stack.Repos.Wallets.GetForUpdate(ctx, walletID); err != nil {
				return err
			}
			close(locked)
			<-release
			return nil
		})
	}()
	<-locked

	w.send(t, message)
	time.Sleep(900 * time.Millisecond)

	visible, inFlight := stack.QueueState(t)
	if inFlight != 1 || visible != 0 {
		t.Fatalf("queue state: %d visible, %d in flight; the message must be received and still not deleted", visible, inFlight)
	}
	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d transactions were stored before the wallet lock was released", got)
	}

	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("the lock holder failed: %v", err)
	}
	if got := waitForTransaction(t, message.external); got.status != "PROCESSED" {
		t.Fatalf("transaction = %+v", got)
	}
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if s := walletState(t, w.ID); s.debits != 2500 {
		t.Fatalf("wallet = %+v", s)
	}
}

// Scenario: A confirmed business rejection removes the message from the queue
//
//	Given a wallet with balance 10.00 BRL
//	When a BET of 25.00 BRL arrives over the queue
//	Then the transaction is REJECTED, the message is deleted and the dead letter queue stays empty
func TestAConfirmedBusinessRejectionRemovesTheMessageFromTheQueue(t *testing.T) {
	w := newWallet(t, "10.00")
	message := w.queuedOperation("BET", "25.00")

	w.send(t, message)

	got := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if got.status != "REJECTED" || got.failureCode != "INSUFFICIENT_FUNDS" {
		t.Fatalf("transaction = %+v", got)
	}
	time.Sleep(1500 * time.Millisecond)
	if dead := stack.DeadLettersContaining(message.external); len(dead) != 0 {
		t.Fatalf("a business rejection reached the dead letter queue: %v", dead)
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("inbox rows %d, completed %v", rows, completed)
	}
}

// Scenario: A malformed body is sent to the dead letter queue and never retried
//
//	When a message whose body is not valid JSON arrives
//	Then it reaches the dead letter queue with the reason
//	And it is removed from the main queue and delivered nowhere else
func TestAMalformedBodyIsSentToTheDeadLetterQueueAndNeverRetried(t *testing.T) {
	marker := "not-json-" + uuid.NewString()
	stack.PublishMessage(t, "malformed", "dedup-"+uuid.NewString(), "{"+marker)

	dead := stack.DeadLetterContaining(t, marker, consumeTimeout)

	if !strings.Contains(dead.Attributes["failureReason"], "malformed message") {
		t.Fatalf("failureReason = %q", dead.Attributes["failureReason"])
	}
	stack.WaitForEmptyQueue(t, consumeTimeout)
	time.Sleep(2500 * time.Millisecond)
	if got := len(stack.DeadLettersContaining(marker)); got != 1 {
		t.Fatalf("the poison message reached the dead letter queue %d times, want once: it was retried", got)
	}
}

// Scenario: A transient storage failure leaves the message for redelivery
//
//	Given storage that is unavailable for the next two attempts at the operation
//	When a bet arrives over the queue
//	Then the message is not deleted and reappears after the visibility timeout
//	And once the storage answers it is processed exactly once
func TestATransientStorageFailureLeavesTheMessageForRedelivery(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	stack.Faults.FailStorageFor(message.key, 2)

	w.send(t, message)

	got := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if got.status != "PROCESSED" {
		t.Fatalf("transaction = %+v", got)
	}
	if s := walletState(t, w.ID); s.debits != 2500 || s.balanceMinor != 97500 || s.version != 2 {
		t.Fatalf("wallet = %+v: the retried message must apply exactly once", s)
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("inbox rows %d, completed %v", rows, completed)
	}
	if dead := stack.DeadLettersContaining(message.external); len(dead) != 0 {
		t.Fatal("a message that succeeded on redelivery reached the dead letter queue")
	}
}

// Scenario: A message that fails every time lands in the dead letter queue
//
//	Given storage that is unavailable for every attempt at the operation
//	When a bet arrives over the queue
//	And it has been received more than maxReceiveCount times
//	Then SQS moves it to the dead letter queue
//	And nothing was stored
func TestAMessageThatFailsEveryTimeLandsInTheDeadLetterQueue(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	stack.Faults.FailStorageFor(message.key, 1000)
	t.Cleanup(func() { stack.Faults.FailStorageFor(message.key, 0) })

	w.send(t, message)

	stack.DeadLetterContaining(t, message.external, 90*time.Second)
	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d transactions were stored by a message that never succeeded", got)
	}
	if rows, _ := inboxRows(t, message.messageID); rows != 0 {
		t.Fatalf("the inbox holds %d records of a message that never committed", rows)
	}
}

// Scenario: The OPENING kind delivered over SQS is refused
//
//	When a message carrying kind OPENING arrives
//	Then it reaches the dead letter queue with OPENING_NOT_ALLOWED
//	And the wallet is untouched
func TestTheOpeningKindDeliveredOverSQSIsRefused(t *testing.T) {
	w := newWallet(t, "100.00")
	before := walletState(t, w.ID)
	message := w.queuedOperation(string(entities.KindOpening), "500.00")

	w.send(t, message)

	dead := stack.DeadLetterContaining(t, message.external, consumeTimeout)
	if !strings.Contains(dead.Attributes["failureReason"], "OPENING") {
		t.Fatalf("failureReason = %q", dead.Attributes["failureReason"])
	}
	stack.WaitForEmptyQueue(t, consumeTimeout)
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("an OPENING changed the wallet: before %+v, after %+v", before, after)
	}
	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d external transactions were stored for an OPENING", got)
	}
}

// Scenario: The same operation over HTTP and then over SQS is applied once
//
//	Given a bet processed over HTTP
//	When the same operation arrives over the queue as a new message
//	Then it is recognised as a replay and applies nothing
//	And the message is recorded as handled and removed from the queue
func TestTheSameOperationOverHTTPAndThenOverSQSIsAppliedOnce(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	res := submitAs(t, core.ProviderA, message.key, submitBody{
		ProviderID: core.ProviderA, ExternalTransactionID: message.external, PlayerID: w.PlayerID, WalletID: w.ID,
		RoundID: "round-987", GameID: "fortune-chimp", Kind: "BET", Money: moneyBody{Amount: "25.00", Currency: "BRL"},
	})
	core.RequireStatus(t, res, http.StatusOK)

	w.send(t, message)

	stack.WaitForEmptyQueue(t, consumeTimeout)
	deadline := time.Now().Add(consumeTimeout)
	for time.Now().Before(deadline) {
		if rows, completed := inboxRows(t, message.messageID); rows == 1 && completed {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rows, completed := inboxRows(t, message.messageID); rows != 1 || !completed {
		t.Fatalf("the message was not recorded as handled: %d rows, completed %v", rows, completed)
	}
	if s := walletState(t, w.ID); s.debits != 2500 || s.balanceMinor != 97500 || s.version != 2 {
		t.Fatalf("the operation was applied more than once: %+v", s)
	}
	if got := transactionsOf(t, w.ID); got != 1 {
		t.Fatalf("%d transactions exist for one operation", got)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: The same operation over SQS and then over HTTP is applied once
//
//	Given a bet processed over the queue
//	When the same operation is posted over HTTP under the same key
//	Then the response is 200 with idempotentReplay true and the original transaction id
//	And the ledger still holds a single debit
func TestTheSameOperationOverSQSAndThenOverHTTPIsAppliedOnce(t *testing.T) {
	w := newWallet(t, "1000.00")
	message := w.queuedOperation("BET", "25.00")
	w.send(t, message)
	first := waitForTransaction(t, message.external)
	stack.WaitForEmptyQueue(t, consumeTimeout)

	res := submitAs(t, core.ProviderA, message.key, submitBody{
		ProviderID: core.ProviderA, ExternalTransactionID: message.external, PlayerID: w.PlayerID, WalletID: w.ID,
		RoundID: "round-987", GameID: "fortune-chimp", Kind: "BET", Money: moneyBody{Amount: "25.00", Currency: "BRL"},
	})
	got := core.Decode[transactionResponse](t, core.KeepStatus(t, res, http.StatusOK))

	if !got.IdempotentReplay || got.TransactionID != first.id || got.Balance == nil || got.Balance.Amount != "975.00" {
		t.Fatalf("response = %+v, want a replay of %s at 975.00", got, first.id)
	}
	if s := walletState(t, w.ID); s.debits != 2500 || s.version != 2 {
		t.Fatalf("the operation was applied more than once: %+v", s)
	}
	requireLedgerMatchesBalance(t, w.ID)
}
