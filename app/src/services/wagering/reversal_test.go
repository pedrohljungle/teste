package wagering

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// submit sends an operation of the fixture's wallet, keyed by its external id.
func (f *fixture) submit(t *testing.T, kind, external, amount, reference string) (structs.WagerOutcome, error) {
	t.Helper()
	return f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
		op.Kind = kind
		op.ExternalTransactionID = external
		op.IdempotencyKey = "provider-a:" + external
		op.Money = money(t, amount)
		op.ReferenceExternalTransactionID = reference
	}))
}

// must submits an operation and expects it to succeed as an outcome.
func (f *fixture) must(t *testing.T, kind, external, amount, reference string) *entities.WagerTransaction {
	t.Helper()
	out, err := f.submit(t, kind, external, amount, reference)
	if err != nil {
		t.Fatalf("%s %s: %v", kind, external, err)
	}
	return out.Transaction
}

func (f *fixture) storedStatus(t *testing.T, external string) (entities.TransactionStatus, entities.FailureCode) {
	t.Helper()
	for _, stored := range f.m.transactions {
		if stored.ExternalTransactionID != nil && *stored.ExternalTransactionID == external {
			code := ""
			if stored.FailureCode != nil {
				code = *stored.FailureCode
			}
			return entities.TransactionStatus(stored.Status), entities.FailureCode(code)
		}
	}
	t.Fatalf("no transaction stored for %s", external)
	return "", ""
}

func (f *fixture) advance(d time.Duration) { f.m.clock = f.m.clock.Add(d) }

func (f *fixture) resolve(t *testing.T) int {
	t.Helper()
	found, err := f.svc.ResolvePending(context.Background())
	if err != nil {
		t.Fatalf("ResolvePending: %v", err)
	}
	return found
}

// --- applying a reversal whose reference is there -------------------------------------------

func TestARefundOfAProcessedBetCreditsTheExactAmount(t *testing.T) {
	f := newFixture(t, "1000.00")
	bet := f.must(t, "BET", "bet-1", "25.00", "")

	refund := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	if refund.Status() != entities.StatusProcessed || refund.ResultBalance().Amount() != "1000.00" ||
		refund.ReferenceTransactionID() != bet.ID() {
		t.Fatalf("refund = %s, %s, reference %s", refund.Status(), refund.ResultBalance(), refund.ReferenceTransactionID())
	}
	if balance, version := f.balance(t); balance != "1000.00" || version != 3 {
		t.Fatalf("wallet = %s at version %d", balance, version)
	}
	last := f.m.entries[len(f.m.entries)-1]
	if last.Direction != "CREDIT" || last.AmountMinor != 2500 || last.BalanceBeforeMinor != 97500 || last.BalanceAfterMinor != 100000 {
		t.Fatalf("entry = %+v", last)
	}
	if got := eventTypes(f.m.events[2:]); len(got) != 2 || got[0] != "WagerTransactionProcessed" || got[1] != "WalletBalanceChanged" {
		t.Fatalf("events of the refund = %v", got)
	}
}

func TestTheDirectionOfARollbackDependsOnWhatItUndoes(t *testing.T) {
	cases := []struct {
		name      string
		setup     func(*testing.T, *fixture)
		reference string
		want      string
		balance   string
	}{
		{"a bet is credited back", func(t *testing.T, f *fixture) { f.must(t, "BET", "ref", "25.00", "") }, "ref", "CREDIT", "1000.00"},
		{"a win is debited back", func(t *testing.T, f *fixture) { f.must(t, "WIN", "ref", "25.00", "") }, "ref", "DEBIT", "1000.00"},
		{"a refund is debited back", func(t *testing.T, f *fixture) {
			f.must(t, "BET", "bet", "25.00", "")
			f.must(t, "REFUND", "ref", "25.00", "bet")
		}, "ref", "DEBIT", "975.00"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "1000.00")
			tc.setup(t, f)

			rollback := f.must(t, "ROLLBACK", "rollback-1", "25.00", tc.reference)

			if rollback.Status() != entities.StatusProcessed {
				t.Fatalf("status %s, code %s", rollback.Status(), rollback.FailureCode())
			}
			if last := f.m.entries[len(f.m.entries)-1]; last.Direction != tc.want {
				t.Fatalf("direction = %s, want %s", last.Direction, tc.want)
			}
			if balance, _ := f.balance(t); balance != tc.balance {
				t.Fatalf("balance = %s, want %s", balance, tc.balance)
			}
		})
	}
}

