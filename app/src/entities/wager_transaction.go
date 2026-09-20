package entities

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// ErrOpeningNotAllowed is an external operation of kind OPENING. That kind belongs to the
// internal wallet opening, and nothing received over HTTP or SQS may claim it.
var ErrOpeningNotAllowed = errors.New("OPENING is reserved to the internal wallet opening")

// TransactionOrigin says who created a transaction: the system itself, or a provider.
type TransactionOrigin string

// Transaction origins.
const (
	OriginInternal TransactionOrigin = "INTERNAL"
	OriginExternal TransactionOrigin = "EXTERNAL"
)

// TransactionKind is what an operation does.
type TransactionKind string

// Transaction kinds. OPENING is the only internal one.
const (
	KindOpening  TransactionKind = "OPENING"
	KindBet      TransactionKind = "BET"
	KindWin      TransactionKind = "WIN"
	KindLoss     TransactionKind = "LOSS"
	KindRefund   TransactionKind = "REFUND"
	KindRollback TransactionKind = "ROLLBACK"
)

// ParseTransactionKind reads the kind of an external operation. The comparison is case
// insensitive and the result is upper case, which is the form the idempotency hash covers.
// OPENING is refused here, because it is not an external kind.
func ParseTransactionKind(raw string) (TransactionKind, error) {
	switch kind := TransactionKind(strings.ToUpper(raw)); kind {
	case KindBet, KindWin, KindLoss, KindRefund, KindRollback:
		return kind, nil
	case KindOpening:
		return "", ErrOpeningNotAllowed
	default:
		return "", fmt.Errorf("unknown kind %q", raw)
	}
}

// IsReversal reports whether the kind undoes another transaction and therefore has to name it.
func (k TransactionKind) IsReversal() bool {
	return k == KindRefund || k == KindRollback
}

// TransactionStatus is where a transaction is in its life.
type TransactionStatus string

// Transaction statuses. PROCESSED, REJECTED and FAILED are terminal.
const (
	StatusPending          TransactionStatus = "PENDING"
	StatusPendingReference TransactionStatus = "PENDING_REFERENCE"
	StatusProcessed        TransactionStatus = "PROCESSED"
	StatusRejected         TransactionStatus = "REJECTED"
	StatusFailed           TransactionStatus = "FAILED"
)

// IsTerminal reports a status no transition leaves.
func (s TransactionStatus) IsTerminal() bool {
	return s == StatusProcessed || s == StatusRejected || s == StatusFailed
}

func (s TransactionStatus) valid() bool {
	return s == StatusPending || s == StatusPendingReference || s.IsTerminal()
}

// allowedTransitions is the state machine. Anything not listed, including every move out of a
// terminal state, is refused.
var allowedTransitions = map[TransactionStatus][]TransactionStatus{
	StatusPending:          {StatusPendingReference, StatusProcessed, StatusRejected, StatusFailed},
	StatusPendingReference: {StatusProcessed, StatusRejected, StatusFailed},
}

// ExternalOperation is what a provider sends, before any of it is trusted. HTTP and SQS both
// build one and hand it to NewExternalTransaction, which is why the two entry points end up
// with the same validation and the same idempotency hash.
type ExternalOperation struct {
	ProviderID                     string
	ExternalTransactionID          string
	IdempotencyKey                 string
	PlayerID                       string
	WalletID                       string
	RoundID                        string
	GameID                         string
	Kind                           string
	Money                          Money
	ReferenceExternalTransactionID string
}

// ReferenceOutcome is what evaluating the reference of a reversal concluded.
type ReferenceOutcome int

// Reference outcomes.
const (
	// ReferenceReady means the reference is processed and compatible: the reversal can apply.
	ReferenceReady ReferenceOutcome = iota + 1
	// ReferenceWaiting means the reference exists but has not finished, so the reversal waits.
	ReferenceWaiting
)

