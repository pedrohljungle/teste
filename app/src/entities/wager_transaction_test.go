package entities

import (
	"bytes"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestAnExternalTransactionIsAcceptedAsPendingWithItsHash(t *testing.T) {
	tx := newTransaction(t, nil)

	if tx.Status() != StatusPending || tx.Origin() != OriginExternal || tx.Kind() != KindBet {
		t.Fatalf("status %s, origin %s, kind %s", tx.Status(), tx.Origin(), tx.Kind())
	}
	if tx.ProviderID() != "provider-a" || tx.ExternalTransactionID() != "transaction-123" ||
		tx.IdempotencyKey() != "provider-a:transaction-123" || tx.RoundID() != "round-987" || tx.GameID() != "fortune-chimp" {
		t.Fatalf("metadata = %+v", tx.Snapshot())
	}
	if len(tx.PayloadHash()) != 32 {
		t.Fatalf("payload hash has %d bytes, want a SHA-256", len(tx.PayloadHash()))
	}
	if !tx.CreatedAt().Equal(t0) || tx.SettledAt() != (time.Time{}) {
		t.Fatalf("createdAt %s, settledAt %s", tx.CreatedAt(), tx.SettledAt())
	}
}

func TestTheKindOfAnExternalOperationIsCaseInsensitive(t *testing.T) {
	tx := newTransaction(t, func(op *ExternalOperation) { op.Kind = "bet" })
	if tx.Kind() != KindBet {
		t.Fatalf("Kind = %s", tx.Kind())
	}
}

func TestOpeningIsRefusedAsAnExternalKind(t *testing.T) {
	op := operation(t)
	op.Kind = "OPENING"

	_, err := NewExternalTransaction(id(10), op, t0)

	if !errors.Is(err, ErrOpeningNotAllowed) || !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("error = %v, want ErrOpeningNotAllowed and ErrInvalidTransaction", err)
	}
}

func TestAMalformedExternalOperationIsRefused(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ExternalOperation)
	}{
		{"unknown kind", func(op *ExternalOperation) { op.Kind = "GAMBLE" }},
		{"empty kind", func(op *ExternalOperation) { op.Kind = "" }},
		{"no provider", func(op *ExternalOperation) { op.ProviderID = "" }},
		{"blank provider", func(op *ExternalOperation) { op.ProviderID = "   " }},
		{"no external id", func(op *ExternalOperation) { op.ExternalTransactionID = "" }},
		{"no idempotency key", func(op *ExternalOperation) { op.IdempotencyKey = "" }},
		{"no round", func(op *ExternalOperation) { op.RoundID = "" }},
		{"no game", func(op *ExternalOperation) { op.GameID = "" }},
		{"player not a UUID", func(op *ExternalOperation) { op.PlayerID = "player-1" }},
		{"player is the nil UUID", func(op *ExternalOperation) { op.PlayerID = "00000000-0000-0000-0000-000000000000" }},
		{"wallet not a UUID", func(op *ExternalOperation) { op.WalletID = "" }},
		{"uninitialised money", func(op *ExternalOperation) { op.Money = Money{} }},
		{"negative money", func(op *ExternalOperation) { op.Money = mustMinor(t, -1, "BRL") }},
		{"a reversal without a reference", func(op *ExternalOperation) { op.Kind = "REFUND" }},
		{"a bet with a reference", func(op *ExternalOperation) { op.ReferenceExternalTransactionID = "bet-1" }},
		{"a loss with a reference", func(op *ExternalOperation) {
			op.Kind, op.Money, op.ReferenceExternalTransactionID = "LOSS", brl(t, "0.00"), "bet-1"
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			op := operation(t)
			tc.mutate(&op)
			if _, err := NewExternalTransaction(id(10), op, t0); !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("error = %v, want ErrInvalidTransaction", err)
			}
		})
	}
	if _, err := NewExternalTransaction([16]byte{}, operation(t), t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("a nil id: error = %v", err)
	}
}

func TestAWinMayOrMayNotNameABetOfTheRound(t *testing.T) {
	without := newTransaction(t, func(op *ExternalOperation) { op.Kind = "WIN" })
	with := newTransaction(t, func(op *ExternalOperation) {
		op.Kind, op.ReferenceExternalTransactionID = "WIN", "bet-1"
	})
	if without.ReferenceExternalTransactionID() != "" || with.ReferenceExternalTransactionID() != "bet-1" {
		t.Fatalf("references: %q and %q", without.ReferenceExternalTransactionID(), with.ReferenceExternalTransactionID())
	}
}

