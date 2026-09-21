// Package wallet is the contract of the wallet domain: what its storage offers.
package wallet

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Repository stores wallets and their ledger.
type Repository interface {
	// Insert stores a new wallet. It fails with ErrAlreadyExists when the player already has a
	// wallet in that currency.
	Insert(ctx context.Context, wallet *entities.Wallet) error

	// Get reads a wallet without locking it. It fails with ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*entities.Wallet, error)

	// GetForUpdate reads a wallet and locks its row until the transaction ends, which is what
	// makes two writers of the same wallet run one after the other. It fails with ErrNotFound,
	// and with persistence.ErrNoTransaction outside of a unit of work.
	GetForUpdate(ctx context.Context, id uuid.UUID) (*entities.Wallet, error)

	// Update writes the balance and the version, and only if the stored row still has the
	// version the aggregate was read with. It fails with ErrConcurrentUpdate otherwise, so an
	// update that lost a race is never applied over one that won. Like every write that has to
	// land with others, it fails with persistence.ErrNoTransaction outside of a unit of work.
	Update(ctx context.Context, wallet *entities.Wallet) error

	// InsertEntry appends a ledger entry. It fails with ErrDuplicateMovement when the
	// transaction already has an entry in that wallet, and with persistence.ErrNoTransaction
	// outside of a unit of work: an entry stored without its balance change is a corrupt ledger.
	InsertEntry(ctx context.Context, entry entities.LedgerEntry) error

	// ListEntries reads up to limit ledger entries of a wallet after the given position, oldest
	// first. Ordering by the position the database assigned makes a page stable: an entry written
	// later always has a higher position than every entry a client has already seen.
	ListEntries(ctx context.Context, walletID uuid.UUID, afterSeq int64, limit int) ([]entities.LedgerEntry, error)

	// SumEntries totals a wallet's ledger: how many entries, and the sum of the credits and of the
	// debits in minor units. It is what a balance is rebuilt from.
	SumEntries(ctx context.Context, walletID uuid.UUID) (Totals, error)
}

// Totals is the sum of a wallet's ledger, in minor units.
type Totals struct {
	Entries int
	Credits int64
	Debits  int64
}