// WagerTransaction is the record of one operation on a wallet, from the moment it is accepted
// to its terminal state. It owns its state machine: the only way to change its status is one of
// the Mark methods, and each of them refuses what the machine does not allow.
type WagerTransaction struct {
	id       uuid.UUID
	origin   TransactionOrigin
	kind     TransactionKind
	status   TransactionStatus
	walletID uuid.UUID
	playerID uuid.UUID
	money    Money

	providerID          string
	externalID          string
	idempotencyKey      string
	payloadHash         []byte
	roundID             string
	gameID              string
	referenceExternalID string
	referenceID         uuid.UUID

	failureCode         FailureCode
	resultBalance       Money
	resultWalletVersion int64

	referenceAttempts      int
	referenceNextAttemptAt time.Time
	referenceExpiresAt     time.Time

	createdAt time.Time
	updatedAt time.Time
	settledAt time.Time
}

// NewExternalTransaction accepts an operation from a provider as a PENDING transaction.
//
// It validates that the operation is well formed: the identifiers are present, the ids are
// UUIDs, the kind is external, the money is initialised and not negative, and a reversal names
// its reference. What it does not decide is whether a business rule accepts the operation. A
// bet of zero is well formed and is refused later, by CheckAmountPolicy, so that the refusal
// can be stored and replayed like any other outcome.
func NewExternalTransaction(id uuid.UUID, op ExternalOperation, at time.Time) (*WagerTransaction, error) {
	if id == uuid.Nil {
		return nil, fmt.Errorf("%w: id is required", ErrInvalidTransaction)
	}

	kind, err := ParseTransactionKind(op.Kind)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}
	for _, field := range []struct{ name, value string }{
		{"providerId", op.ProviderID},
		{"externalTransactionId", op.ExternalTransactionID},
		{"idempotencyKey", op.IdempotencyKey},
		{"roundId", op.RoundID},
		{"gameId", op.GameID},
	} {
		if strings.TrimSpace(field.value) == "" {
			return nil, fmt.Errorf("%w: %s is required", ErrInvalidTransaction, field.name)
		}
	}
	playerID, err := parseRequiredUUID("playerId", op.PlayerID)
	if err != nil {
		return nil, err
	}
	walletID, err := parseRequiredUUID("walletId", op.WalletID)
	if err != nil {
		return nil, err
	}
	if !op.Money.IsInitialized() {
		return nil, fmt.Errorf("%w: money is required", ErrInvalidTransaction)
	}
	if op.Money.IsNegative() {
		return nil, fmt.Errorf("%w: money cannot be negative", ErrInvalidTransaction)
	}

	reference := strings.TrimSpace(op.ReferenceExternalTransactionID)
	switch {
	case kind.IsReversal() && reference == "":
		return nil, fmt.Errorf("%w: referenceExternalTransactionId is required for %s", ErrInvalidTransaction, kind)
	case (kind == KindBet || kind == KindLoss) && reference != "":
		return nil, fmt.Errorf("%w: referenceExternalTransactionId does not apply to %s", ErrInvalidTransaction, kind)
	}

	hash, err := OperationPayload{
		ExternalTransactionID:          op.ExternalTransactionID,
		GameID:                         op.GameID,
		Kind:                           kind,
		Money:                          op.Money,
		PlayerID:                       playerID.String(),
		ProviderID:                     op.ProviderID,
		ReferenceExternalTransactionID: reference,
		RoundID:                        op.RoundID,
		WalletID:                       walletID.String(),
	}.Hash()
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}

	now := at.UTC()
	return &WagerTransaction{
		id:                  id,
		origin:              OriginExternal,
		kind:                kind,
		status:              StatusPending,
		walletID:            walletID,
		playerID:            playerID,
		money:               op.Money,
		providerID:          op.ProviderID,
		externalID:          op.ExternalTransactionID,
		idempotencyKey:      op.IdempotencyKey,
		payloadHash:         hash,
		roundID:             op.RoundID,
		gameID:              op.GameID,
		referenceExternalID: reference,
		createdAt:           now,
		updatedAt:           now,
	}, nil
}

