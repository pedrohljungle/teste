package entities

import (
	"errors"
	"fmt"
)

// Domain errors. Each is a sentinel, so a caller classifies a failure with errors.Is instead of
// reading a message, and a panic never stands in for a business outcome.
var (
	// ErrInvalidWallet is a wallet that cannot exist: a nil id, an unknown currency, a negative
	// balance or a version below one.
	ErrInvalidWallet = errors.New("invalid wallet")
	// ErrInvalidTransaction is a transaction that is malformed, as opposed to one that a
	// business rule refuses. Nothing is persisted for it.
	ErrInvalidTransaction = errors.New("invalid transaction")
	// ErrInvalidLedgerEntry is a ledger entry whose arithmetic or shape does not hold.
	ErrInvalidLedgerEntry = errors.New("invalid ledger entry")
	// ErrInvalidTransition is a state change the state machine does not allow, which includes
	// every change out of a terminal state.
	ErrInvalidTransition = errors.New("invalid state transition")
	// ErrInvalidEvent is an event that cannot be built from what it was given.
	ErrInvalidEvent = errors.New("invalid event")
	// ErrInvalidInboxMessage is an inbox record that cannot exist.
	ErrInvalidInboxMessage = errors.New("invalid inbox message")
	// ErrNonPositiveAmount is a movement of zero or less. A movement always moves something.
	ErrNonPositiveAmount = errors.New("movement amount must be positive")
	// ErrInsufficientFunds is a debit above the balance. Which failure code it becomes depends
	// on the kind of transaction that attempted it, see FailureCodeOf.
	ErrInsufficientFunds = errors.New("insufficient funds")
	// ErrRejected matches every *Rejection, whatever its code.
	ErrRejected = errors.New("rejected")
)

// FailureCode is the stable, documented reason a transaction did not succeed. It is what a
// provider reads to tell an input it can correct from a definitive outcome.
type FailureCode string

// Failure codes. The two "insufficient funds" codes are different on purpose: a reversal that
// would overdraw the wallet must not be mistaken for a bet placed without balance.
const (
	FailureInsufficientFunds         FailureCode = "INSUFFICIENT_FUNDS"
	FailureRollbackInsufficientFunds FailureCode = "ROLLBACK_INSUFFICIENT_FUNDS"
	FailureReferenceNotFound         FailureCode = "REFERENCE_NOT_FOUND"
	FailureReferenceNotProcessed     FailureCode = "REFERENCE_NOT_PROCESSED"
	FailureReferenceAlreadyReversed  FailureCode = "REFERENCE_ALREADY_REVERSED"
	FailureReferenceMismatch         FailureCode = "REFERENCE_MISMATCH"
	FailureAmountMismatch            FailureCode = "AMOUNT_MISMATCH"
	FailureCurrencyMismatch          FailureCode = "CURRENCY_MISMATCH"
	FailureWalletNotFound            FailureCode = "WALLET_NOT_FOUND"
	FailurePlayerMismatch            FailureCode = "PLAYER_MISMATCH"
	FailureOpeningNotAllowed         FailureCode = "OPENING_NOT_ALLOWED"
	FailureInvalidAmount             FailureCode = "INVALID_AMOUNT"
	// FailureInternal is the code of a FAILED transaction: a permanent failure of the
	// infrastructure, recorded for audit, that no repetition would fix.
	FailureInternal FailureCode = "INTERNAL_ERROR"
)

// Correctable reports whether the provider can fix the request and try again, as opposed to a
// definitive result that a retry with the same content would only reproduce.
func (c FailureCode) Correctable() bool {
	switch c {
	case FailureAmountMismatch, FailureCurrencyMismatch, FailureWalletNotFound, FailurePlayerMismatch, FailureInvalidAmount:
		return true
	default:
		return false
	}
}

// Rejection is a business rule refusing an operation. It carries the failure code that is
// persisted with the transaction and returned to the provider, and it matches ErrRejected.
type Rejection struct {
	Code   FailureCode
	Detail string
}

// Reject builds a Rejection.
func Reject(code FailureCode, format string, args ...any) *Rejection {
	return &Rejection{Code: code, Detail: fmt.Sprintf(format, args...)}
}

func (r *Rejection) Error() string {
	return string(r.Code) + ": " + r.Detail
}

// Is makes every Rejection match the ErrRejected sentinel.
func (r *Rejection) Is(target error) bool {
	return target == ErrRejected
}

// RejectionCode extracts the failure code from an error chain, when there is a Rejection in it.
func RejectionCode(err error) (FailureCode, bool) {
	var rejection *Rejection
	if errors.As(err, &rejection) {
		return rejection.Code, true
	}
	return "", false
}

// FailureCodeOf classifies an error raised while applying a transaction of the given kind. The
// second value is false when the error is not a business outcome, in which case it is an
// infrastructure problem and the caller decides between retrying and failing.
func FailureCodeOf(kind TransactionKind, err error) (FailureCode, bool) {
	if code, ok := RejectionCode(err); ok {
		return code, true
	}
	switch {
	case errors.Is(err, ErrInsufficientFunds):
		if kind == KindRollback {
			return FailureRollbackInsufficientFunds, true
		}
		return FailureInsufficientFunds, true
	case errors.Is(err, ErrCurrencyMismatch):
		return FailureCurrencyMismatch, true
	default:
		return "", false
	}
}