func TestARollbackThatWouldOverdrawIsRejectedWithItsOwnCode(t *testing.T) {
	f := newFixture(t, "0.00")
	f.must(t, "WIN", "win-1", "40.00", "")
	f.must(t, "BET", "bet-1", "35.00", "")
	if balance, _ := f.balance(t); balance != "5.00" {
		t.Fatalf("setup: balance %s", balance)
	}

	rollback := f.must(t, "ROLLBACK", "rollback-1", "40.00", "win-1")

	if rollback.Status() != entities.StatusRejected || rollback.FailureCode() != entities.FailureRollbackInsufficientFunds {
		t.Fatalf("status %s, code %s", rollback.Status(), rollback.FailureCode())
	}
	if rollback.FailureCode() == entities.FailureInsufficientFunds {
		t.Fatal("a reversal without funds must not read as a bet without funds")
	}
	if balance, version := f.balance(t); balance != "5.00" || version != 3 {
		t.Fatalf("a rejected rollback changed the wallet: %s at version %d", balance, version)
	}
}

func TestAReferenceIsReversedAtMostOnceWhateverTheKind(t *testing.T) {
	cases := []struct{ first, second string }{
		{"REFUND", "REFUND"},
		{"ROLLBACK", "ROLLBACK"},
		{"REFUND", "ROLLBACK"},
		{"ROLLBACK", "REFUND"},
	}
	for _, tc := range cases {
		t.Run(tc.first+" then "+tc.second, func(t *testing.T) {
			f := newFixture(t, "1000.00")
			f.must(t, "BET", "bet-1", "25.00", "")
			f.must(t, tc.first, "first", "25.00", "bet-1")

			second := f.must(t, tc.second, "second", "25.00", "bet-1")

			if second.Status() != entities.StatusRejected || second.FailureCode() != entities.FailureReferenceAlreadyReversed {
				t.Fatalf("status %s, code %s", second.Status(), second.FailureCode())
			}
			if balance, _ := f.balance(t); balance != "1000.00" {
				t.Fatalf("the bet was given back twice: %s", balance)
			}
		})
	}
}

func TestAReversalThatDisagreesWithItsReferenceIsRejected(t *testing.T) {
	cases := map[string]struct {
		mutate func(*entities.ExternalOperation)
		want   entities.FailureCode
	}{
		"another round":  {func(op *entities.ExternalOperation) { op.RoundID = "round-1" }, entities.FailureReferenceMismatch},
		"another amount": {func(op *entities.ExternalOperation) { op.Money = money(t, "20.00") }, entities.FailureAmountMismatch},
		"another player": {func(op *entities.ExternalOperation) { op.PlayerID = uuid.NewString() }, entities.FailurePlayerMismatch},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, "1000.00")
			f.must(t, "BET", "bet-1", "25.00", "")

			out, err := f.svc.Submit(context.Background(), f.op(t, func(op *entities.ExternalOperation) {
				op.Kind, op.ExternalTransactionID, op.IdempotencyKey = "REFUND", "refund-1", "provider-a:refund-1"
				op.ReferenceExternalTransactionID = "bet-1"
				tc.mutate(op)
			}))

			if err != nil || out.Transaction.FailureCode() != tc.want {
				t.Fatalf("outcome %+v, error %v, want %s", out, err, tc.want)
			}
		})
	}
}

func TestARefundOfAWinIsRejectedBecauseOnlyABetIsRefunded(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "WIN", "win-1", "25.00", "")

	refund := f.must(t, "REFUND", "refund-1", "25.00", "win-1")

	if refund.Status() != entities.StatusRejected || refund.FailureCode() != entities.FailureReferenceMismatch {
		t.Fatalf("status %s, code %s", refund.Status(), refund.FailureCode())
	}
}

func TestAReversalOfARejectedReferenceIsRejected(t *testing.T) {
	f := newFixture(t, "10.00")
	bet := f.must(t, "BET", "bet-1", "25.00", "")
	if bet.Status() != entities.StatusRejected {
		t.Fatalf("setup: the bet is %s", bet.Status())
	}

	refund := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	if refund.Status() != entities.StatusRejected || refund.FailureCode() != entities.FailureReferenceNotProcessed {
		t.Fatalf("status %s, code %s", refund.Status(), refund.FailureCode())
	}
}

// --- a reversal whose reference has not arrived ---------------------------------------------

func TestAReversalBeforeItsReferenceIsParkedAsPendingReference(t *testing.T) {
	f := newFixture(t, "1000.00")

	refund := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	if refund.Status() != entities.StatusPendingReference {
		t.Fatalf("status = %s", refund.Status())
	}
	if refund.ReferenceAttempts() != 0 || !refund.ReferenceNextAttemptAt().Equal(fixedNow.Add(time.Second)) ||
		!refund.ReferenceExpiresAt().Equal(fixedNow.Add(time.Hour)) {
		t.Fatalf("attempts %d, next %s, expires %s", refund.ReferenceAttempts(), refund.ReferenceNextAttemptAt(), refund.ReferenceExpiresAt())
	}
	if balance, version := f.balance(t); balance != "1000.00" || version != 1 || len(f.m.entries) != 0 {
		t.Fatalf("a pending reversal moved the wallet: %s at version %d, %d entries", balance, version, len(f.m.entries))
	}
	if got := eventTypes(f.m.events); len(got) != 1 || got[0] != "WagerTransactionPendingReference" {
		t.Fatalf("events = %v", got)
	}
}