// NewOpeningTransaction creates the internal transaction that credits a wallet's initial
// balance. It carries none of the external metadata, because none of it applies: no provider,
// no external id, no key, no hash, no round, no game and no reference.
func NewOpeningTransaction(id, walletID, playerID uuid.UUID, money Money, at time.Time) (*WagerTransaction, error) {
	if id == uuid.Nil || walletID == uuid.Nil || playerID == uuid.Nil {
		return nil, fmt.Errorf("%w: ids are required", ErrInvalidTransaction)
	}
	if !money.IsPositive() {
		return nil, fmt.Errorf("%w: an opening credits a positive amount, got %s", ErrInvalidTransaction, money)
	}
	now := at.UTC()
	return &WagerTransaction{
		id:        id,
		origin:    OriginInternal,
		kind:      KindOpening,
		status:    StatusPending,
		walletID:  walletID,
		playerID:  playerID,
		money:     money,
		createdAt: now,
		updatedAt: now,
	}, nil
}

func parseRequiredUUID(field, raw string) (uuid.UUID, error) {
	parsed, err := uuid.Parse(raw)
	if err != nil || parsed == uuid.Nil {
		return uuid.Nil, fmt.Errorf("%w: %s must be a UUID", ErrInvalidTransaction, field)
	}
	return parsed, nil
}

// CheckAmountPolicy applies the value rule of the kind: LOSS moves nothing and has to say so
// with zero, and every other kind has to move something. A violation is a Rejection, so it can
// be stored with its code.
func (t *WagerTransaction) CheckAmountPolicy() error {
	if t.kind == KindLoss {
		if !t.money.IsZero() {
			return Reject(FailureInvalidAmount, "LOSS requires an amount of 0.00, got %s", t.money)
		}
		return nil
	}
	if !t.money.IsPositive() {
		return Reject(FailureInvalidAmount, "%s requires an amount above zero, got %s", t.kind, t.money)
	}
	return nil
}

// MovesBalance reports whether the kind changes the wallet balance. LOSS does not.
func (t *WagerTransaction) MovesBalance() bool {
	return t.kind != KindLoss
}

// Movement is the direction the transaction moves the balance. A ROLLBACK depends on what it
// undoes, so it needs its reference: undoing a BET credits, undoing a WIN or a REFUND debits.
func (t *WagerTransaction) Movement(reference *WagerTransaction) (Direction, error) {
	switch t.kind {
	case KindOpening, KindWin, KindRefund:
		return DirectionCredit, nil
	case KindBet:
		return DirectionDebit, nil
	case KindRollback:
		if reference == nil {
			return "", fmt.Errorf("%w: a ROLLBACK needs its reference to know its direction", ErrInvalidTransaction)
		}
		switch reference.kind {
		case KindBet:
			return DirectionCredit, nil
		case KindWin, KindRefund:
			return DirectionDebit, nil
		default:
			return "", Reject(FailureReferenceMismatch, "a ROLLBACK cannot undo a %s", reference.kind)
		}
	default:
		return "", fmt.Errorf("%w: %s moves no balance", ErrInvalidTransaction, t.kind)
	}
}