func TestTheValuePolicyOfEachKind(t *testing.T) {
	cases := []struct {
		kind    string
		amount  string
		allowed bool
	}{
		{"BET", "25.00", true},
		{"BET", "0.00", false},
		{"WIN", "40.00", true},
		{"WIN", "0.00", false},
		{"LOSS", "0.00", true},
		{"LOSS", "0.01", false},
		{"REFUND", "25.00", true},
		{"REFUND", "0.00", false},
		{"ROLLBACK", "25.00", true},
		{"ROLLBACK", "0.00", false},
	}
	for _, tc := range cases {
		t.Run(tc.kind+" "+tc.amount, func(t *testing.T) {
			tx := newTransaction(t, func(op *ExternalOperation) {
				op.Kind, op.Money = tc.kind, brl(t, tc.amount)
				if tc.kind == "REFUND" || tc.kind == "ROLLBACK" {
					op.ReferenceExternalTransactionID = "bet-1"
				}
			})
			err := tx.CheckAmountPolicy()
			if tc.allowed {
				if err != nil {
					t.Fatalf("CheckAmountPolicy: %v", err)
				}
				return
			}
			if code, ok := RejectionCode(err); !ok || code != FailureInvalidAmount {
				t.Fatalf("error = %v, want a rejection with %s", err, FailureInvalidAmount)
			}
		})
	}
}

func TestOnlyALossLeavesTheBalanceAlone(t *testing.T) {
	for _, kind := range []string{"BET", "WIN", "REFUND", "ROLLBACK"} {
		tx := newTransaction(t, func(op *ExternalOperation) {
			op.Kind = kind
			if kind == "REFUND" || kind == "ROLLBACK" {
				op.ReferenceExternalTransactionID = "bet-1"
			}
		})
		if !tx.MovesBalance() {
			t.Errorf("%s must move the balance", kind)
		}
	}
	loss := newTransaction(t, func(op *ExternalOperation) { op.Kind, op.Money = "LOSS", brl(t, "0.00") })
	if loss.MovesBalance() {
		t.Error("LOSS must not move the balance")
	}
	if _, err := loss.Movement(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("LOSS has no direction: error = %v", err)
	}
}

