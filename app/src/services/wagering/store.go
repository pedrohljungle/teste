package wagering

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// store writes what a decision produced. It runs inside the unit of work, so it is one commit:
// the transaction, the ledger entry, the balance and the events land together or not at all.
//
// The transaction goes first. The ledger entry points at it, and so does the unique index that
// turns a second copy of the same operation into a refusal by the database.
func (s *service) store(ctx context.Context, wallet *entities.Wallet, tx *entities.WagerTransaction, d decision) error {
	if err := s.wagering.Insert(ctx, tx); err != nil {
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
