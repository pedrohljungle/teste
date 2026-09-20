package wagering

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Service is what the wagering domain offers to whoever delivers an operation to it. HTTP and the
// queue both call the same Submit, which is what gives them the same rules and the same
// idempotency.
type Service interface {
	// Submit applies an external operation exactly once, however many times it is delivered.
	//
	// A malformed operation fails with entities.ErrInvalidTransaction, without storing anything.
	// An operation a business rule refuses is stored as REJECTED and returned as an outcome, so
	// that replaying it answers the same. The same key with the same content is a replay of the
	// stored outcome; the same key with other content, or the same operation under another key,
	// fails with ErrIdempotencyConflict.
	//
	// An operation the system cannot even attach to a wallet or a player, such as one for a wallet
	// that does not exist, fails with an *entities.Rejection and is not stored: there is nothing
	// for a transaction to belong to.
	Submit(ctx context.Context, operation entities.ExternalOperation) (structs.WagerOutcome, error)
}