func TestTheDirectionOfEachMovement(t *testing.T) {
	cases := []struct {
		kind      string
		reference TransactionKind
		want      Direction
	}{
		{"BET", "", DirectionDebit},
		{"WIN", "", DirectionCredit},
		{"REFUND", KindBet, DirectionCredit},
		{"ROLLBACK", KindBet, DirectionCredit},
		{"ROLLBACK", KindWin, DirectionDebit},
		{"ROLLBACK", KindRefund, DirectionDebit},
	}
	for _, tc := range cases {
		t.Run(tc.kind+" of "+string(tc.reference), func(t *testing.T) {
			tx := newTransaction(t, func(op *ExternalOperation) {
				op.Kind = tc.kind
				if tc.reference != "" {
					op.ReferenceExternalTransactionID = "ref-1"
				}
			})
			var reference *WagerTransaction
			if tc.reference != "" {
				reference = newTransaction(t, func(op *ExternalOperation) {
					op.Kind = string(tc.reference)
					if tc.reference == KindRefund {
						op.ReferenceExternalTransactionID = "bet-1"
					}
				})
			}
			got, err := tx.Movement(reference)
			if err != nil || got != tc.want {
				t.Fatalf("Movement = %s, %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestARollbackNeedsItsReferenceToKnowItsDirection(t *testing.T) {
	rollback := newTransaction(t, func(op *ExternalOperation) {
		op.Kind, op.ReferenceExternalTransactionID = "ROLLBACK", "bet-1"
	})
	if _, err := rollback.Movement(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Fatalf("error = %v, want ErrInvalidTransaction", err)
	}
	loss := newTransaction(t, func(op *ExternalOperation) { op.Kind, op.Money = "LOSS", brl(t, "0.00") })
	if _, err := rollback.Movement(loss); !errors.Is(err, ErrRejected) {
		t.Fatalf("undoing a LOSS: error = %v, want a rejection", err)
	}
}

func TestTheOpeningTransactionCarriesNoExternalMetadata(t *testing.T) {
	tx, err := NewOpeningTransaction(id(3), id(2), id(1), brl(t, "1000.00"), t0)
	if err != nil {
		t.Fatalf("NewOpeningTransaction: %v", err)
	}
	if tx.Kind() != KindOpening || tx.Origin() != OriginInternal || tx.Status() != StatusPending {
		t.Fatalf("kind %s, origin %s, status %s", tx.Kind(), tx.Origin(), tx.Status())
	}
	snapshot := tx.Snapshot()
	if snapshot.ProviderID != nil || snapshot.ExternalTransactionID != nil || snapshot.IdempotencyKey != nil ||
		snapshot.PayloadHash != nil || snapshot.RoundID != nil || snapshot.GameID != nil ||
		snapshot.ReferenceExternalTransactionID != nil {
		t.Fatalf("an opening must store NULL for every external column: %+v", snapshot)
	}
	if err := tx.CheckAmountPolicy(); err != nil {
		t.Fatalf("CheckAmountPolicy: %v", err)
	}
}

func TestAnOpeningTransactionRefusesWhatIsNotAPositiveCredit(t *testing.T) {
	for name, money := range map[string]Money{
		"zero": brl(t, "0.00"), "negative": mustMinor(t, -1, "BRL"), "uninitialised": {},
	} {
		if _, err := NewOpeningTransaction(id(3), id(2), id(1), money, t0); !errors.Is(err, ErrInvalidTransaction) {
			t.Errorf("%s: error = %v", name, err)
		}
	}
	if _, err := NewOpeningTransaction([16]byte{}, id(2), id(1), brl(t, "1.00"), t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("no id: error = %v", err)
	}
}

func TestATransactionMovesThroughTheStateMachine(t *testing.T) {
	t.Run("pending to processed records what the provider is told", func(t *testing.T) {
		tx := newTransaction(t, nil)
		if err := tx.MarkProcessed(brl(t, "975.00"), 2, t0.Add(time.Second)); err != nil {
			t.Fatalf("MarkProcessed: %v", err)
		}
		if tx.Status() != StatusProcessed || tx.ResultBalance().Amount() != "975.00" || tx.ResultWalletVersion() != 2 {
			t.Fatalf("status %s, result %s at version %d", tx.Status(), tx.ResultBalance(), tx.ResultWalletVersion())
		}
		if !tx.SettledAt().Equal(t0.Add(time.Second)) || tx.FailureCode() != "" {
			t.Fatalf("settledAt %s, failure %q", tx.SettledAt(), tx.FailureCode())
		}
	})
	t.Run("pending to rejected records the code", func(t *testing.T) {
		tx := newTransaction(t, nil)
		if err := tx.MarkRejected(FailureInsufficientFunds, t0); err != nil {
			t.Fatalf("MarkRejected: %v", err)
		}
		if tx.Status() != StatusRejected || tx.FailureCode() != FailureInsufficientFunds || tx.SettledAt().IsZero() {
			t.Fatalf("status %s, failure %q", tx.Status(), tx.FailureCode())
		}
	})
	t.Run("pending to failed records the code", func(t *testing.T) {
		tx := newTransaction(t, nil)
		if err := tx.MarkFailed(FailureInternal, t0); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
		if tx.Status() != StatusFailed || tx.FailureCode() != FailureInternal {
			t.Fatalf("status %s, failure %q", tx.Status(), tx.FailureCode())
		}
	})
	t.Run("pending to pending reference to processed", func(t *testing.T) {
		tx := newReversal(t)
		if err := tx.MarkPendingReference(t0.Add(time.Minute), t0.Add(24*time.Hour), t0); err != nil {
			t.Fatalf("MarkPendingReference: %v", err)
		}
		if tx.Status() != StatusPendingReference || tx.ReferenceAttempts() != 0 ||
			!tx.ReferenceNextAttemptAt().Equal(t0.Add(time.Minute)) || !tx.ReferenceExpiresAt().Equal(t0.Add(24*time.Hour)) {
			t.Fatalf("pending reference = %+v", tx.Snapshot())
		}
		if err := tx.MarkProcessed(brl(t, "1000.00"), 3, t0); err != nil {
			t.Fatalf("MarkProcessed: %v", err)
		}
	})
	t.Run("pending reference to rejected", func(t *testing.T) {
		tx := newReversal(t)
		mustPend(t, tx)
		if err := tx.MarkRejected(FailureReferenceNotFound, t0); err != nil {
			t.Fatalf("MarkRejected: %v", err)
		}
	})
	t.Run("pending reference to failed", func(t *testing.T) {
		tx := newReversal(t)
		mustPend(t, tx)
		if err := tx.MarkFailed(FailureInternal, t0); err != nil {
			t.Fatalf("MarkFailed: %v", err)
		}
	})
}

func TestATerminalTransactionCannotBeChangedAgain(t *testing.T) {
	finish := map[string]func(*WagerTransaction) error{
		"processed": func(tx *WagerTransaction) error { return tx.MarkProcessed(brl(t, "1.00"), 1, t0) },
		"rejected":  func(tx *WagerTransaction) error { return tx.MarkRejected(FailureInsufficientFunds, t0) },
		"failed":    func(tx *WagerTransaction) error { return tx.MarkFailed(FailureInternal, t0) },
	}
	attempts := map[string]func(*WagerTransaction) error{
		"to processed": func(tx *WagerTransaction) error { return tx.MarkProcessed(brl(t, "1.00"), 1, t0) },
		"to rejected":  func(tx *WagerTransaction) error { return tx.MarkRejected(FailureInsufficientFunds, t0) },
		"to failed":    func(tx *WagerTransaction) error { return tx.MarkFailed(FailureInternal, t0) },
		"to pending reference": func(tx *WagerTransaction) error {
			return tx.MarkPendingReference(t0, t0, t0)
		},
		"a retry": func(tx *WagerTransaction) error { return tx.RetryReference(t0, t0) },
		"a link":  func(tx *WagerTransaction) error { return tx.LinkReference(id(9), t0) },
	}

	for endName, end := range finish {
		for attemptName, attempt := range attempts {
			t.Run(endName+" then "+attemptName, func(t *testing.T) {
				tx := newReversal(t)
				if err := end(tx); err != nil {
					t.Fatalf("reaching the terminal state: %v", err)
				}
				before := tx.Snapshot()

				if err := attempt(tx); !errors.Is(err, ErrInvalidTransition) {
					t.Fatalf("error = %v, want ErrInvalidTransition", err)
				}
				if after := tx.Snapshot(); !equalSnapshots(before, after) {
					t.Fatalf("a refused transition changed the transaction:\n before %+v\n after  %+v", before, after)
				}
			})
		}
	}
}

func TestOnlyAPendingReferenceTransactionRetries(t *testing.T) {
	tx := newReversal(t)
	if err := tx.RetryReference(t0, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("retrying a PENDING transaction: error = %v", err)
	}

	mustPend(t, tx)
	if err := tx.RetryReference(t0.Add(time.Hour), t0.Add(time.Minute)); err != nil {
		t.Fatalf("RetryReference: %v", err)
	}
	if tx.Status() != StatusPendingReference || tx.ReferenceAttempts() != 1 || !tx.ReferenceNextAttemptAt().Equal(t0.Add(time.Hour)) {
		t.Fatalf("status %s, attempts %d, next %s", tx.Status(), tx.ReferenceAttempts(), tx.ReferenceNextAttemptAt())
	}
}

func TestTheWaitForAReferenceEndsByAttemptsOrByExpiry(t *testing.T) {
	tx := newReversal(t)
	if err := tx.MarkPendingReference(t0, t0.Add(time.Hour), t0); err != nil {
		t.Fatalf("MarkPendingReference: %v", err)
	}
	if tx.ReferenceExhausted(t0, 3) {
		t.Fatal("a fresh wait is not exhausted")
	}
	for range 3 {
		if err := tx.RetryReference(t0, t0); err != nil {
			t.Fatalf("RetryReference: %v", err)
		}
	}
	if !tx.ReferenceExhausted(t0, 3) {
		t.Fatal("three attempts must exhaust a limit of three")
	}

	expiring := newReversal(t)
	if err := expiring.MarkPendingReference(t0, t0.Add(time.Hour), t0); err != nil {
		t.Fatalf("MarkPendingReference: %v", err)
	}
	if expiring.ReferenceExhausted(t0.Add(time.Hour-time.Nanosecond), 100) {
		t.Fatal("not yet expired")
	}
	if !expiring.ReferenceExhausted(t0.Add(time.Hour), 100) {
		t.Fatal("the expiry instant must end the wait")
	}
}

func TestAnInvalidTransitionInputIsRefusedBeforeTheStateChanges(t *testing.T) {
	tx := newTransaction(t, nil)
	if err := tx.MarkProcessed(Money{}, 1, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("uninitialised result: error = %v", err)
	}
	if err := tx.MarkProcessed(mustMinor(t, -1, "BRL"), 1, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("negative result: error = %v", err)
	}
	if err := tx.MarkProcessed(mustMoney(t, "1.00", "USD"), 1, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("another currency: error = %v", err)
	}
	if err := tx.MarkProcessed(brl(t, "1.00"), 0, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("version zero: error = %v", err)
	}
	if err := tx.MarkRejected("", t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("rejection without a code: error = %v", err)
	}
	if err := tx.MarkFailed("", t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("failure without a code: error = %v", err)
	}
	if err := tx.MarkPendingReference(t0, t0, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("a bet cannot wait for a reference: error = %v", err)
	}
	if err := tx.LinkReference([16]byte{}, t0); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("a nil reference id: error = %v", err)
	}
	if tx.Status() != StatusPending {
		t.Fatalf("a refused input moved the transaction to %s", tx.Status())
	}
}

func TestLinkingAReferenceRecordsItsResolvedId(t *testing.T) {
	tx := newReversal(t)
	if err := tx.LinkReference(id(9), t0); err != nil {
		t.Fatalf("LinkReference: %v", err)
	}
	if tx.ReferenceTransactionID() != id(9) {
		t.Fatalf("ReferenceTransactionID = %s", tx.ReferenceTransactionID())
	}
}

// --- references ------------------------------------------------------------------------------

func TestAReversalOfAProcessedCompatibleReferenceIsReady(t *testing.T) {
	bet := processed(t, newTransaction(t, nil))
	refund := newReversal(t)

	outcome, err := refund.EvaluateReference(bet)

	if err != nil || outcome != ReferenceReady {
		t.Fatalf("outcome %d, error %v", outcome, err)
	}
}

func TestAReversalWaitsForAReferenceThatIsStillPending(t *testing.T) {
	t.Run("a bet that is still pending", func(t *testing.T) {
		outcome, err := newReversal(t).EvaluateReference(newTransaction(t, nil))
		if err != nil || outcome != ReferenceWaiting {
			t.Fatalf("outcome %d, error %v", outcome, err)
		}
	})
	t.Run("a refund that is itself waiting for its reference", func(t *testing.T) {
		waiting := newReversal(t)
		mustPend(t, waiting)
		rollback := newTransaction(t, func(op *ExternalOperation) {
			op.ExternalTransactionID = "rollback-1"
			op.IdempotencyKey = "provider-a:rollback-1"
			op.Kind = "ROLLBACK"
			op.ReferenceExternalTransactionID = "refund-1"
		})

		outcome, err := rollback.EvaluateReference(waiting)

		if err != nil || outcome != ReferenceWaiting {
			t.Fatalf("outcome %d, error %v", outcome, err)
		}
	})
}

func TestAReversalOfAReferenceThatEndedWithoutSuccessIsRejected(t *testing.T) {
	rejected := newTransaction(t, nil)
	if err := rejected.MarkRejected(FailureInsufficientFunds, t0); err != nil {
		t.Fatal(err)
	}
	failed := newTransaction(t, nil)
	if err := failed.MarkFailed(FailureInternal, t0); err != nil {
		t.Fatal(err)
	}

	for name, reference := range map[string]*WagerTransaction{"rejected": rejected, "failed": failed} {
		_, err := newReversal(t).EvaluateReference(reference)
		if code, ok := RejectionCode(err); !ok || code != FailureReferenceNotProcessed {
			t.Errorf("%s: error = %v, want %s", name, err, FailureReferenceNotProcessed)
		}
	}
}

func TestAReversalThatDisagreesWithItsReferenceIsRejected(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ExternalOperation)
		want   FailureCode
	}{
		{"another provider", func(op *ExternalOperation) { op.ProviderID = "provider-b" }, FailureReferenceMismatch},
		{"another player", func(op *ExternalOperation) { op.PlayerID = id(77).String() }, FailureReferenceMismatch},
		{"another wallet", func(op *ExternalOperation) { op.WalletID = id(78).String() }, FailureReferenceMismatch},
		{"another currency", func(op *ExternalOperation) { op.Money = mustMoney(t, "25.00", "USD") }, FailureReferenceMismatch},
		{"another round", func(op *ExternalOperation) { op.RoundID = "round-988" }, FailureReferenceMismatch},
		{"another amount", func(op *ExternalOperation) { op.Money = brl(t, "20.00") }, FailureAmountMismatch},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bet := processed(t, newTransaction(t, nil))
			refund := newTransaction(t, func(op *ExternalOperation) {
				op.Kind, op.ReferenceExternalTransactionID = "REFUND", "transaction-123"
				tc.mutate(op)
			})
			_, err := refund.EvaluateReference(bet)
			if code, ok := RejectionCode(err); !ok || code != tc.want {
				t.Fatalf("error = %v, want %s", err, tc.want)
			}
		})
	}
}

