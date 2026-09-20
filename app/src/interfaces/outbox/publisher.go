package outbox

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Publisher sends an event to the broker its consumers read from.
//
// It is at-least-once: an error means the event may or may not have been accepted, and the same
// event is sent again later. The event id is stable, and is what an implementation hands the
// broker to deduplicate on, so a repeat is recognisable to whoever consumes it.
type Publisher interface {
	Publish(ctx context.Context, event *entities.OutboxEvent) error
}
