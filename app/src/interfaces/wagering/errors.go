package wagering

import "errors"

var (
	// ErrNotFound is a transaction that does not exist.
	ErrNotFound = errors.New("transaction not found")
	// ErrDuplicate is a transaction that collides with a stored one on a unique identity: the
	// provider and external id, the idempotency key, or the opening of a wallet.
	ErrDuplicate = errors.New("transaction already exists")
	// ErrAlreadyReversed is a second successful reversal of one reference, refused by the
	// database.
	ErrAlreadyReversed = errors.New("the reference was already reversed")
	// ErrStale is an update of a transaction that is gone or already settled.
	ErrStale = errors.New("transaction is not in a state that can change")
)
