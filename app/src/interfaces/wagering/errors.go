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
	// ErrIdempotencyConflict is an operation that contradicts one already received: the same key
	// carrying other content, or the same provider and external id arriving under another key.
	// Nothing is applied, and the operation that was received first stays as it was.
	ErrIdempotencyConflict = errors.New("the idempotency key or the operation was already used with other content")
	// ErrKindNotSupported is an operation of a kind the system does not process yet.
	ErrKindNotSupported = errors.New("this kind of operation is not supported yet")
	// ErrStale is an update of a transaction that is gone or already settled.
	ErrStale = errors.New("transaction is not in a state that can change")
)
