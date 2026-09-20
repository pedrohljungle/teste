package structs

import "github.com/estrategiahq/pedro-test/app/src/entities"

// WagerOutcome is what submitting an external operation came to: the transaction as it now
// stands, and whether it is the stored result of an operation that had already been received.
//
// A replay is not an error and not a second application: the operation was applied once, the
// first time, and this hands back what happened then.
type WagerOutcome struct {
	Transaction *entities.WagerTransaction
	Replay      bool
}