// EvaluateReference decides whether this reversal can apply against the transaction it names.
//
// Whatever is already knowable is checked first, whatever the state of the reference: the kind
// it may undo, provider, player, wallet, currency, round and amount. Only then does the state
// decide between applying (processed), waiting (still pending) and refusing (rejected or
// failed, which will never become something to undo).
func (t *WagerTransaction) EvaluateReference(reference *WagerTransaction) (ReferenceOutcome, error) {
	if !t.kind.IsReversal() {
		return 0, fmt.Errorf("%w: %s has no reference to evaluate", ErrInvalidTransaction, t.kind)
	}
	if reference == nil {
		return 0, fmt.Errorf("%w: a reference is required", ErrInvalidTransaction)
	}

	if !t.canUndo(reference.kind) {
		return 0, Reject(FailureReferenceMismatch, "a %s cannot undo a %s", t.kind, reference.kind)
	}
	for _, check := range []struct {
		name string
		same bool
	}{
		{"provider", t.providerID == reference.providerID},
		{"player", t.playerID == reference.playerID},
		{"wallet", t.walletID == reference.walletID},
		{"currency", t.money.Currency() == reference.money.Currency()},
		{"round", t.roundID == reference.roundID},
	} {
		if !check.same {
			return 0, Reject(FailureReferenceMismatch, "the %s differs from the referenced transaction", check.name)
		}
	}
	if equal, err := t.money.Equal(reference.money); err != nil {
		return 0, err
	} else if !equal {
		return 0, Reject(FailureAmountMismatch, "%s of %s does not match the referenced %s of %s",
			t.kind, t.money, reference.kind, reference.money)
	}

	switch reference.status {
	case StatusProcessed:
		return ReferenceReady, nil
	case StatusPending, StatusPendingReference:
		return ReferenceWaiting, nil
	default:
		return 0, Reject(FailureReferenceNotProcessed, "the referenced transaction ended %s", reference.status)
	}
}

func (t *WagerTransaction) canUndo(kind TransactionKind) bool {
	switch t.kind {
	case KindRefund:
		return kind == KindBet
	case KindRollback:
		return kind == KindBet || kind == KindWin || kind == KindRefund
	default:
		return false
	}
}

// LinkReference records which stored transaction the external reference resolved to.
func (t *WagerTransaction) LinkReference(id uuid.UUID, at time.Time) error {
	if t.status.IsTerminal() {
		return fmt.Errorf("%w: %s is terminal", ErrInvalidTransition, t.status)
	}
	if id == uuid.Nil {
		return fmt.Errorf("%w: the resolved reference id is required", ErrInvalidTransaction)
	}
	t.referenceID = id
	t.updatedAt = at.UTC()
	return nil
}

// MarkProcessed concludes the operation successfully. It stores what the provider is told
// about the outcome, which is the balance as it was when this operation applied: a later
// replay answers with that, not with whatever the balance has become.
func (t *WagerTransaction) MarkProcessed(resultBalance Money, walletVersion int64, at time.Time) error {
	if !resultBalance.IsInitialized() || resultBalance.IsNegative() {
		return fmt.Errorf("%w: the result balance must be initialised and not negative", ErrInvalidTransaction)
	}
	if resultBalance.Currency() != t.money.Currency() {
		return fmt.Errorf("%w: the result balance is in %s and the transaction in %s",
			ErrInvalidTransaction, resultBalance.Currency(), t.money.Currency())
	}
	if walletVersion < 1 {
		return fmt.Errorf("%w: the wallet version must be at least one", ErrInvalidTransaction)
	}
	if err := t.transition(StatusProcessed, at); err != nil {
		return err
	}
	t.resultBalance = resultBalance
	t.resultWalletVersion = walletVersion
	t.settledAt = at.UTC()
	return nil
}

// MarkRejected concludes the operation as refused by a business rule.
func (t *WagerTransaction) MarkRejected(code FailureCode, at time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: a rejection needs a failure code", ErrInvalidTransaction)
	}
	if err := t.transition(StatusRejected, at); err != nil {
		return err
	}
	t.failureCode = code
	t.settledAt = at.UTC()
	return nil
}

// MarkFailed concludes the operation as a permanent infrastructure failure, kept for audit.
func (t *WagerTransaction) MarkFailed(code FailureCode, at time.Time) error {
	if code == "" {
		return fmt.Errorf("%w: a failure needs a failure code", ErrInvalidTransaction)
	}
	if err := t.transition(StatusFailed, at); err != nil {
		return err
	}
	t.failureCode = code
	t.settledAt = at.UTC()
	return nil
}

