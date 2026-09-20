package wagering

import (
	"errors"
	"fmt"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// decision is what applying an operation to a wallet produced: the ledger entry when the balance
// moved, and the events that the outcome calls for. The wallet and the transaction are changed
// in place, in memory, and nothing reaches storage until store writes it.
type decision struct {
	entry  *entities.LedgerEntry
	events []*entities.OutboxEvent
}

// decide runs the domain rules of one operation against a locked wallet and leaves the
// transaction in its terminal state. A refusal by a business rule is an outcome like any other:
// the transaction ends REJECTED and is stored with its code. Only a failure that is not a
// business outcome is returned as an error, which rolls the whole unit of work back.
func (s *service) decide(wallet *entities.Wallet, tx *entities.WagerTransaction, correlationID string, now time.Time) (decision, error) {
	entry, err := s.move(wallet, tx, now)

	switch {
	case err == nil:
		return s.processed(wallet, tx, entry, correlationID, now)
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
	if err := tx.MarkRejected(code, now); err != nil {
		return decision{}, err
	}
	rejected, err := entities.NewWagerTransactionRejectedEvent(s.newID(), tx, correlationID, "", now)
	if err != nil {
		return decision{}, err
	}
	return decision{events: []*entities.OutboxEvent{rejected}}, nil
}

// move applies the operation to the wallet. It returns the ledger entry of the movement, or nil
// for an operation that moves nothing, and it leaves the wallet as it was on any error.
func (s *service) move(wallet *entities.Wallet, tx *entities.WagerTransaction, now time.Time) (*entities.LedgerEntry, error) {
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
	if !tx.MovesBalance() {
		return nil, nil
	}

	direction, err := tx.Movement(nil)
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
