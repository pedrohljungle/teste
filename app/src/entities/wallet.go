package entities

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// Wallet is the financial aggregate root: the balance of one player in one currency, and the
// only thing allowed to change it.
//
// The balance moves through Credit and Debit and nowhere else, and each of them hands back the
// ledger entry that has to be stored in the same commit. A balance change without its entry is
// therefore not something a caller can express.
//
// Concurrency is coordinated by the database with a row lock; the version is what an update
// asserts and what events publish. It starts at one and grows only when the balance changes.
type Wallet struct {
	id       uuid.UUID
	playerID uuid.UUID
	currency Currency
	balance  Money
	version  int64
	// loadedVersion is the version this aggregate had when it was read, which is the one an
	// UPDATE has to find in the row. Zero means the wallet was just created.
	loadedVersion int64
	createdAt     time.Time
	updatedAt     time.Time
}

// OpeningIDs are the identities an opening needs. They come from the caller so the aggregate
// stays deterministic and testable.
type OpeningIDs struct {
	Wallet      uuid.UUID
	Transaction uuid.UUID
	Entry       uuid.UUID
}

// WalletOpening is everything a wallet opening produces, to be stored in one commit. The
// transaction and the entry are nil when the initial balance is zero: there is nothing to
// audit until there is money.
type WalletOpening struct {
	Wallet      *Wallet
	Transaction *WagerTransaction
	Entry       *LedgerEntry
}

// OpenWallet creates a wallet with its initial balance. With a positive balance it also
// creates the OPENING transaction, already PROCESSED, and the credit entry for it. The wallet
// starts at version one either way: opening is not a movement that bumps it.
func OpenWallet(ids OpeningIDs, playerID uuid.UUID, initial Money, at time.Time) (WalletOpening, error) {
	if ids.Wallet == uuid.Nil || playerID == uuid.Nil {
		return WalletOpening{}, fmt.Errorf("%w: wallet and player ids are required", ErrInvalidWallet)
	}
	if !initial.IsInitialized() {
		return WalletOpening{}, fmt.Errorf("%w: %w", ErrInvalidWallet, ErrUninitializedMoney)
	}
	if initial.IsNegative() {
		return WalletOpening{}, fmt.Errorf("%w: the initial balance cannot be negative", ErrInvalidWallet)
	}

	now := at.UTC()
	wallet := &Wallet{
		id:        ids.Wallet,
		playerID:  playerID,
		currency:  initial.Currency(),
		balance:   initial,
		version:   1,
		createdAt: now,
		updatedAt: now,
	}
	if initial.IsZero() {
		return WalletOpening{Wallet: wallet}, nil
	}

	transaction, err := NewOpeningTransaction(ids.Transaction, ids.Wallet, playerID, initial, now)
	if err != nil {
		return WalletOpening{}, err
	}
	zero, err := ZeroMoney(string(initial.Currency()))
	if err != nil {
		return WalletOpening{}, fmt.Errorf("%w: %w", ErrInvalidWallet, err)
	}
	entry, err := NewLedgerEntry(ids.Entry, ids.Wallet, ids.Transaction, DirectionCredit, initial, zero, initial, now)
	if err != nil {
		return WalletOpening{}, err
	}
	if err := transaction.MarkProcessed(initial, wallet.version, now); err != nil {
		return WalletOpening{}, err
	}
	return WalletOpening{Wallet: wallet, Transaction: transaction, Entry: &entry}, nil
}

// ID is the wallet identity.
func (w *Wallet) ID() uuid.UUID { return w.id }

// PlayerID is the owner.
func (w *Wallet) PlayerID() uuid.UUID { return w.playerID }

// Currency is the only currency this wallet holds.
func (w *Wallet) Currency() Currency { return w.currency }

// Balance is the current balance.
func (w *Wallet) Balance() Money { return w.balance }

// Version is the current version.
func (w *Wallet) Version() int64 { return w.version }

// ExpectedVersion is the version the stored row must have for an update to apply: the one this
// aggregate was read with. It is zero for a wallet that has never been stored.
func (w *Wallet) ExpectedVersion() int64 { return w.loadedVersion }

