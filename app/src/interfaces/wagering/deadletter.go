package wagering

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// DeadLetter is where a message goes when no retry can make it work: it is malformed, or it
// contradicts something already stored. Sending it there, with the reason, is what lets a person
// find out why, instead of the queue redelivering it until its receive count runs out.
type DeadLetter interface {
	// Send stores the message, unchanged, together with why it was given up on.
	Send(ctx context.Context, message structs.QueueMessage, reason string) error
}
