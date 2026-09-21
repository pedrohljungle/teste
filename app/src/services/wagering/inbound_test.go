package wagering

import (
	"context"
	"crypto/sha256"
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// message wraps an operation as the queue delivers it. The hash stands for the exact content of the
// body, so two messages with the same body have the same hash.
func (f *fixture) message(t *testing.T, id, body string, mutate func(*entities.ExternalOperation)) structs.WagerMessage {
	t.Helper()
	sum := sha256.Sum256([]byte(body))
	return structs.WagerMessage{ID: id, Operation: f.op(t, mutate), Hash: sum[:]}
}

func TestAMessageIsHandledOnceAndRecordedInTheInboxInTheSameCommit(t *testing.T) {
	f := newFixture(t, "1000.00")

	out, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil))

	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if out.Duplicate || out.Replay || out.Transaction.Status() != entities.StatusProcessed {
		t.Fatalf("outcome = %+v", out)
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 {
		t.Fatalf("wallet = %s at version %d", balance, version)
	}
	record, ok := f.m.inbox[inboxKey(consumerName, "msg-1")]
	if !ok || record.CompletedAt == nil {
		t.Fatalf("the inbox record is missing or not completed: %+v", record)
	}
	if f.m.atomicCalls != 1 {
		t.Fatalf("the inbox and the operation ran in %d units of work, want one", f.m.atomicCalls)
	}
	if len(f.m.entries) != 1 || len(f.m.events) != 2 {
		t.Fatalf("%d entries and %d events", len(f.m.entries), len(f.m.events))
	}
}

func TestTheSameMessageDeliveredAgainDoesNothing(t *testing.T) {
	f := newFixture(t, "1000.00")
	message := f.message(t, "msg-1", "body", nil)
	if _, err := f.svc.Receive(context.Background(), message); err != nil {
		t.Fatal(err)
	}

	out, err := f.svc.Receive(context.Background(), message)

	if err != nil || !out.Duplicate || out.Transaction != nil {
		t.Fatalf("outcome %+v, error %v, want a duplicate", out, err)
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 {
		t.Fatalf("the redelivery moved the wallet: %s at version %d", balance, version)
	}
	if len(f.m.entries) != 1 || len(f.m.transactions) != 1 || len(f.m.events) != 2 {
		t.Fatalf("the redelivery wrote again: %d entries, %d transactions, %d events",
			len(f.m.entries), len(f.m.transactions), len(f.m.events))
	}
}

func TestAMessageReusingAnIdWithOtherContentIsRefusedWhole(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil)); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "another body", func(op *entities.ExternalOperation) {
		op.ExternalTransactionID, op.IdempotencyKey = "other", "other-key"
	}))

	if !errors.Is(err, wageringiface.ErrMessageConflict) {
		t.Fatalf("error = %v, want ErrMessageConflict", err)
	}
	if balance, _ := f.balance(t); balance != "975.00" || len(f.m.transactions) != 1 {
		t.Fatalf("the conflicting message applied something: %s, %d transactions", balance, len(f.m.transactions))
	}
}

func TestAMessageForAnOperationAlreadyAppliedOverHTTPIsAReplay(t *testing.T) {
	f := newFixture(t, "1000.00")
	first, err := f.svc.Submit(context.Background(), f.op(t, nil))
	if err != nil {
		t.Fatal(err)
	}

	out, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil))

	if err != nil || !out.Replay || out.Transaction.ID() != first.Transaction.ID() {
		t.Fatalf("outcome %+v, error %v, want the stored outcome as a replay", out, err)
	}
	if balance, version := f.balance(t); balance != "975.00" || version != 2 || len(f.m.entries) != 1 {
		t.Fatalf("the operation was applied twice: %s at version %d, %d entries", balance, version, len(f.m.entries))
	}
	if record, ok := f.m.inbox[inboxKey(consumerName, "msg-1")]; !ok || record.CompletedAt == nil {
		t.Fatal("the message must be recorded as handled even though it applied nothing")
	}
}

func TestAnOperationAlreadyAppliedOverTheQueueIsAReplayOverHTTP(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil)); err != nil {
		t.Fatal(err)
	}

	out, err := f.svc.Submit(context.Background(), f.op(t, nil))

	if err != nil || !out.Replay {
		t.Fatalf("outcome %+v, error %v, want a replay", out, err)
	}
	if balance, _ := f.balance(t); balance != "975.00" {
		t.Fatalf("the operation was applied twice: %s", balance)
	}
}

func TestAMessageContradictingAStoredOperationIsAConflictAndLeavesNoInboxRecord(t *testing.T) {
	f := newFixture(t, "1000.00")
	if _, err := f.svc.Submit(context.Background(), f.op(t, nil)); err != nil {
		t.Fatal(err)
	}

	_, err := f.svc.Receive(context.Background(), f.message(t, "msg-2", "other body", func(op *entities.ExternalOperation) {
		op.Money = money(t, "30.00")
	}))

	if !errors.Is(err, wageringiface.ErrIdempotencyConflict) {
		t.Fatalf("error = %v, want ErrIdempotencyConflict", err)
	}
	if _, ok := f.m.inbox[inboxKey(consumerName, "msg-2")]; ok {
		t.Fatal("a refused message left an inbox record, so a redelivery would be treated as handled")
	}
}