func TestAReversalOnlyUndoesWhatItsKindMayUndo(t *testing.T) {
	kinds := map[string]TransactionKind{"BET": KindBet, "WIN": KindWin, "LOSS": KindLoss, "REFUND": KindRefund, "ROLLBACK": KindRollback}
	allowed := map[string]map[TransactionKind]bool{
		"REFUND":   {KindBet: true},
		"ROLLBACK": {KindBet: true, KindWin: true, KindRefund: true},
	}
	for reversal, undoable := range allowed {
		for name, kind := range kinds {
			t.Run(reversal+" of "+name, func(t *testing.T) {
				reference := newTransaction(t, func(op *ExternalOperation) {
					op.Kind = string(kind)
					switch kind {
					case KindLoss:
						op.Money = brl(t, "25.00")
					case KindRefund, KindRollback:
						op.ReferenceExternalTransactionID = "bet-1"
					}
				})
				processed(t, reference)
				reversalTx := newTransaction(t, func(op *ExternalOperation) {
					op.Kind, op.ReferenceExternalTransactionID = reversal, "transaction-123"
				})

				_, err := reversalTx.EvaluateReference(reference)

				if undoable[kind] {
					if err != nil {
						t.Fatalf("a %s may undo a %s, got %v", reversal, name, err)
					}
					return
				}
				if code, ok := RejectionCode(err); !ok || code != FailureReferenceMismatch {
					t.Fatalf("error = %v, want %s", err, FailureReferenceMismatch)
				}
			})
		}
	}
}

