package wallet

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

const (
	// defaultPageSize is the page a client that names no limit gets.
	defaultPageSize = 50
	// maxPageSize caps what one request can ask for. The cap is a rule of the service and not of
	// the DTO: a limit that travels inside a request is a limit that some caller forgets to apply.
	maxPageSize = 200
)

func (s *service) Get(ctx context.Context, id uuid.UUID) (wallet *entities.Wallet, err error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wallet.Service.Get")
	defer func() { end(err) }()

	return s.wallets.Get(ctx, id)
}

func (s *service) Ledger(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (page structs.LedgerPage, err error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wallet.Service.Ledger")
	defer func() { end(err) }()

	if limit < 0 {
		return structs.LedgerPage{}, walletiface.ErrInvalidPage
	}
	if limit == 0 {
		limit = defaultPageSize
	}
	limit = min(limit, maxPageSize)

	afterSeq, err := structs.DecodeCursor(cursor)
	if err != nil {
		return structs.LedgerPage{}, err
	}
	// An empty ledger and a wallet that does not exist look the same to a query, and are answered
	// differently.
	if _, err := s.wallets.Get(ctx, walletID); err != nil {
		return structs.LedgerPage{}, err
	}

	// One more than asked for tells whether there is a next page without a second query.
	entries, err := s.wallets.ListEntries(ctx, walletID, afterSeq, limit+1)
	if err != nil {
		return structs.LedgerPage{}, err
	}
	if len(entries) > limit {
		entries = entries[:limit]
		page.HasMore = true
	}
	page.Entries = entries
	if len(entries) > 0 {
		page.NextAfter = entries[len(entries)-1].Seq()
	}
	return page, nil
}
