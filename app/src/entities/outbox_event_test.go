package entities

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func decode(t *testing.T, e *OutboxEvent) map[string]any {
	t.Helper()
	var envelope map[string]any
	if err := json.Unmarshal(e.Payload(), &envelope); err != nil {
		t.Fatalf("the payload is not JSON: %v\n%s", err, e.Payload())
	}
	return envelope
}

func data(t *testing.T, e *OutboxEvent) map[string]any {
	t.Helper()
	fields, ok := decode(t, e)["data"].(map[string]any)
	if !ok {
		t.Fatalf("the envelope has no data object: %s", e.Payload())
	}
	return fields
}

func TestAProcessedEventCarriesTheEnvelopeAndIsDefinedByItsConstructor(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))

	event, err := NewWagerTransactionProcessedEvent(id(20), tx, "corr-1", tx.ID().String(), t0.Add(1500*time.Millisecond))
	if err != nil {
		t.Fatalf("NewWagerTransactionProcessedEvent: %v", err)
	}

	if event.Type() != EventWagerTransactionProcessed || event.Version() != 1 || event.ID() != id(20) ||
		event.AggregateType() != AggregateWagerTransaction || event.AggregateID() != tx.ID() {
		t.Fatalf("event = %+v", event.Snapshot())
	}
	if event.Status() != OutboxPending || event.Attempts() != 0 || !event.NextAttemptAt().Equal(t0.Add(1500*time.Millisecond)) {
		t.Fatalf("status %s, attempts %d, next %s", event.Status(), event.Attempts(), event.NextAttemptAt())
	}

	envelope := decode(t, event)
	want := map[string]any{
		"eventId":       id(20).String(),
		"eventType":     "WagerTransactionProcessed",
		"aggregateId":   tx.ID().String(),
		"correlationId": "corr-1",
		"causationId":   tx.ID().String(),
		"occurredAt":    "2026-09-08T12:00:01.500Z",
		"version":       float64(1),
	}
	for key, value := range want {
		if envelope[key] != value {
			t.Errorf("envelope[%q] = %v, want %v", key, envelope[key], value)
		}
	}
	fields := data(t, event)
	if fields["kind"] != "BET" || fields["providerId"] != "provider-a" || fields["externalTransactionId"] != "transaction-123" {
		t.Errorf("data = %v", fields)
	}
	money, _ := fields["money"].(map[string]any)
	if money["amount"] != "25.00" || money["currency"] != "BRL" {
		t.Errorf("money = %v", fields["money"])
	}
}

func TestAnEventMoneyIsAlwaysADecimalString(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))
	event, err := NewWagerTransactionProcessedEvent(id(20), tx, "corr-1", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	// A JSON number would already be a float on the consumer's side.
	if got := string(event.Payload()); !strings.Contains(got, `"amount":"25.00"`) {
		t.Fatalf("the amount is not a string: %s", got)
	}
}

func TestALossProducesTheProcessedEventAndNoBalanceEvent(t *testing.T) {
	loss := newTransaction(t, func(op *ExternalOperation) { op.Kind, op.Money = "LOSS", brl(t, "0.00") })
	if err := loss.MarkProcessed(brl(t, "100.00"), 3, t0); err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionProcessedEvent(id(20), loss, "corr-1", "", t0)
	if err != nil {
		t.Fatalf("a LOSS is a fact even though it moves no balance: %v", err)
	}
	if data(t, event)["kind"] != "LOSS" {
		t.Fatalf("data = %v", data(t, event))
	}
	if loss.MovesBalance() {
		t.Fatal("a LOSS moves no balance, so no WalletBalanceChanged can follow from it")
	}
}

func TestARejectedEventCarriesTheFailureCode(t *testing.T) {
	tx := newTransaction(t, nil)
	if err := tx.MarkRejected(FailureInsufficientFunds, t0); err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionRejectedEvent(id(21), tx, "corr-1", "", t0)
	if err != nil {
		t.Fatalf("NewWagerTransactionRejectedEvent: %v", err)
	}
	if event.Type() != EventWagerTransactionRejected || data(t, event)["failureCode"] != "INSUFFICIENT_FUNDS" {
		t.Fatalf("type %s, data %v", event.Type(), data(t, event))
	}
	if _, present := decode(t, event)["causationId"]; present {
		t.Fatal("an absent causation id must be omitted")
	}
}

func TestAPendingReferenceEventCarriesTheReferenceAndTheExpiry(t *testing.T) {
	tx := newReversal(t)
	if err := tx.MarkPendingReference(t0.Add(time.Minute), t0.Add(24*time.Hour), t0); err != nil {
		t.Fatal(err)
	}

	event, err := NewWagerTransactionPendingReferenceEvent(id(22), tx, "corr-1", "", t0)
	if err != nil {
		t.Fatalf("NewWagerTransactionPendingReferenceEvent: %v", err)
	}
	fields := data(t, event)
	if event.Type() != EventWagerTransactionPendingReference ||
		fields["referenceExternalTransactionId"] != "transaction-123" || fields["expiresAt"] != "2026-09-09T12:00:00.000Z" {
		t.Fatalf("type %s, data %v", event.Type(), fields)
	}
}