func TestOnlyAReversalEvaluatesAReference(t *testing.T) {
	bet := newTransaction(t, nil)
	if _, err := bet.EvaluateReference(processed(t, newTransaction(t, nil))); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("a bet has no reference: error = %v", err)
	}
	if _, err := newReversal(t).EvaluateReference(nil); !errors.Is(err, ErrInvalidTransaction) {
		t.Errorf("a nil reference: error = %v", err)
	}
}

// --- the idempotency hash --------------------------------------------------------------------

func TestTheHashIsTheSameForEquivalentForms(t *testing.T) {
	base := newTransaction(t, nil)
	equivalent := []struct {
		name   string
		mutate func(*ExternalOperation)
	}{
		{"amount without decimals", func(op *ExternalOperation) { op.Money = mustMoney(t, "25", "BRL") }},
		{"amount with one decimal", func(op *ExternalOperation) { op.Money = mustMoney(t, "25.0", "BRL") }},
		{"lower case currency", func(op *ExternalOperation) { op.Money = mustMoney(t, "25.00", "brl") }},
		{"lower case kind", func(op *ExternalOperation) { op.Kind = "bet" }},
		{"upper case UUIDs", func(op *ExternalOperation) {
			op.PlayerID = strings.ToUpper(id(1).String())
			op.WalletID = strings.ToUpper(id(2).String())
		}},
		{"another idempotency key", func(op *ExternalOperation) { op.IdempotencyKey = "something-else-entirely" }},
	}
	for _, tc := range equivalent {
		t.Run(tc.name, func(t *testing.T) {
			other := newTransaction(t, tc.mutate)
			if !base.MatchesPayload(other.PayloadHash()) {
				t.Fatalf("the hash differs for an equivalent operation")
			}
		})
	}
}

