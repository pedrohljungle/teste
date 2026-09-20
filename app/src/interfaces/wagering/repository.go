// Package wagering is the contract of the wagering domain: what the storage of transactions
// offers.
package wagering

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Repository stores wager transactions.
type Repository interface {
	// Insert stores a new transaction. It fails with ErrDuplicate when the provider and
	// external id, the idempotency key or an opening for the wallet already exist.
	Insert(ctx context.Context, transaction *entities.WagerTransaction) error

	// Update writes the changes of a transaction that is not settled yet. It fails with
	// ErrStale when the row is gone or already terminal, so a transaction that reached a final
	// state is never rewritten. It fails with ErrAlreadyReversed when marking the transaction
	// PROCESSED would give its reference a second successful reversal.
	Update(ctx context.Context, transaction *entities.WagerTransaction) error

	// Get reads a transaction by id. It fails with ErrNotFound.
	Get(ctx context.Context, id uuid.UUID) (*entities.WagerTransaction, error)

	// FindByExternal reads a provider's transaction by the id the provider gave it. It fails
	// with ErrNotFound.
	FindByExternal(ctx context.Context, providerID, externalTransactionID string) (*entities.WagerTransaction, error)

	// FindByKey reads a provider's transaction by the idempotency key it arrived with. It fails
	// with ErrNotFound.
	FindByKey(ctx context.Context, providerID, idempotencyKey string) (*entities.WagerTransaction, error)
}