func TestABalanceChangedEventCarriesTheWholeMovement(t *testing.T) {
	wallet := newWallet(t, "1000.00")
	entry, err := wallet.Debit(id(30), id(31), brl(t, "25.00"), t0)
	if err != nil {
		t.Fatal(err)
	}

	event, err := NewWalletBalanceChangedEvent(id(23), entry, wallet.Version(), "corr-1", id(31).String(), t0)
	if err != nil {
		t.Fatalf("NewWalletBalanceChangedEvent: %v", err)
	}

	if event.Type() != EventWalletBalanceChanged || event.AggregateType() != AggregateWallet || event.AggregateID() != wallet.ID() {
		t.Fatalf("event = %+v", event.Snapshot())
	}
	fields := data(t, event)
	for key, want := range map[string]any{
		"walletId": wallet.ID().String(), "transactionId": id(31).String(), "direction": "DEBIT", "walletVersion": float64(2),
	} {
		if fields[key] != want {
			t.Errorf("data[%q] = %v, want %v", key, fields[key], want)
		}
	}
	for key, want := range map[string]string{"money": "25.00", "balanceBefore": "1000.00", "balanceAfter": "975.00"} {
		amount, _ := fields[key].(map[string]any)
		if amount["amount"] != want || amount["currency"] != "BRL" {
			t.Errorf("data[%q] = %v, want %s BRL", key, fields[key], want)
		}
	}
}

func TestTheOpeningEventsCarryNoExternalMetadata(t *testing.T) {
	opening, err := OpenWallet(OpeningIDs{Wallet: id(2), Transaction: id(3), Entry: id(4)}, id(1), brl(t, "1000.00"), t0)
	if err != nil {
		t.Fatal(err)
	}

	processedEvent, err := NewWagerTransactionProcessedEvent(id(20), opening.Transaction, "corr-1", "", t0)
	if err != nil {
		t.Fatalf("processed: %v", err)
	}
	changedEvent, err := NewWalletBalanceChangedEvent(id(21), *opening.Entry, opening.Wallet.Version(), "corr-1", "", t0)
	if err != nil {
		t.Fatalf("balance changed: %v", err)
	}

	fields := data(t, processedEvent)
	for _, absent := range []string{"providerId", "externalTransactionId", "roundId", "gameId"} {
		if _, present := fields[absent]; present {
			t.Errorf("an internal opening must not report %s: %v", absent, fields)
		}
	}
	if fields["kind"] != "OPENING" {
		t.Errorf("kind = %v", fields["kind"])
	}
	changed := data(t, changedEvent)
	if changed["walletVersion"] != float64(1) || changed["direction"] != "CREDIT" {
		t.Errorf("balance changed = %v", changed)
	}
	before, _ := changed["balanceBefore"].(map[string]any)
	after, _ := changed["balanceAfter"].(map[string]any)
	if before["amount"] != "0.00" || after["amount"] != "1000.00" {
		t.Errorf("balances = %v -> %v", before, after)
	}
}