func TestTheHashChangesWithEveryBusinessField(t *testing.T) {
	base := newTransaction(t, nil)
	different := []struct {
		name   string
		mutate func(*ExternalOperation)
	}{
		{"provider", func(op *ExternalOperation) { op.ProviderID = "provider-b" }},
		{"external id", func(op *ExternalOperation) { op.ExternalTransactionID = "transaction-124" }},
		{"player", func(op *ExternalOperation) { op.PlayerID = id(5).String() }},
		{"wallet", func(op *ExternalOperation) { op.WalletID = id(6).String() }},
		{"round", func(op *ExternalOperation) { op.RoundID = "round-988" }},
		{"game", func(op *ExternalOperation) { op.GameID = "other-game" }},
		{"kind", func(op *ExternalOperation) { op.Kind = "WIN" }},
		{"amount", func(op *ExternalOperation) { op.Money = brl(t, "25.01") }},
		{"currency", func(op *ExternalOperation) { op.Money = mustMoney(t, "25.00", "USD") }},
		{"reference", func(op *ExternalOperation) { op.Kind, op.ReferenceExternalTransactionID = "WIN", "bet-1" }},
	}
	seen := map[string]string{string(base.PayloadHash()): "the base operation"}
	for _, tc := range different {
		t.Run(tc.name, func(t *testing.T) {
			other := newTransaction(t, tc.mutate)
			if base.MatchesPayload(other.PayloadHash()) {
				t.Fatalf("changing the %s did not change the hash", tc.name)
			}
			if previous, dup := seen[string(other.PayloadHash())]; dup {
				t.Fatalf("the hash of a changed %s collides with %s", tc.name, previous)
			}
			seen[string(other.PayloadHash())] = tc.name
		})
	}
}

