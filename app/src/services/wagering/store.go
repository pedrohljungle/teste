package wagering

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// persistence is how a transaction reaches storage: inserted when it arrived just now, updated when
// it was already there waiting for its reference.
type persistence int

const (
	insert persistence = iota
	update
)

// store writes what a decision produced. It runs inside the unit of work, so it is one commit:
// the transaction, the ledger entry, the balance and the events land together or not at all.
//
// The transaction goes first. The ledger entry points at it, and so does the unique index that
// turns a second copy of the same operation into a refusal by the database.
func (s *service) store(ctx context.Context, wallet *entities.Wallet, tx *entities.WagerTransaction, d decision, how persistence) error {
	if err := s.saveTransaction(ctx, tx, how); err != nil {
		return err
	}
	if d.entry != nil {
		if err := s.wallets.InsertEntry(ctx, *d.entry); err != nil {
			return err
		}
		if err := s.wallets.Update(ctx, wallet); err != nil {
			return err
		}
	}
	for _, event := range d.events {
		if err := s.outbox.Insert(ctx, event); err != nil {
			return err
		}
	}
	return nil
}

func (s *service) saveTransaction(ctx context.Context, tx *entities.WagerTransaction, how persistence) error {
	if how == update {
		return s.wagering.Update(ctx, tx)
	}
	return s.wagering.Insert(ctx, tx)
}
