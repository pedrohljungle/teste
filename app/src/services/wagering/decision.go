package wagering

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// errWaitingForReference is the internal signal that a reversal cannot be decided yet, because what
// it refers to has not arrived or has not finished. It is never returned to a caller: it becomes a
// PENDING_REFERENCE transaction, or another look later.
var errWaitingForReference = errors.New("waiting for the reference")

// decision is what applying an operation to a wallet produced: the ledger entry when the balance
// moved, and the events that the outcome calls for. The wallet and the transaction are changed
// in place, in memory, and nothing reaches storage until store writes it.
type decision struct {
	entry  *entities.LedgerEntry
	events []*entities.OutboxEvent
}

// resolution is what has to be known about the reference of a reversal before deciding it: the
// transaction it refers to, if that has arrived, and the reversal that already succeeded against
// it, if there is one. Both are read from storage, which is why they are gathered before the
// decision and not during it: the decision itself touches no storage.
type resolution struct {
	reference *entities.WagerTransaction
	reversal  *entities.WagerTransaction
}

// resolve reads what a reversal refers to. It has to run inside the unit of work, after the wallet
// lock, so that what it reads is what the decision is made on.
func (s *service) resolve(ctx context.Context, tx *entities.WagerTransaction) (resolution, error) {
	var found resolution
	if !tx.Kind().IsReversal() {
		return found, nil
	}

	reference, err := s.wagering.FindByExternal(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	switch {
	case err == nil:
		found.reference = reference
	case !isNotFound(err):
		return resolution{}, err
	}

	reversal, err := s.wagering.FindReversalOf(ctx, tx.ProviderID(), tx.ReferenceExternalTransactionID())
	switch {
	case err == nil && reversal.ID() != tx.ID():
		found.reversal = reversal
	case err != nil && !isNotFound(err):
		return resolution{}, err
	}
	return found, nil
}

// decide runs the domain rules of one operation against a locked wallet and leaves the
// transaction in its next state. A refusal by a business rule is an outcome like any other:
// the transaction ends REJECTED and is stored with its code. Only a failure that is not a
// business outcome is returned as an error, which rolls the whole unit of work back.
func (s *service) decide(wallet *entities.Wallet, tx *entities.WagerTransaction, found resolution, correlationID string, now time.Time) (decision, error) {
	entry, err := s.move(wallet, tx, found, now)

	switch {
	case err == nil:
		return s.processed(wallet, tx, entry, correlationID, now)
	case errors.Is(err, errWaitingForReference):
		return s.waiting(tx, correlationID, now)
	case errors.Is(err, entities.ErrMoneyOverflow):
		// A balance that cannot be represented is a permanent failure that no retry fixes. It is
		// kept for audit as FAILED, and it does not publish an event: nothing happened.
		if err := tx.MarkFailed(entities.FailureInternal, now); err != nil {
			return decision{}, err
		}
		return decision{}, nil
	}

	code, business := entities.FailureCodeOf(tx.Kind(), err)
	if !business {
		return decision{}, err
	}
	return s.rejected(tx, code, correlationID, now)
}

// rejected concludes an operation a business rule refused, and builds the event for it.
func (s *service) rejected(tx *entities.WagerTransaction, code entities.FailureCode, correlationID string, now time.Time) (decision, error) {
	if err := tx.MarkRejected(code, now); err != nil {
		return decision{}, err
	}
	event, err := entities.NewWagerTransactionRejectedEvent(s.newID(), tx, correlationID, "", now)
	if err != nil {
		return decision{}, err
	}
	return decision{events: []*entities.OutboxEvent{event}}, nil
}

// waiting parks a reversal whose reference has not arrived. The next look and the expiry are stored
// on the transaction, so another instance, or this one after a restart, picks the wait up where it
// was. A reversal that is already waiting is not parked again: the caller is the job that looks
// again, and it decides between another wait and giving up.
func (s *service) waiting(tx *entities.WagerTransaction, correlationID string, now time.Time) (decision, error) {
	if tx.Status() != entities.StatusPending {
		return decision{}, errWaitingForReference
	}
	if err := tx.MarkPendingReference(now.Add(s.referenceBackoff(1)), now.Add(s.cfg.TTL), now); err != nil {
		return decision{}, err
	}
	event, err := entities.NewWagerTransactionPendingReferenceEvent(s.newID(), tx, correlationID, "", now)
	if err != nil {
		return decision{}, err
	}
	return decision{events: []*entities.OutboxEvent{event}}, nil
}

// move applies the operation to the wallet. It returns the ledger entry of the movement, or nil
// for an operation that moves nothing, and it leaves the wallet as it was on any error.
func (s *service) move(wallet *entities.Wallet, tx *entities.WagerTransaction, found resolution, now time.Time) (*entities.LedgerEntry, error) {
	if err := tx.CheckAmountPolicy(); err != nil {
		return nil, err
	}
	if tx.PlayerID() != wallet.PlayerID() {
		return nil, entities.Reject(entities.FailurePlayerMismatch,
			"the wallet belongs to another player than the operation names")
	}
	// A LOSS moves nothing, but it still has to be in the currency of the wallet.
	if tx.Money().Currency() != wallet.Currency() {
		return nil, entities.Reject(entities.FailureCurrencyMismatch,
			"the wallet holds %s and the operation is in %s", wallet.Currency(), tx.Money().Currency())
	}
	if tx.Kind().IsReversal() {
		if err := s.checkReference(tx, found, now); err != nil {
			return nil, err
		}
	}
	if !tx.MovesBalance() {
		return nil, nil
	}

	direction, err := tx.Movement(found.reference)
	if err != nil {
		return nil, err
	}
	var entry entities.LedgerEntry
	if direction == entities.DirectionDebit {
		entry, err = wallet.Debit(s.newID(), tx.ID(), tx.Money(), now)
	} else {
		entry, err = wallet.Credit(s.newID(), tx.ID(), tx.Money(), now)
	}
	if err != nil {
		return nil, err
	}
	return &entry, nil
}

// checkReference decides whether a reversal can be applied to what it refers to. It waits when the
// reference has not arrived or has not finished, refuses when it disagrees with the reference or the
// reference ended without success, and refuses when the reference was already reversed.
//
// The last rule is one reversal per transaction, of any kind. The challenge only requires that two
// of the same kind not succeed, and this is stronger on purpose: a REFUND and a ROLLBACK of the same
// bet would each give the money back, and nothing about the order they arrive in should decide
// whether the player is paid twice. The database enforces it too, with a unique index.
func (s *service) checkReference(tx *entities.WagerTransaction, found resolution, now time.Time) error {
	if found.reference == nil {
		return errWaitingForReference
	}
	outcome, err := tx.EvaluateReference(found.reference)
	if err != nil {
		return err
	}
	if outcome == entities.ReferenceWaiting {
		return errWaitingForReference
	}
	if found.reversal != nil {
		return entities.Reject(entities.FailureReferenceAlreadyReversed,
			"the transaction was already reversed by %s", found.reversal.ExternalTransactionID())
	}
	return tx.LinkReference(found.reference.ID(), now)
}

// processed concludes a successful operation and builds its events: the fact that it was
// processed, which a LOSS also is, and the balance change when there was one.
func (s *service) processed(wallet *entities.Wallet, tx *entities.WagerTransaction, entry *entities.LedgerEntry, correlationID string, now time.Time) (decision, error) {
	if err := tx.MarkProcessed(wallet.Balance(), wallet.Version(), now); err != nil {
		return decision{}, err
	}
	processed, err := entities.NewWagerTransactionProcessedEvent(s.newID(), tx, correlationID, "", now)
	if err != nil {
		return decision{}, err
	}
	events := []*entities.OutboxEvent{processed}

	if entry != nil {
		changed, err := entities.NewWalletBalanceChangedEvent(s.newID(), *entry, wallet.Version(),
			correlationID, tx.ID().String(), now)
		if err != nil {
			return decision{}, fmt.Errorf("balance event: %w", err)
		}
		events = append(events, changed)
	}
	return decision{entry: entry, events: events}, nil
}