func TestTheCanonicalPayloadHasSortedKeysAndNoTransportMetadata(t *testing.T) {
	payload := OperationPayload{
		ExternalTransactionID:          "transaction-123",
		GameID:                         "fortune-chimp",
		Kind:                           KindRefund,
		Money:                          brl(t, "25"),
		PlayerID:                       id(1).String(),
		ProviderID:                     "provider-a",
		ReferenceExternalTransactionID: "bet-1",
		RoundID:                        "round-987",
		WalletID:                       id(2).String(),
	}

	canonical, err := payload.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}

	want := `{"externalTransactionId":"transaction-123","gameId":"fortune-chimp","kind":"REFUND",` +
		`"money":{"amount":"25.00","currency":"BRL"},"playerId":"00000000-0000-7000-8000-000000000001",` +
		`"providerId":"provider-a","referenceExternalTransactionId":"bet-1","roundId":"round-987",` +
		`"walletId":"00000000-0000-7000-8000-000000000002"}`
	if string(canonical) != want {
		t.Fatalf("canonical JSON:\n got  %s\n want %s", canonical, want)
	}
}

func TestAnAbsentReferenceIsOmittedFromTheCanonicalPayload(t *testing.T) {
	canonical, err := OperationPayload{Kind: KindBet, Money: brl(t, "1.00")}.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if bytes.Contains(canonical, []byte("referenceExternalTransactionId")) {
		t.Fatalf("an absent reference must be omitted, not empty: %s", canonical)
	}
	if bytes.Contains(canonical, []byte("null")) {
		t.Fatalf("no field may be null: %s", canonical)
	}
}

func TestTheCanonicalPayloadDoesNotEscapeHTMLCharacters(t *testing.T) {
	canonical, err := OperationPayload{ProviderID: "a&b<c>", Kind: KindBet, Money: brl(t, "1.00")}.CanonicalJSON()
	if err != nil {
		t.Fatalf("CanonicalJSON: %v", err)
	}
	if !bytes.Contains(canonical, []byte(`"a&b<c>"`)) {
		t.Fatalf("HTML characters were escaped: %s", canonical)
	}
}

func TestTheSameKeyWithADifferentPayloadIsAConflictAndTheSamePayloadIsAReplay(t *testing.T) {
	stored := newTransaction(t, nil)

	replay := newTransaction(t, nil)
	if !stored.MatchesPayload(replay.PayloadHash()) {
		t.Fatal("the same content under the same key must match")
	}
	conflicting := newTransaction(t, func(op *ExternalOperation) { op.Money = brl(t, "30.00") })
	if stored.MatchesPayload(conflicting.PayloadHash()) {
		t.Fatal("different content under the same key must not match")
	}
}

// --- persistence shape -----------------------------------------------------------------------