// MarkPendingReference parks a reversal whose reference has not arrived. The next attempt and
// the expiry are stored on the transaction itself, so a restart or another instance picks the
// wait up exactly where it was.
func (t *WagerTransaction) MarkPendingReference(nextAttemptAt, expiresAt, at time.Time) error {
	if !t.kind.IsReversal() {
		return fmt.Errorf("%w: only a reversal waits for a reference, and this is a %s", ErrInvalidTransaction, t.kind)
	}
	if err := t.transition(StatusPendingReference, at); err != nil {
		return err
	}
	t.referenceAttempts = 0
	t.referenceNextAttemptAt = nextAttemptAt.UTC()
	t.referenceExpiresAt = expiresAt.UTC()
	return nil
}

// RetryReference records one more failed attempt to resolve the reference and schedules the
// next. It is the only change a PENDING_REFERENCE transaction can undergo without leaving the
// state.
func (t *WagerTransaction) RetryReference(nextAttemptAt, at time.Time) error {
	if t.status != StatusPendingReference {
		return fmt.Errorf("%w: only a PENDING_REFERENCE transaction retries, this one is %s", ErrInvalidTransition, t.status)
	}
	t.referenceAttempts++
	t.referenceNextAttemptAt = nextAttemptAt.UTC()
	t.updatedAt = at.UTC()
	return nil
}

// ReferenceExhausted reports whether the wait is over: the attempts reached the limit or the
// expiry passed, whichever comes first.
func (t *WagerTransaction) ReferenceExhausted(now time.Time, maxAttempts int) bool {
	if t.referenceAttempts >= maxAttempts {
		return true
	}
	return !t.referenceExpiresAt.IsZero() && !now.Before(t.referenceExpiresAt)
}

func (t *WagerTransaction) transition(to TransactionStatus, at time.Time) error {
	for _, allowed := range allowedTransitions[t.status] {
		if allowed == to {
			t.status = to
			t.updatedAt = at.UTC()
			return nil
		}
	}
	return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, t.status, to)
}

// MatchesPayload reports whether the given hash is the one this transaction was accepted with.
// It is how an idempotent replay tells the same request from a different one under the same
// key.
func (t *WagerTransaction) MatchesPayload(hash []byte) bool {
	return bytes.Equal(t.payloadHash, hash)
}

// ID is the transaction identity.
func (t *WagerTransaction) ID() uuid.UUID { return t.id }

// Origin says whether the system or a provider created it.
func (t *WagerTransaction) Origin() TransactionOrigin { return t.origin }

// Kind is what the operation does.
func (t *WagerTransaction) Kind() TransactionKind { return t.kind }

// Status is where the transaction is in its life.
func (t *WagerTransaction) Status() TransactionStatus { return t.status }

// WalletID is the wallet it operates on.
func (t *WagerTransaction) WalletID() uuid.UUID { return t.walletID }

// PlayerID is the player it belongs to.
func (t *WagerTransaction) PlayerID() uuid.UUID { return t.playerID }

// Money is the amount of the operation.
func (t *WagerTransaction) Money() Money { return t.money }

// ProviderID is the provider, empty for an internal transaction.
func (t *WagerTransaction) ProviderID() string { return t.providerID }

// ExternalTransactionID is the provider's id for the operation, empty for an internal one.
func (t *WagerTransaction) ExternalTransactionID() string { return t.externalID }

// IdempotencyKey is the key the provider sent, exactly as it was sent.
func (t *WagerTransaction) IdempotencyKey() string { return t.idempotencyKey }

// PayloadHash is the hash of the canonical business payload.
func (t *WagerTransaction) PayloadHash() []byte { return append([]byte(nil), t.payloadHash...) }

// RoundID is the game round, empty for an internal transaction.
func (t *WagerTransaction) RoundID() string { return t.roundID }

// GameID is the game, empty for an internal transaction.
func (t *WagerTransaction) GameID() string { return t.gameID }

// ReferenceExternalTransactionID is the provider id of the transaction a reversal undoes.
func (t *WagerTransaction) ReferenceExternalTransactionID() string { return t.referenceExternalID }

// ReferenceTransactionID is the stored transaction the reference resolved to, or uuid.Nil.
func (t *WagerTransaction) ReferenceTransactionID() uuid.UUID { return t.referenceID }

