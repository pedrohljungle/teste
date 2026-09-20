// Package outbox is the contract of the outbox: where integration events are stored in the
// same commit as the change that caused them.
package outbox

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Repository stores outbox events.
type Repository interface {
	// Insert stores an event. It fails with persistence.ErrNoTransaction outside of a unit of
	// work, because an event stored on its own could outlive the change it describes: the event
	// is only allowed to exist if the commit that caused it did.
	Insert(ctx context.Context, event *entities.OutboxEvent) error
}