func TestRehydratingATransactionRestoresItWithoutReapplyingAnything(t *testing.T) {
	tx := newReversal(t)
	if err := tx.LinkReference(id(9), t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.MarkPendingReference(t0.Add(time.Minute), t0.Add(time.Hour), t0); err != nil {
		t.Fatal(err)
	}
	if err := tx.RetryReference(t0.Add(2*time.Minute), t0); err != nil {
		t.Fatal(err)
	}
	stored := tx.Snapshot()

	restored, err := RehydrateWagerTransaction(stored)
	if err != nil {
		t.Fatalf("RehydrateWagerTransaction: %v", err)
	}

	if !equalSnapshots(restored.Snapshot(), stored) {
		t.Fatalf("round trip differs:\n got  %+v\n want %+v", restored.Snapshot(), stored)
	}
	if restored.Status() != StatusPendingReference || restored.ReferenceAttempts() != 1 {
		t.Fatalf("status %s, attempts %d", restored.Status(), restored.ReferenceAttempts())
	}
	// It is still a live transaction: the machine continues from where it was stored.
	if err := restored.MarkProcessed(brl(t, "1000.00"), 3, t0); err != nil {
		t.Fatalf("a rehydrated pending transaction must still be able to finish: %v", err)
	}
}

func TestRehydratingATerminalTransactionKeepsItTerminal(t *testing.T) {
	stored := processed(t, newTransaction(t, nil)).Snapshot()

	restored, err := RehydrateWagerTransaction(stored)
	if err != nil {
		t.Fatalf("RehydrateWagerTransaction: %v", err)
	}
	if err := restored.MarkRejected(FailureInsufficientFunds, t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("error = %v, want ErrInvalidTransition", err)
	}
	if restored.ResultBalance().Amount() != "975.00" || restored.ResultWalletVersion() != 2 {
		t.Fatalf("result = %s at version %d", restored.ResultBalance(), restored.ResultWalletVersion())
	}
}

func TestRehydratingAnOpeningTransactionKeepsItsNullsAsNulls(t *testing.T) {
	opening, err := OpenWallet(OpeningIDs{Wallet: id(2), Transaction: id(3), Entry: id(4)}, id(1), brl(t, "10.00"), t0)
	if err != nil {
		t.Fatal(err)
	}
	stored := opening.Transaction.Snapshot()

	restored, err := RehydrateWagerTransaction(stored)
	if err != nil {
		t.Fatalf("RehydrateWagerTransaction: %v", err)
	}
	if !equalSnapshots(restored.Snapshot(), stored) {
		t.Fatalf("round trip differs:\n got  %+v\n want %+v", restored.Snapshot(), stored)
	}
}

func TestRehydratingATransactionRejectsWhatCouldNotExist(t *testing.T) {
	valid := newTransaction(t, nil).Snapshot()
	cases := []struct {
		name   string
		mutate func(*WagerTransactionSnapshot)
	}{
		{"unknown origin", func(s *WagerTransactionSnapshot) { s.Origin = "OUTER" }},
		{"unknown kind", func(s *WagerTransactionSnapshot) { s.Kind = "GAMBLE" }},
		{"unknown status", func(s *WagerTransactionSnapshot) { s.Status = "LIMBO" }},
		{"unknown currency", func(s *WagerTransactionSnapshot) { s.Currency = "ZZZ" }},
		{"an opening that is external", func(s *WagerTransactionSnapshot) { s.Kind = "OPENING" }},
		{"a bet that is internal", func(s *WagerTransactionSnapshot) { s.Origin = "INTERNAL" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := valid
			tc.mutate(&s)
			if _, err := RehydrateWagerTransaction(s); !errors.Is(err, ErrInvalidTransaction) {
				t.Fatalf("error = %v, want ErrInvalidTransaction", err)
			}
		})
	}
}

func TestTheStatusHelpers(t *testing.T) {
	for _, status := range []TransactionStatus{StatusProcessed, StatusRejected, StatusFailed} {
		if !status.IsTerminal() {
			t.Errorf("%s must be terminal", status)
		}
	}
	for _, status := range []TransactionStatus{StatusPending, StatusPendingReference} {
		if status.IsTerminal() {
			t.Errorf("%s must not be terminal", status)
		}
	}
	if !KindRefund.IsReversal() || !KindRollback.IsReversal() || KindBet.IsReversal() || KindWin.IsReversal() {
		t.Error("only REFUND and ROLLBACK are reversals")
	}
}

// --- helpers ---------------------------------------------------------------------------------

func newReversal(t *testing.T) *WagerTransaction {
	t.Helper()
	return newTransaction(t, func(op *ExternalOperation) {
		op.ExternalTransactionID = "refund-1"
		op.IdempotencyKey = "provider-a:refund-1"
		op.Kind = "REFUND"
		op.ReferenceExternalTransactionID = "transaction-123"
	})
}

func mustPend(t *testing.T, tx *WagerTransaction) {
	t.Helper()
	if err := tx.MarkPendingReference(t0.Add(time.Minute), t0.Add(time.Hour), t0); err != nil {
		t.Fatalf("MarkPendingReference: %v", err)
	}
}

func equalSnapshots(a, b WagerTransactionSnapshot) bool {
	return reflect.DeepEqual(a, b)
}