// FailureCode is why the transaction ended REJECTED or FAILED, empty otherwise.
func (t *WagerTransaction) FailureCode() FailureCode { return t.failureCode }

// ResultBalance is the balance observed when the operation applied. It is the zero Money until
// the transaction is PROCESSED.
func (t *WagerTransaction) ResultBalance() Money { return t.resultBalance }

// ResultWalletVersion is the wallet version observed when the operation applied.
func (t *WagerTransaction) ResultWalletVersion() int64 { return t.resultWalletVersion }

// ReferenceAttempts is how many times resolving the reference has been retried.
func (t *WagerTransaction) ReferenceAttempts() int { return t.referenceAttempts }

// ReferenceNextAttemptAt is when the next retry is due, the zero time when none is scheduled.
func (t *WagerTransaction) ReferenceNextAttemptAt() time.Time { return t.referenceNextAttemptAt }

// ReferenceExpiresAt is when the wait for the reference ends, the zero time when it is not
// waiting.
func (t *WagerTransaction) ReferenceExpiresAt() time.Time { return t.referenceExpiresAt }

// CreatedAt is when the operation was accepted, in UTC.
func (t *WagerTransaction) CreatedAt() time.Time { return t.createdAt }

// UpdatedAt is when it last changed, in UTC.
func (t *WagerTransaction) UpdatedAt() time.Time { return t.updatedAt }

// SettledAt is when it reached a terminal state, the zero time before that.
func (t *WagerTransaction) SettledAt() time.Time { return t.settledAt }

// WagerTransactionSnapshot is the persisted shape of a transaction, as a row of
// wager_transactions. The columns that do not apply to an internal transaction are pointers, so
// they are stored as NULL.
type WagerTransactionSnapshot struct {
	ID                             uuid.UUID  `db:"id"`
	Origin                         string     `db:"origin"`
	Kind                           string     `db:"kind"`
	Status                         string     `db:"status"`
	WalletID                       uuid.UUID  `db:"wallet_id"`
	PlayerID                       uuid.UUID  `db:"player_id"`
	AmountMinor                    int64      `db:"amount_minor"`
	Currency                       string     `db:"currency"`
	ProviderID                     *string    `db:"provider_id"`
	ExternalTransactionID          *string    `db:"external_transaction_id"`
	IdempotencyKey                 *string    `db:"idempotency_key"`
	PayloadHash                    []byte     `db:"payload_hash"`
	RoundID                        *string    `db:"round_id"`
	GameID                         *string    `db:"game_id"`
	ReferenceExternalTransactionID *string    `db:"reference_external_transaction_id"`
	ReferenceTransactionID         *uuid.UUID `db:"reference_transaction_id"`
	FailureCode                    *string    `db:"failure_code"`
	ResultBalanceMinor             *int64     `db:"result_balance_minor"`
	ResultWalletVersion            *int64     `db:"result_wallet_version"`
	ReferenceAttempts              int        `db:"reference_attempts"`
	ReferenceNextAttemptAt         *time.Time `db:"reference_next_attempt_at"`
	ReferenceExpiresAt             *time.Time `db:"reference_expires_at"`
	CreatedAt                      time.Time  `db:"created_at"`
	UpdatedAt                      time.Time  `db:"updated_at"`
	SettledAt                      *time.Time `db:"settled_at"`
}