// CreatedAt is when the wallet was opened, in UTC.
func (w *Wallet) CreatedAt() time.Time { return w.createdAt }

// UpdatedAt is when the balance last changed, in UTC.
func (w *Wallet) UpdatedAt() time.Time { return w.updatedAt }

// Credit adds the amount to the balance and returns the ledger entry for it.
func (w *Wallet) Credit(entryID, transactionID uuid.UUID, amount Money, at time.Time) (LedgerEntry, error) {
	return w.move(DirectionCredit, entryID, transactionID, amount, at)
}

// Debit removes the amount from the balance and returns the ledger entry for it. A debit above
// the balance fails with ErrInsufficientFunds and changes nothing: the balance never goes
// negative.
func (w *Wallet) Debit(entryID, transactionID uuid.UUID, amount Money, at time.Time) (LedgerEntry, error) {
	return w.move(DirectionDebit, entryID, transactionID, amount, at)
}

func (w *Wallet) move(direction Direction, entryID, transactionID uuid.UUID, amount Money, at time.Time) (LedgerEntry, error) {
	if !amount.IsInitialized() {
		return LedgerEntry{}, ErrUninitializedMoney
	}
	if !amount.IsPositive() {
		return LedgerEntry{}, fmt.Errorf("%w: got %s", ErrNonPositiveAmount, amount)
	}
	if amount.Currency() != w.currency {
		return LedgerEntry{}, fmt.Errorf("%w: the wallet holds %s and the movement is in %s",
			ErrCurrencyMismatch, w.currency, amount.Currency())
	}

	before := w.balance
	var after Money
	var err error
	if direction == DirectionCredit {
		after, err = before.Add(amount)
	} else {
		if before.Minor() < amount.Minor() {
			return LedgerEntry{}, fmt.Errorf("%w: balance %s, debit of %s", ErrInsufficientFunds, before, amount)
		}
		after, err = before.Sub(amount)
	}
	if err != nil {
		return LedgerEntry{}, err
	}

	// The entry is built before any state changes, so a failure leaves the aggregate as it was.
	entry, err := NewLedgerEntry(entryID, w.id, transactionID, direction, amount, before, after, at)
	if err != nil {
		return LedgerEntry{}, err
	}

	w.balance = after
	w.version++
	w.updatedAt = at.UTC()
	return entry, nil
}

// WalletSnapshot is the persisted shape of a wallet, as a row of wallets.
type WalletSnapshot struct {
	ID           uuid.UUID `db:"id"`
	PlayerID     uuid.UUID `db:"player_id"`
	Currency     string    `db:"currency"`
	BalanceMinor int64     `db:"balance_minor"`
	Version      int64     `db:"version"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// Snapshot is what the repository writes.
func (w *Wallet) Snapshot() WalletSnapshot {
	return WalletSnapshot{
		ID:           w.id,
		PlayerID:     w.playerID,
		Currency:     string(w.currency),
		BalanceMinor: w.balance.Minor(),
		Version:      w.version,
		CreatedAt:    w.createdAt,
		UpdatedAt:    w.updatedAt,
	}
}

// RehydrateWallet restores a stored wallet. It applies no movement and emits no event: the
// balance is taken as stored, after checking that it is a wallet that could exist.
func RehydrateWallet(s WalletSnapshot) (*Wallet, error) {
	if s.ID == uuid.Nil || s.PlayerID == uuid.Nil {
		return nil, fmt.Errorf("%w: ids are required", ErrInvalidWallet)
	}
	balance, err := NewMoney(s.BalanceMinor, s.Currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidWallet, err)
	}
	if balance.IsNegative() {
		return nil, fmt.Errorf("%w: stored balance %s is negative", ErrInvalidWallet, balance)
	}
	if s.Version < 1 {
		return nil, fmt.Errorf("%w: version %d is below one", ErrInvalidWallet, s.Version)
	}
	return &Wallet{
		id:            s.ID,
		playerID:      s.PlayerID,
		currency:      balance.Currency(),
		balance:       balance,
		version:       s.Version,
		loadedVersion: s.Version,
		createdAt:     s.CreatedAt.UTC(),
		updatedAt:     s.UpdatedAt.UTC(),
	}, nil
}