func TestAnEventPayloadIsAnImmutableSnapshot(t *testing.T) {
	wallet := newWallet(t, "1000.00")
	entry, err := wallet.Debit(id(30), id(31), brl(t, "25.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	event, err := NewWalletBalanceChangedEvent(id(23), entry, wallet.Version(), "corr-1", "", t0)
	if err != nil {
		t.Fatal(err)
	}
	stored := event.Payload()

	// Later movements change the wallet and never the event that was already written.
	if _, err := wallet.Debit(id(32), id(33), brl(t, "100.00"), t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if string(event.Payload()) != string(stored) {
		t.Fatalf("the payload changed after a later movement:\n before %s\n after  %s", stored, event.Payload())
	}

	// The copy handed out cannot be used to rewrite the stored one either.
	handedOut := event.Payload()
	handedOut[0] = 'X'
	if string(event.Payload()) != string(stored) {
		t.Fatal("mutating the returned payload altered the event")
	}
}

func TestAnEventRefusesATransactionInTheWrongState(t *testing.T) {
	pending := newTransaction(t, nil)
	processedTx := processed(t, newTransaction(t, nil))

	if _, err := NewWagerTransactionProcessedEvent(id(20), pending, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("processed from a pending transaction: error = %v", err)
	}
	if _, err := NewWagerTransactionRejectedEvent(id(20), processedTx, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("rejected from a processed transaction: error = %v", err)
	}
	if _, err := NewWagerTransactionPendingReferenceEvent(id(20), processedTx, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("pending reference from a processed transaction: error = %v", err)
	}
}

func TestAnEventRefusesWhatItCannotIdentify(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))
	if _, err := NewWagerTransactionProcessedEvent([16]byte{}, tx, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("no event id: error = %v", err)
	}
	if _, err := NewWagerTransactionProcessedEvent(id(20), tx, "  ", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("blank correlation id: error = %v", err)
	}
	if _, err := NewWalletBalanceChangedEvent(id(20), LedgerEntry{}, 1, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("no ledger entry: error = %v", err)
	}
	wallet := newWallet(t, "10.00")
	entry, err := wallet.Credit(id(5), id(6), brl(t, "1.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewWalletBalanceChangedEvent(id(20), entry, 0, "corr", "", t0); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("wallet version zero: error = %v", err)
	}
}

func TestRehydratingAnEventKeepsItsIdAndPayloadUntouched(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))
	event, err := NewWagerTransactionProcessedEvent(id(20), tx, "corr-1", "cause-1", t0)
	if err != nil {
		t.Fatal(err)
	}
	stored := event.Snapshot()

	restored, err := RehydrateOutboxEvent(stored)
	if err != nil {
		t.Fatalf("RehydrateOutboxEvent: %v", err)
	}

	if restored.ID() != id(20) || string(restored.Payload()) != string(event.Payload()) {
		t.Fatal("a rehydrated event must keep its id and its payload, byte for byte")
	}
	if restored.CorrelationID() != "corr-1" || restored.CausationID() != "cause-1" {
		t.Fatalf("correlation %q, causation %q", restored.CorrelationID(), restored.CausationID())
	}
	if got := restored.Snapshot(); !equalOutbox(got, stored) {
		t.Fatalf("round trip differs:\n got  %+v\n want %+v", got, stored)
	}
}

func TestRehydratingAnEventRejectsWhatCouldNotExist(t *testing.T) {
	valid := OutboxEventSnapshot{EventID: id(20), AggregateID: id(21), Status: "PENDING"}
	unknown := valid
	unknown.Status = "LIMBO"
	if _, err := RehydrateOutboxEvent(unknown); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("unknown status: error = %v", err)
	}
	noID := valid
	noID.EventID = uuid.Nil
	if _, err := RehydrateOutboxEvent(noID); !errors.Is(err, ErrInvalidEvent) {
		t.Errorf("no event id: error = %v", err)
	}
}

func equalOutbox(a, b OutboxEventSnapshot) bool {
	return string(a.Payload) == string(b.Payload) && a.EventID == b.EventID && a.EventType == b.EventType &&
		a.Status == b.Status && a.Attempts == b.Attempts && a.OccurredAt.Equal(b.OccurredAt) &&
		a.NextAttemptAt.Equal(b.NextAttemptAt) && a.CorrelationID == b.CorrelationID &&
		textValue(a.CausationID) == textValue(b.CausationID)
}

func TestAnEventIsPublishedOnceAndOnlyWhilePending(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))
	event, err := NewWagerTransactionProcessedEvent(id(20), tx, "corr-1", "", t0)
	if err != nil {
		t.Fatal(err)
	}

	if err := event.MarkPublished(t0.Add(time.Second)); err != nil {
		t.Fatalf("MarkPublished: %v", err)
	}
	if event.Status() != OutboxPublished || !event.PublishedAt().Equal(t0.Add(time.Second)) || event.LockedBy() != "" {
		t.Fatalf("status %s, publishedAt %s, lockedBy %q", event.Status(), event.PublishedAt(), event.LockedBy())
	}
	if err := event.MarkPublished(t0.Add(time.Hour)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("publishing twice: error = %v, want ErrInvalidTransition", err)
	}
	if err := event.Reschedule(t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("rescheduling a published event: error = %v, want ErrInvalidTransition", err)
	}
	if !event.PublishedAt().Equal(t0.Add(time.Second)) {
		t.Fatalf("the first publication time was overwritten: %s", event.PublishedAt())
	}
}

func TestAFailedEventStaysPendingAndWaitsLonger(t *testing.T) {
	tx := processed(t, newTransaction(t, nil))
	event, err := NewWagerTransactionProcessedEvent(id(20), tx, "corr-1", "", t0)
	if err != nil {
		t.Fatal(err)
	}

	if err := event.Reschedule(t0.Add(time.Minute)); err != nil {
		t.Fatalf("Reschedule: %v", err)
	}

	if event.Status() != OutboxPending || !event.NextAttemptAt().Equal(t0.Add(time.Minute)) {
		t.Fatalf("status %s, next attempt %s", event.Status(), event.NextAttemptAt())
	}
	if event.ID() != id(20) {
		t.Fatal("rescheduling must not change the event id: a republication has to carry the same one")
	}
}