func TestABusinessRejectionOverTheQueueIsStoredAndTheMessageIsRecordedAsHandled(t *testing.T) {
	f := newFixture(t, "10.00")

	out, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil))

	if err != nil {
		t.Fatalf("a rejection is an outcome, not an error: %v", err)
	}
	if out.Transaction.Status() != entities.StatusRejected || out.Transaction.FailureCode() != entities.FailureInsufficientFunds {
		t.Fatalf("status %s, code %s", out.Transaction.Status(), out.Transaction.FailureCode())
	}
	if record, ok := f.m.inbox[inboxKey(consumerName, "msg-1")]; !ok || record.CompletedAt == nil {
		t.Fatal("the rejected message must be recorded as handled")
	}
	if balance, version := f.balance(t); balance != "10.00" || version != 1 {
		t.Fatalf("a rejection changed the wallet: %s at version %d", balance, version)
	}
}

func TestAMessageForAnUnknownWalletIsRejectedWithoutARecord(t *testing.T) {
	f := newFixture(t, "100.00")

	_, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", func(op *entities.ExternalOperation) {
		op.WalletID = uuid.NewString()
	}))

	if code, ok := entities.RejectionCode(err); !ok || code != entities.FailureWalletNotFound {
		t.Fatalf("error = %v, want a rejection with %s", err, entities.FailureWalletNotFound)
	}
	if len(f.m.inbox) != 0 || len(f.m.transactions) != 0 {
		t.Fatalf("something was stored: %d inbox records, %d transactions", len(f.m.inbox), len(f.m.transactions))
	}
}

func TestAStorageFailureRollsTheInboxRecordBackSoTheRedeliveryIsProcessed(t *testing.T) {
	f := newFixture(t, "100.00")
	f.m.failWalletLock = persistenceiface.ErrUnavailable
	message := f.message(t, "msg-1", "body", nil)

	_, err := f.svc.Receive(context.Background(), message)

	if !errors.Is(err, persistenceiface.ErrUnavailable) {
		t.Fatalf("error = %v, want ErrUnavailable", err)
	}
	if len(f.m.inbox) != 0 {
		t.Fatal("the failed attempt kept its inbox record: the redelivery would be skipped as a duplicate and the operation lost")
	}

	f.m.failWalletLock = nil
	out, err := f.svc.Receive(context.Background(), message)
	if err != nil || out.Transaction.Status() != entities.StatusProcessed {
		t.Fatalf("the redelivery: outcome %+v, error %v", out, err)
	}
}

func TestAMessageThatLosesTheRaceForTheUniqueIndexIsRetriedAndResolvesAsAReplay(t *testing.T) {
	f := newFixture(t, "1000.00")
	var winner *entities.WagerTransaction
	// The operation has not been stored when this message looks for it. Before it inserts, another
	// message for the same operation commits, and the database refuses this insert.
	f.m.beforeInsert = func() {
		other := newTestService(f.m)
		candidate, err := entities.NewExternalTransaction(other.newID(), f.op(t, nil), fixedNow)
		if err != nil {
			t.Fatal(err)
		}
		if err := candidate.MarkProcessed(money(t, "975.00"), 2, fixedNow); err != nil {
			t.Fatal(err)
		}
		f.m.commitElsewhere(candidate.Snapshot())
		winner = candidate
	}

	out, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", nil))

	if err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if !out.Replay || out.Transaction.ID() != winner.ID() {
		t.Fatalf("outcome %+v, want a replay of the winner %s", out, winner.ID())
	}
	if record, ok := f.m.inbox[inboxKey(consumerName, "msg-1")]; !ok || record.CompletedAt == nil {
		t.Fatal("the message must be recorded as handled after the retry")
	}
	if len(f.m.entries) != 0 {
		t.Fatal("the losing attempt left a ledger entry behind")
	}
}

func TestAMessageThatCannotBeHandledAtAllIsPermanentAndOpensNoUnitOfWork(t *testing.T) {
	cases := map[string]func(*entities.ExternalOperation){
		"an unknown kind": func(op *entities.ExternalOperation) { op.Kind = "GAMBLE" },
		"OPENING":         func(op *entities.ExternalOperation) { op.Kind = "OPENING" },
		"no provider":     func(op *entities.ExternalOperation) { op.ProviderID = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "100.00")

			_, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", mutate))

			if !errors.Is(err, entities.ErrInvalidTransaction) {
				t.Fatalf("error = %v, want ErrInvalidTransaction", err)
			}
			if f.m.atomicCalls != 0 || len(f.m.inbox) != 0 {
				t.Fatal("something was stored for a message that was invalid before it started")
			}
		})
	}
}

func TestAMessageWithoutAnIdOrAHashIsInvalid(t *testing.T) {
	f := newFixture(t, "100.00")

	for name, message := range map[string]structs.WagerMessage{
		"no id":   {ID: "", Operation: f.op(t, nil), Hash: []byte{1}},
		"no hash": {ID: "msg-1", Operation: f.op(t, nil)},
	} {
		if _, err := f.svc.Receive(context.Background(), message); !errors.Is(err, entities.ErrInvalidTransaction) {
			t.Errorf("%s: error = %v, want ErrInvalidTransaction", name, err)
		}
	}
	if f.m.atomicCalls != 0 {
		t.Fatal("a unit of work was opened for a message that cannot be recorded")
	}
}

func TestReversalsOverTheQueueAreNotSupportedYet(t *testing.T) {
	f := newFixture(t, "100.00")

	_, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", func(op *entities.ExternalOperation) {
		op.Kind, op.ReferenceExternalTransactionID = "REFUND", "bet-1"
	}))

	if !errors.Is(err, wageringiface.ErrKindNotSupported) {
		t.Fatalf("error = %v, want ErrKindNotSupported", err)
	}
}
