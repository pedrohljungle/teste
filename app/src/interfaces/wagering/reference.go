package wagering

import "context"

// ReferenceResolver resolves the reversals that are waiting for a transaction that had not arrived
// when they did. It is called by a periodic job, on every instance of the worker.
type ReferenceResolver interface {
	// ResolvePending looks again for the reference of the reversals that are due, and concludes
	// each one: applied when its reference is processed, rejected when its reference ended without
	// success or the wait is over, and rescheduled with a longer wait otherwise. It returns how many
	// it found, and a positive count tells the caller there may be more waiting.
	//
	// One reversal is one unit of work, so a failure in one does not undo the others. The state of
	// the wait, the attempts and the next look, is stored on the transaction, which is what makes a
	// restart lose nothing.
	ResolvePending(ctx context.Context) (found int, err error)
}
