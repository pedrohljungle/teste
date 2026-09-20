package wallet

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Service is what the wallet domain offers to whoever delivers a request to it.
type Service interface {
	// Open creates the wallet of a player in the currency of the initial balance. With a
	// positive balance it also records the OPENING transaction, its credit in the ledger and
	// the events, all in the commit that creates the wallet. It fails with ErrAlreadyExists
	// when the player already has a wallet in that currency, and with entities.ErrInvalidWallet
	// when the input cannot make a wallet.
	Open(ctx context.Context, playerID uuid.UUID, initialBalance entities.Money) (*entities.Wallet, error)
}