func TestReplayingAPendingReversalReturnsThePendingOutcome(t *testing.T) {
	f := newFixture(t, "1000.00")
	first := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	out, err := f.submit(t, "REFUND", "refund-1", "25.00", "bet-1")

	if err != nil || !out.Replay || out.Transaction.ID() != first.ID() || out.Transaction.Status() != entities.StatusPendingReference {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	if len(f.m.events) != 1 {
		t.Fatalf("a replay published %d events", len(f.m.events))
	}
}

func TestAPendingReversalIsResolvedWhenItsReferenceArrives(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	f.must(t, "BET", "bet-1", "25.00", "")

	if found := f.resolve(t); found != 0 {
		t.Fatalf("found %d before the first look was due", found)
	}
	f.advance(2 * time.Second)
	if found := f.resolve(t); found != 1 {
		t.Fatalf("found %d, want 1", found)
	}

	status, _ := f.storedStatus(t, "refund-1")
	if status != entities.StatusProcessed {
		t.Fatalf("status = %s", status)
	}
	if balance, version := f.balance(t); balance != "1000.00" || version != 3 {
		t.Fatalf("wallet = %s at version %d, the refund must credit exactly once", balance, version)
	}
	if f.resolve(t) != 0 {
		t.Fatal("a resolved reversal was found again")
	}
}

func TestAWaitingReversalLooksAgainWithALongerWaitEachTime(t *testing.T) {
	f := newFixture(t, "1000.00")
	parked := f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	next := parked.ReferenceNextAttemptAt()

	for attempt, wantWait := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second} {
		f.m.clock = next
		if found := f.resolve(t); found != 1 {
			t.Fatalf("look %d: found %d", attempt+1, found)
		}
		var stored entities.WagerTransactionSnapshot
		for _, s := range f.m.transactions {
			if s.ExternalTransactionID != nil && *s.ExternalTransactionID == "refund-1" {
				stored = s
			}
		}
		if stored.Status != "PENDING_REFERENCE" || stored.ReferenceAttempts != attempt+1 {
			t.Fatalf("look %d: status %s, attempts %d", attempt+1, stored.Status, stored.ReferenceAttempts)
		}
		if got := stored.ReferenceNextAttemptAt.Sub(next); got != wantWait {
			t.Fatalf("look %d: waits %s, want %s", attempt+1, got, wantWait)
		}
		next = *stored.ReferenceNextAttemptAt
	}
}

func TestAWaitThatRunsOutOfAttemptsIsRejectedAsReferenceNotFound(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	for range testReference.MaxAttempts + 1 {
		f.advance(time.Hour / 4)
		f.resolve(t)
	}

	status, code := f.storedStatus(t, "refund-1")
	if status != entities.StatusRejected || code != entities.FailureReferenceNotFound {
		t.Fatalf("status %s, code %s", status, code)
	}
	if got := eventTypes(f.m.events); got[len(got)-1] != "WagerTransactionRejected" {
		t.Fatalf("events = %v, want the rejection at the end", got)
	}
}

func TestAWaitThatPassesItsTTLIsRejectedAsReferenceNotFound(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")

	f.m.clock = fixedNow.Add(testReference.TTL)
	f.resolve(t)

	status, code := f.storedStatus(t, "refund-1")
	if status != entities.StatusRejected || code != entities.FailureReferenceNotFound {
		t.Fatalf("status %s, code %s", status, code)
	}
}

func TestAReversalWhoseReferenceIsItselfWaitingKeepsWaitingThenIsRejectedAsNotProcessed(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	f.must(t, "ROLLBACK", "rollback-1", "25.00", "refund-1")

	f.advance(2 * time.Second)
	f.resolve(t)
	if status, _ := f.storedStatus(t, "rollback-1"); status != entities.StatusPendingReference {
		t.Fatalf("the rollback is %s: its reference is only pending, it has to keep waiting", status)
	}

	f.m.clock = fixedNow.Add(testReference.TTL)
	f.resolve(t)
	status, code := f.storedStatus(t, "rollback-1")
	if status != entities.StatusRejected || code != entities.FailureReferenceNotProcessed {
		t.Fatalf("status %s, code %s: the reference exists, it just never finished", status, code)
	}
}

