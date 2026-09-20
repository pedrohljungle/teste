package wallet

import "errors"

var (
	// ErrNotFound is a wallet that does not exist.
	ErrNotFound = errors.New("wallet not found")
	// ErrAlreadyExists is a second wallet for the same player and currency.
	ErrAlreadyExists = errors.New("wallet already exists for the player and currency")
	// ErrConcurrentUpdate is an update that found the wallet at another version than the one it
	// was read with.
	ErrConcurrentUpdate = errors.New("wallet was changed concurrently")
	// ErrDuplicateMovement is a second ledger entry for a transaction that already moved the
	// wallet. It is the database refusing a movement the rules should already have stopped.
	ErrDuplicateMovement = errors.New("the transaction already moved this wallet")
)
