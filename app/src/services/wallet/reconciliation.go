package wallet

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

func (s *service) Reconcile(ctx context.Context, walletID uuid.UUID) (result structs.Reconciliation, err error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wallet.Service.Reconcile",
		observability.String("walletId", walletID.String()))
	defer func() { end(err) }()

	// The stored balance and the sum of the ledger are read in one snapshot. Read one after the
	// other, a movement that commits between them would make a healthy wallet look divergent, and
	// the report would be as wrong as the thing it is checking.
	err = s.uow.Snapshot(ctx, func(ctx context.Context) error {
		wallet, err := s.wallets.Get(ctx, walletID)
		if err != nil {
			return err
		}
		totals, err := s.wallets.SumEntries(ctx, walletID)
		if err != nil {
			return err
		}
		result, err = reconcile(wallet, totals)
		return err
	})
	if err != nil {
		return structs.Reconciliation{}, err
	}

	if !result.Consistent {
		// Reported and never corrected: a balance that disagrees with its ledger is a fact for a
		// person to investigate, and rewriting it here would destroy the evidence.
		s.obs.Error(ctx, fmt.Errorf("%w: stored %s, rebuilt from the ledger %s, difference %s",
			walletiface.ErrBalanceDivergence, result.StoredBalance, result.CalculatedBalance, result.Difference),
			"wallet balance diverges from its ledger",
			observability.String("walletId", result.WalletID),
			observability.Int("checkedEntries", result.CheckedEntries),
		)
	}
	return result, nil
}

// reconcile rebuilds the balance from the totals of the ledger and compares it with the wallet.
func reconcile(wallet *entities.Wallet, totals walletiface.Totals) (structs.Reconciliation, error) {
	currency := string(wallet.Currency())
	credits, err := entities.NewMoney(totals.Credits, currency)
	if err != nil {
		return structs.Reconciliation{}, err
	}
	debits, err := entities.NewMoney(totals.Debits, currency)
	if err != nil {
		return structs.Reconciliation{}, err
	}
	calculated, err := credits.Sub(debits)
	if err != nil {
		return structs.Reconciliation{}, err
	}
	difference, err := wallet.Balance().Sub(calculated)
	if err != nil {
		return structs.Reconciliation{}, err
	}

	return structs.Reconciliation{
		WalletID:          wallet.ID().String(),
		StoredBalance:     wallet.Balance(),
		CalculatedBalance: calculated,
		Difference:        difference,
		Consistent:        difference.IsZero(),
		CheckedEntries:    totals.Entries,
	}, nil
}
