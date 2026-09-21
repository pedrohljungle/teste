// Package inbox is the contract of the inbox: the durable record that a consumer received a
// message, kept in the same commit as what handling the message did.
package inbox

import (
	"context"
	"errors"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// ErrNotFound is a message the inbox has no record of.
var ErrNotFound = errors.New("inbox message not found")

// Repository stores inbox records.
type Repository interface {
	// Insert records a message as received. It reports false, and stores nothing, when the
	// consumer already has a record of the message id. The conflict is an answer and not an
	// error, so the surrounding transaction stays usable and the caller can read the record it
	// collided with. It fails with persistence.ErrNoTransaction outside of a unit of work: a
	// record that is not in the commit of its handling proves nothing.
	Insert(ctx context.Context, message *entities.InboxMessage) (inserted bool, err error)

	// Complete stores that the handling of the message is done, in the same unit of work.
	Complete(ctx context.Context, message *entities.InboxMessage) error

	// Find reads the record of a message. It fails with ErrNotFound.
	Find(ctx context.Context, consumerName, messageID string) (*entities.InboxMessage, error)
}
