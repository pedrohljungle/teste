package persistence

import "errors"

// ErrUnavailable marks a failure of the storage that a retry may cure: the database is
// unreachable, a connection dropped, a statement timed out, or a transaction lost a
// serialisation or deadlock race. It is what tells "try again" from "this can never work".
var ErrUnavailable = errors.New("storage unavailable")

// ErrNoTransaction is an operation that needs an open transaction, such as a row lock, called
// outside of one. It is a programming error, not a runtime condition.
var ErrNoTransaction = errors.New("this operation requires an open transaction")
