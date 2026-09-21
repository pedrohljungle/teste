package wallet

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Service is what the wallet domain offers to whoever delivers a request to it.
type Service interface {
	// Open creates the wallet of a player in the currency of the initial balance. With a
	// positive balance it also records the OPENING transaction, its credit in the ledger and
	// the events, all in the commit that creates the wallet. It fails with ErrAlreadyExists
	// when the player already has a wallet in that currency, and with entities.ErrInvalidWallet
	// when the input cannot make a wallet.
	Open(ctx context.Context, playerID uuid.UUID, initialBalance entities.Money) (*entities.Wallet, error)

	// Get reads a wallet. It fails with ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*entities.Wallet, error)

	// Ledger reads one page of a wallet's ledger, oldest entry first, starting after the position
	// the cursor names, or at the start when it is empty. The page size is capped by the service. It
	// fails with ErrNotFound when the wallet does not exist, and with structs.ErrInvalidCursor for a
	// cursor this system did not issue.
	Ledger(ctx context.Context, walletID uuid.UUID, cursor string, limit int) (structs.LedgerPage, error)

	// Reconcile rebuilds a wallet balance from its ledger and compares it with the stored one, both
	// read from one consistent view of the data. It changes nothing: a divergence is reported in the
	// result, in the log and in a metric, and never corrected. It fails with ErrNotFound.
	Reconcile(ctx context.Context, walletID uuid.UUID) (structs.Reconciliation, error)
}