// Snapshot is what the repository writes.
func (t *WagerTransaction) Snapshot() WagerTransactionSnapshot {
	snapshot := WagerTransactionSnapshot{
		ID:                             t.id,
		Origin:                         string(t.origin),
		Kind:                           string(t.kind),
		Status:                         string(t.status),
		WalletID:                       t.walletID,
		PlayerID:                       t.playerID,
		AmountMinor:                    t.money.Minor(),
		Currency:                       string(t.money.Currency()),
		ProviderID:                     textPointer(t.providerID),
		ExternalTransactionID:          textPointer(t.externalID),
		IdempotencyKey:                 textPointer(t.idempotencyKey),
		RoundID:                        textPointer(t.roundID),
		GameID:                         textPointer(t.gameID),
		ReferenceExternalTransactionID: textPointer(t.referenceExternalID),
		FailureCode:                    textPointer(string(t.failureCode)),
		ReferenceAttempts:              t.referenceAttempts,
		ReferenceNextAttemptAt:         timePointer(t.referenceNextAttemptAt),
		ReferenceExpiresAt:             timePointer(t.referenceExpiresAt),
		CreatedAt:                      t.createdAt,
		UpdatedAt:                      t.updatedAt,
		SettledAt:                      timePointer(t.settledAt),
	}
	if len(t.payloadHash) > 0 {
		snapshot.PayloadHash = append([]byte(nil), t.payloadHash...)
	}
	if t.referenceID != uuid.Nil {
		id := t.referenceID
		snapshot.ReferenceTransactionID = &id
	}
	if t.resultBalance.IsInitialized() {
		minor, version := t.resultBalance.Minor(), t.resultWalletVersion
		snapshot.ResultBalanceMinor = &minor
		snapshot.ResultWalletVersion = &version
	}
	return snapshot
}

// RehydrateWagerTransaction restores a stored transaction. It replays no transition and emits
// no event: the status is taken as stored, after checking that it is one the machine knows.
func RehydrateWagerTransaction(s WagerTransactionSnapshot) (*WagerTransaction, error) {
	origin, kind, status := TransactionOrigin(s.Origin), TransactionKind(s.Kind), TransactionStatus(s.Status)
	if origin != OriginInternal && origin != OriginExternal {
		return nil, fmt.Errorf("%w: unknown origin %q", ErrInvalidTransaction, s.Origin)
	}
	switch kind {
	case KindOpening, KindBet, KindWin, KindLoss, KindRefund, KindRollback:
	default:
		return nil, fmt.Errorf("%w: unknown kind %q", ErrInvalidTransaction, s.Kind)
	}
	if !status.valid() {
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalidTransaction, s.Status)
	}
	if (kind == KindOpening) != (origin == OriginInternal) {
		return nil, fmt.Errorf("%w: a %s transaction cannot have origin %s", ErrInvalidTransaction, kind, origin)
	}
	money, err := NewMoney(s.AmountMinor, s.Currency)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
	}

	t := &WagerTransaction{
		id:                  s.ID,
		origin:              origin,
		kind:                kind,
		status:              status,
		walletID:            s.WalletID,
		playerID:            s.PlayerID,
		money:               money,
		providerID:          textValue(s.ProviderID),
		externalID:          textValue(s.ExternalTransactionID),
		idempotencyKey:      textValue(s.IdempotencyKey),
		payloadHash:         append([]byte(nil), s.PayloadHash...),
		roundID:             textValue(s.RoundID),
		gameID:              textValue(s.GameID),
		referenceExternalID: textValue(s.ReferenceExternalTransactionID),
		failureCode:         FailureCode(textValue(s.FailureCode)),
		referenceAttempts:   s.ReferenceAttempts,
		createdAt:           s.CreatedAt.UTC(),
		updatedAt:           s.UpdatedAt.UTC(),
	}
	if s.ReferenceTransactionID != nil {
		t.referenceID = *s.ReferenceTransactionID
	}
	if s.ResultBalanceMinor != nil {
		result, err := NewMoney(*s.ResultBalanceMinor, s.Currency)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidTransaction, err)
		}
		t.resultBalance = result
		if s.ResultWalletVersion != nil {
			t.resultWalletVersion = *s.ResultWalletVersion
		}
	}
	t.referenceNextAttemptAt = timeValue(s.ReferenceNextAttemptAt)
	t.referenceExpiresAt = timeValue(s.ReferenceExpiresAt)
	t.settledAt = timeValue(s.SettledAt)
	return t, nil
}

func textPointer(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func textValue(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func timePointer(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	return &t
}

func timeValue(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return t.UTC()
}
