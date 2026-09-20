// Package outbox is the contract of the outbox: where integration events are stored in the
// same commit as the change that caused them, and from where they are published afterwards.
package outbox

import (
	"context"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
)

// Repository stores outbox events and hands them to publishers.
type Repository interface {
	// Insert stores an event. It fails with persistence.ErrNoTransaction outside of a unit of
	// work, because an event stored on its own could outlive the change it describes: the event
	// is only allowed to exist if the commit that caused it did.
	Insert(ctx context.Context, event *entities.OutboxEvent) error

	// Claim reserves up to limit events that are due, for the publisher that names itself. Many
	// publishers may claim at once and none waits for another: each one gets events the others
	// did not. An event stays reserved for lease, and once the lease runs out it can be claimed
	// again, which is how the events of a publisher that died are recovered. Every claim counts
	// as an attempt.
	//
	// An event is not claimed while an earlier event of the same aggregate is still pending,
	// which keeps the events of one aggregate in the order they occurred however many
	// publishers run.
	Claim(ctx context.Context, publisher string, limit int, lease time.Duration, now time.Time) ([]*entities.OutboxEvent, error)

	// Complete stores that an event was published. It is a no-op for an event that is not
	// PENDING any more, which is what happens when two publishers both got the same event after a
	// lease ran out: the second finds it already done, and that is not an error.
	Complete(ctx context.Context, event *entities.OutboxEvent) error

	// Release stores the new time of a failed event and gives it back, but only if the publisher
	// still holds it. A publisher that lost its lease to another one must not overwrite what the
	// other one decided.
	Release(ctx context.Context, event *entities.OutboxEvent, publisher string) error
}