func TestAChainOfPendingReversalsResolvesInOrderOnceTheBetArrives(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	f.must(t, "ROLLBACK", "rollback-1", "25.00", "refund-1")
	f.must(t, "BET", "bet-1", "25.00", "")

	for range 4 {
		f.advance(5 * time.Second)
		f.resolve(t)
	}

	if status, _ := f.storedStatus(t, "refund-1"); status != entities.StatusProcessed {
		t.Fatalf("the refund is %s", status)
	}
	if status, code := f.storedStatus(t, "rollback-1"); status != entities.StatusProcessed {
		t.Fatalf("the rollback is %s (%s)", status, code)
	}
	// bet -25, refund +25, rollback of the refund -25
	if balance, _ := f.balance(t); balance != "975.00" {
		t.Fatalf("balance = %s", balance)
	}
}

func TestAPendingReversalWhoseReferenceEndedWithoutSuccessIsRejectedAtOnce(t *testing.T) {
	f := newFixture(t, "10.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	rejected := f.must(t, "BET", "bet-1", "25.00", "")
	if rejected.Status() != entities.StatusRejected {
		t.Fatalf("setup: the bet is %s", rejected.Status())
	}

	f.advance(2 * time.Second)
	f.resolve(t)

	status, code := f.storedStatus(t, "refund-1")
	if status != entities.StatusRejected || code != entities.FailureReferenceNotProcessed {
		t.Fatalf("status %s, code %s: it must not wait for a reference that will never succeed", status, code)
	}
}

func TestAPendingRollbackThatWouldOverdrawIsRejectedWhenItIsFinallyApplied(t *testing.T) {
	f := newFixture(t, "0.00")
	f.must(t, "ROLLBACK", "rollback-1", "40.00", "win-1")
	f.must(t, "WIN", "win-1", "40.00", "")
	f.must(t, "BET", "bet-1", "38.00", "")

	f.advance(2 * time.Second)
	f.resolve(t)

	status, code := f.storedStatus(t, "rollback-1")
	if status != entities.StatusRejected || code != entities.FailureRollbackInsufficientFunds {
		t.Fatalf("status %s, code %s", status, code)
	}
	if balance, _ := f.balance(t); balance != "2.00" {
		t.Fatalf("balance = %s", balance)
	}
}

func TestOneUnitOfWorkResolvesEachPendingReversalSoOneCannotUndoTheOther(t *testing.T) {
	f := newFixture(t, "1000.00")
	f.must(t, "REFUND", "refund-1", "25.00", "bet-1")
	f.must(t, "REFUND", "refund-2", "25.00", "bet-2")
	f.must(t, "BET", "bet-1", "25.00", "")
	f.must(t, "BET", "bet-2", "25.00", "")
	callsBefore := f.m.atomicCalls

	f.advance(2 * time.Second)
	found := f.resolve(t)

	if found != 2 {
		t.Fatalf("found %d, want both", found)
	}
	// two reversals plus the look that finds nothing more
	if got := f.m.atomicCalls - callsBefore; got != 3 {
		t.Fatalf("%d units of work, want one per reversal and one to find the end", got)
	}
}

func TestTheReferenceBackoffDoublesAndStopsAtTheMaximum(t *testing.T) {
	f := newFixture(t, "1000.00")
	want := map[int]time.Duration{1: time.Second, 2: 2 * time.Second, 3: 4 * time.Second, 4: 8 * time.Second, 5: 8 * time.Second, 40: 8 * time.Second}
	for attempts, wait := range want {
		if got := f.svc.referenceBackoff(attempts); got != wait {
			t.Errorf("referenceBackoff(%d) = %s, want %s", attempts, got, wait)
		}
	}
}

func TestTheReferenceSpreadStaysWithinAFifthEitherSide(t *testing.T) {
	for range 300 {
		if got := spread(10 * time.Second); got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("spread = %s", got)
		}
	}
	if spread(0) != 0 || spread(3*time.Nanosecond) != 3*time.Nanosecond {
		t.Fatal("a delay too small to spread must come back as it is")
	}
}

func TestAPendingReversalReceivedOverTheQueueIsRecordedAsHandledOnceTheWaitIsStored(t *testing.T) {
	f := newFixture(t, "1000.00")

	out, err := f.svc.Receive(context.Background(), f.message(t, "msg-1", "body", func(op *entities.ExternalOperation) {
		op.Kind, op.ReferenceExternalTransactionID = "REFUND", "bet-1"
	}))

	if err != nil || out.Transaction.Status() != entities.StatusPendingReference {
		t.Fatalf("outcome %+v, error %v", out, err)
	}
	if record, ok := f.m.inbox[inboxKey(consumerName, "msg-1")]; !ok || record.CompletedAt == nil {
		t.Fatal("the message must be completed as soon as the wait is stored: the job takes over from there")
	}
}
