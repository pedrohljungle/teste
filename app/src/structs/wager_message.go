package structs

import "github.com/estrategiahq/pedro-test/app/src/entities"

// WagerMessage is an operation as it arrives from the queue: the identity of the message, the
// operation it carries, and the hash of its exact content.
//
// The message id is what the inbox deduplicates on, and the hash is what tells a redelivery of the
// same message from a different message that reused its id. The operation has the same shape an
// HTTP request produces, which is what lets both entry points share one use case.
type WagerMessage struct {
	ID        string
	Operation entities.ExternalOperation
	Hash      []byte
}
