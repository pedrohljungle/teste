package entities

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Direction is which way a ledger entry moved the balance.
type Direction string

// Ledger directions.
const (
	DirectionDebit  Direction = "DEBIT"
	DirectionCredit Direction = "CREDIT"
)

// Valid reports whether the direction is one of the two.
func (d Direction) Valid() bool {
	return d == DirectionDebit || d == DirectionCredit
}

// LedgerEntry is one immutable line of a wallet's ledger. It records the balance before and
// after the movement, so the ledger can be audited without replaying anything.
//
// It is append-only by nature: there is no method that changes it, and the database refuses
// an UPDATE or a DELETE against it. A correction is a new entry.
type LedgerEntry struct {
	id            uuid.UUID
	seq           int64
	walletID      uuid.UUID
	transactionID uuid.UUID
	direction     Direction
	money         Money
	before        Money
	after         Money
	createdAt     time.Time
}

// NewLedgerEntry builds an entry and validates that balanceAfter is balanceBefore plus or
// minus the amount, according to the direction. It is the only place that arithmetic is
// checked, which is why a hand-built entry cannot exist.
func NewLedgerEntry(id, walletID, transactionID uuid.UUID, direction Direction, money, before, after Money, at time.Time) (LedgerEntry, error) {
	if id == uuid.Nil || walletID == uuid.Nil || transactionID == uuid.Nil {
		return LedgerEntry{}, fmt.Errorf("%w: ids are required", ErrInvalidLedgerEntry)
	}
	if !direction.Valid() {
		return LedgerEntry{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, direction)
	}
	if !money.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: the amount must be positive, got %s", ErrInvalidLedgerEntry, money)
	}
	if !before.IsInitialized() || !after.IsInitialized() {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, ErrUninitializedMoney)
	}
	if before.IsNegative() || after.IsNegative() {
		return LedgerEntry{}, fmt.Errorf("%w: a balance cannot be negative", ErrInvalidLedgerEntry)
	}

	var expected Money
	var err error
	switch direction {
	case DirectionCredit:
		expected, err = before.Add(money)
	case DirectionDebit:
		expected, err = before.Sub(money)
	}
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	equal, err := expected.Equal(after)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	if !equal {
		return LedgerEntry{}, fmt.Errorf("%w: balanceAfter %s is not balanceBefore %s %s %s",
			ErrInvalidLedgerEntry, after, before, sign(direction), money)
	}

	return LedgerEntry{
		id:            id,
		walletID:      walletID,
		transactionID: transactionID,
		direction:     direction,
		money:         money,
		before:        before,
		after:         after,
		createdAt:     at.UTC(),
	}, nil
}

func sign(d Direction) string {
	if d == DirectionCredit {
		return "+"
	}
	return "-"
}

// ID is the entry identity.
func (e LedgerEntry) ID() uuid.UUID { return e.id }

// Seq is the position the database gave the entry, zero until it is stored. Pagination orders
// by it.
func (e LedgerEntry) Seq() int64 { return e.seq }

// WalletID is the wallet the entry belongs to.
func (e LedgerEntry) WalletID() uuid.UUID { return e.walletID }

// TransactionID is the transaction that caused the movement.
func (e LedgerEntry) TransactionID() uuid.UUID { return e.transactionID }

// Direction is whether the movement was a debit or a credit.
func (e LedgerEntry) Direction() Direction { return e.direction }

// Money is the amount moved.
func (e LedgerEntry) Money() Money { return e.money }

// BalanceBefore is the wallet balance before the movement.
func (e LedgerEntry) BalanceBefore() Money { return e.before }

// BalanceAfter is the wallet balance after the movement.
func (e LedgerEntry) BalanceAfter() Money { return e.after }

// CreatedAt is when the entry was written, in UTC.
func (e LedgerEntry) CreatedAt() time.Time { return e.createdAt }

// LedgerEntrySnapshot is the persisted shape of an entry, as a row of wallet_ledger_entries.
type LedgerEntrySnapshot struct {
	ID                 uuid.UUID `db:"id"`
	Seq                int64     `db:"seq"`
	WalletID           uuid.UUID `db:"wallet_id"`
	TransactionID      uuid.UUID `db:"transaction_id"`
	Direction          string    `db:"direction"`
	AmountMinor        int64     `db:"amount_minor"`
	Currency           string    `db:"currency"`
	BalanceBeforeMinor int64     `db:"balance_before_minor"`
	BalanceAfterMinor  int64     `db:"balance_after_minor"`
	CreatedAt          time.Time `db:"created_at"`
}

// Snapshot is what the repository writes.
func (e LedgerEntry) Snapshot() LedgerEntrySnapshot {
	return LedgerEntrySnapshot{
		ID:                 e.id,
		Seq:                e.seq,
		WalletID:           e.walletID,
		TransactionID:      e.transactionID,
		Direction:          string(e.direction),
		AmountMinor:        e.money.Minor(),
		Currency:           string(e.money.Currency()),
		BalanceBeforeMinor: e.before.Minor(),
		BalanceAfterMinor:  e.after.Minor(),
		CreatedAt:          e.createdAt,
	}
}

// RehydrateLedgerEntry restores a stored entry. It moves nothing and emits nothing: the entry
// already happened, and this only gives it a shape again. The arithmetic is not checked here,
// because that is the construction's rule and the reconciliation is the detector for a stored
// row that broke it.
func RehydrateLedgerEntry(s LedgerEntrySnapshot) (LedgerEntry, error) {
	direction := Direction(s.Direction)
	if !direction.Valid() {
		return LedgerEntry{}, fmt.Errorf("%w: unknown direction %q", ErrInvalidLedgerEntry, s.Direction)
	}
	money, err := NewMoney(s.AmountMinor, s.Currency)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	before, err := NewMoney(s.BalanceBeforeMinor, s.Currency)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	after, err := NewMoney(s.BalanceAfterMinor, s.Currency)
	if err != nil {
		return LedgerEntry{}, fmt.Errorf("%w: %w", ErrInvalidLedgerEntry, err)
	}
	return LedgerEntry{
		id:            s.ID,
		seq:           s.Seq,
		walletID:      s.WalletID,
		transactionID: s.TransactionID,
		direction:     direction,
		money:         money,
		before:        before,
		after:         after,
		createdAt:     s.CreatedAt.UTC(),
	}, nil
}
