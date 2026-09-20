package outbox

import "context"

// Service drains the outbox. It is called by a periodic job, on every instance of the worker.
type Service interface {
	// PublishDue claims the events that are due and publishes them, and returns how many it
	// found. A positive count tells the caller there may be more waiting, so it should call
	// again at once. Publishing a single event failing is not an error of the call: that event
	// is rescheduled with a longer wait and the others go on. An error means the outbox itself
	// could not be read.
	PublishDue(ctx context.Context) (found int, err error)
}
