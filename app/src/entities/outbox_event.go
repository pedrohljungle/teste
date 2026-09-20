package entities

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
)

// EventType names an integration event.
type EventType string

// Integration events. The type and the version are fixed by the constructor of each event, so a
// caller cannot publish one under a name that disagrees with its data.
const (
	EventWagerTransactionProcessed        EventType = "WagerTransactionProcessed"
	EventWagerTransactionRejected         EventType = "WagerTransactionRejected"
	EventWalletBalanceChanged             EventType = "WalletBalanceChanged"
	EventWagerTransactionPendingReference EventType = "WagerTransactionPendingReference"
)

// Aggregate types an event can belong to.
const (
	AggregateWagerTransaction = "WagerTransaction"
	AggregateWallet           = "Wallet"
)

// eventVersion is the schema version of every event today. A change that a consumer could not
// read with the old shape bumps the version of that event alone.
const eventVersion = 1

// occurredAtLayout is RFC 3339 in UTC with milliseconds, the timestamp format of the contract.
const occurredAtLayout = "2006-01-02T15:04:05.000Z"

// EventEnvelope is the contract every published event shares, with its data typed per event.
type EventEnvelope[T any] struct {
	EventID       uuid.UUID `json:"eventId"`
	EventType     EventType `json:"eventType"`
	AggregateID   uuid.UUID `json:"aggregateId"`
	CorrelationID string    `json:"correlationId"`
	CausationID   string    `json:"causationId,omitempty"`
	OccurredAt    string    `json:"occurredAt"`
	Version       int       `json:"version"`
	Data          T         `json:"data"`
}

// TransactionFacts are the fields every transaction event reports about its transaction. The
// provider metadata is omitted for an internal transaction, where none of it applies.
type TransactionFacts struct {
	TransactionID         uuid.UUID       `json:"transactionId"`
	Kind                  TransactionKind `json:"kind"`
	WalletID              uuid.UUID       `json:"walletId"`
	PlayerID              uuid.UUID       `json:"playerId"`
	Money                 Money           `json:"money"`
	ProviderID            string          `json:"providerId,omitempty"`
	ExternalTransactionID string          `json:"externalTransactionId,omitempty"`
	RoundID               string          `json:"roundId,omitempty"`
	GameID                string          `json:"gameId,omitempty"`
}

// WagerTransactionProcessedData is the data of WagerTransactionProcessed.
type WagerTransactionProcessedData struct {
	TransactionFacts
}

// WagerTransactionRejectedData is the data of WagerTransactionRejected.
type WagerTransactionRejectedData struct {
	TransactionFacts
	FailureCode FailureCode `json:"failureCode"`
}

// WagerTransactionPendingReferenceData is the data of WagerTransactionPendingReference.
type WagerTransactionPendingReferenceData struct {
	TransactionFacts
	ReferenceExternalTransactionID string `json:"referenceExternalTransactionId"`
	ExpiresAt                      string `json:"expiresAt"`
}

// WalletBalanceChangedData is the data of WalletBalanceChanged.
type WalletBalanceChangedData struct {
	WalletID      uuid.UUID `json:"walletId"`
	TransactionID uuid.UUID `json:"transactionId"`
	Direction     Direction `json:"direction"`
	Money         Money     `json:"money"`
	BalanceBefore Money     `json:"balanceBefore"`
	BalanceAfter  Money     `json:"balanceAfter"`
	WalletVersion int64     `json:"walletVersion"`
}

// OutboxStatus is where an outbox record is in its publication.
type OutboxStatus string

// Outbox statuses.
const (
	OutboxPending   OutboxStatus = "PENDING"
	OutboxPublished OutboxStatus = "PUBLISHED"
	OutboxFailed    OutboxStatus = "FAILED"
)

// OutboxEvent is an integration event stored in the same commit as the change that caused it,
// waiting to be published. Its payload is the complete envelope, serialised once at
// construction: a snapshot that no later change to the wallet or the transaction can alter.
//
// The event id is stable. A republication after a crash between publishing and confirming
// carries the same id, which is what lets a consumer recognise it.
type OutboxEvent struct {
	id            uuid.UUID
	aggregateType string
	aggregateID   uuid.UUID
	eventType     EventType
	version       int
	correlationID string
	causationID   string
	payload       []byte
	occurredAt    time.Time
	status        OutboxStatus
	attempts      int
	nextAttemptAt time.Time
	lockedBy      string
	lockedAt      time.Time
	publishedAt   time.Time
}

// NewWagerTransactionProcessedEvent builds the event for a transaction that reached PROCESSED,
// including a LOSS, which is a fact even though it moves no balance.
func NewWagerTransactionProcessedEvent(eventID uuid.UUID, t *WagerTransaction, correlationID, causationID string, at time.Time) (*OutboxEvent, error) {
	if t.status != StatusProcessed {
		return nil, fmt.Errorf("%w: %s needs a PROCESSED transaction, this one is %s",
			ErrInvalidEvent, EventWagerTransactionProcessed, t.status)
	}
	return newOutboxEvent(eventID, AggregateWagerTransaction, t.id, EventWagerTransactionProcessed,
		correlationID, causationID, at, WagerTransactionProcessedData{TransactionFacts: factsOf(t)})
}

// NewWagerTransactionRejectedEvent builds the event for a transaction a business rule refused.
func NewWagerTransactionRejectedEvent(eventID uuid.UUID, t *WagerTransaction, correlationID, causationID string, at time.Time) (*OutboxEvent, error) {
	if t.status != StatusRejected {
		return nil, fmt.Errorf("%w: %s needs a REJECTED transaction, this one is %s",
			ErrInvalidEvent, EventWagerTransactionRejected, t.status)
	}
	return newOutboxEvent(eventID, AggregateWagerTransaction, t.id, EventWagerTransactionRejected,
		correlationID, causationID, at, WagerTransactionRejectedData{TransactionFacts: factsOf(t), FailureCode: t.failureCode})
}

// NewWagerTransactionPendingReferenceEvent builds the event for a reversal that is waiting for
// its reference.
func NewWagerTransactionPendingReferenceEvent(eventID uuid.UUID, t *WagerTransaction, correlationID, causationID string, at time.Time) (*OutboxEvent, error) {
	if t.status != StatusPendingReference {
		return nil, fmt.Errorf("%w: %s needs a PENDING_REFERENCE transaction, this one is %s",
			ErrInvalidEvent, EventWagerTransactionPendingReference, t.status)
	}
	return newOutboxEvent(eventID, AggregateWagerTransaction, t.id, EventWagerTransactionPendingReference,
		correlationID, causationID, at, WagerTransactionPendingReferenceData{
			TransactionFacts:               factsOf(t),
			ReferenceExternalTransactionID: t.referenceExternalID,
			ExpiresAt:                      t.referenceExpiresAt.UTC().Format(occurredAtLayout),
		})
}

// NewWalletBalanceChangedEvent builds the event for a balance that actually changed, from the
// ledger entry that recorded the change and the wallet version that resulted.
func NewWalletBalanceChangedEvent(eventID uuid.UUID, entry LedgerEntry, walletVersion int64, correlationID, causationID string, at time.Time) (*OutboxEvent, error) {
	if entry.id == uuid.Nil {
		return nil, fmt.Errorf("%w: %s needs a ledger entry", ErrInvalidEvent, EventWalletBalanceChanged)
	}
	if walletVersion < 1 {
		return nil, fmt.Errorf("%w: the wallet version must be at least one", ErrInvalidEvent)
	}
	return newOutboxEvent(eventID, AggregateWallet, entry.walletID, EventWalletBalanceChanged,
		correlationID, causationID, at, WalletBalanceChangedData{
			WalletID:      entry.walletID,
			TransactionID: entry.transactionID,
			Direction:     entry.direction,
			Money:         entry.money,
			BalanceBefore: entry.before,
			BalanceAfter:  entry.after,
			WalletVersion: walletVersion,
		})
}

func factsOf(t *WagerTransaction) TransactionFacts {
	return TransactionFacts{
		TransactionID:         t.id,
		Kind:                  t.kind,
		WalletID:              t.walletID,
		PlayerID:              t.playerID,
		Money:                 t.money,
		ProviderID:            t.providerID,
		ExternalTransactionID: t.externalID,
		RoundID:               t.roundID,
		GameID:                t.gameID,
	}
}

func newOutboxEvent[T any](eventID uuid.UUID, aggregateType string, aggregateID uuid.UUID, eventType EventType,
	correlationID, causationID string, at time.Time, data T) (*OutboxEvent, error) {
	if eventID == uuid.Nil || aggregateID == uuid.Nil {
		return nil, fmt.Errorf("%w: event and aggregate ids are required", ErrInvalidEvent)
	}
	if strings.TrimSpace(correlationID) == "" {
		return nil, fmt.Errorf("%w: a correlation id is required", ErrInvalidEvent)
	}

	occurred := at.UTC()
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(EventEnvelope[T]{
		EventID:       eventID,
		EventType:     eventType,
		AggregateID:   aggregateID,
		CorrelationID: correlationID,
		CausationID:   causationID,
		OccurredAt:    occurred.Format(occurredAtLayout),
		Version:       eventVersion,
		Data:          data,
	}); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidEvent, err)
	}

	return &OutboxEvent{
		id:            eventID,
		aggregateType: aggregateType,
		aggregateID:   aggregateID,
		eventType:     eventType,
		version:       eventVersion,
		correlationID: correlationID,
		causationID:   causationID,
		payload:       bytes.TrimRight(buffer.Bytes(), "\n"),
		occurredAt:    occurred,
		status:        OutboxPending,
		nextAttemptAt: occurred,
	}, nil
}

// ID is the stable event id.
func (e *OutboxEvent) ID() uuid.UUID { return e.id }

// AggregateType is the kind of aggregate the event is about.
func (e *OutboxEvent) AggregateType() string { return e.aggregateType }

// AggregateID is the aggregate the event is about.
func (e *OutboxEvent) AggregateID() uuid.UUID { return e.aggregateID }

// Type is the event type.
func (e *OutboxEvent) Type() EventType { return e.eventType }

// Version is the schema version of the event.
func (e *OutboxEvent) Version() int { return e.version }

// CorrelationID ties the event to the request or message that started the chain.
func (e *OutboxEvent) CorrelationID() string { return e.correlationID }

// CausationID is what directly caused the event, empty when not applicable.
func (e *OutboxEvent) CausationID() string { return e.causationID }

// Payload is the serialised envelope, exactly as it is published.
func (e *OutboxEvent) Payload() []byte { return append([]byte(nil), e.payload...) }

// OccurredAt is when the event happened, in UTC.
func (e *OutboxEvent) OccurredAt() time.Time { return e.occurredAt }

// Status is where the event is in its publication.
func (e *OutboxEvent) Status() OutboxStatus { return e.status }

// Attempts is how many times publishing it was attempted.
func (e *OutboxEvent) Attempts() int { return e.attempts }

// NextAttemptAt is when it is next due for publication.
func (e *OutboxEvent) NextAttemptAt() time.Time { return e.nextAttemptAt }

// OutboxEventSnapshot is the persisted shape of an event, as a row of outbox_events.
type OutboxEventSnapshot struct {
	EventID       uuid.UUID  `db:"event_id"`
	AggregateType string     `db:"aggregate_type"`
	AggregateID   uuid.UUID  `db:"aggregate_id"`
	EventType     string     `db:"event_type"`
	EventVersion  int        `db:"event_version"`
	CorrelationID string     `db:"correlation_id"`
	CausationID   *string    `db:"causation_id"`
	Payload       []byte     `db:"payload"`
	OccurredAt    time.Time  `db:"occurred_at"`
	Status        string     `db:"status"`
	Attempts      int        `db:"attempts"`
	NextAttemptAt time.Time  `db:"next_attempt_at"`
	LockedBy      *string    `db:"locked_by"`
	LockedAt      *time.Time `db:"locked_at"`
	PublishedAt   *time.Time `db:"published_at"`
}

// Snapshot is what the repository writes.
func (e *OutboxEvent) Snapshot() OutboxEventSnapshot {
	return OutboxEventSnapshot{
		EventID:       e.id,
		AggregateType: e.aggregateType,
		AggregateID:   e.aggregateID,
		EventType:     string(e.eventType),
		EventVersion:  e.version,
		CorrelationID: e.correlationID,
		CausationID:   textPointer(e.causationID),
		Payload:       append([]byte(nil), e.payload...),
		OccurredAt:    e.occurredAt,
		Status:        string(e.status),
		Attempts:      e.attempts,
		NextAttemptAt: e.nextAttemptAt,
		LockedBy:      textPointer(e.lockedBy),
		LockedAt:      timePointer(e.lockedAt),
		PublishedAt:   timePointer(e.publishedAt),
	}
}

// RehydrateOutboxEvent restores a stored event without changing it: the payload is the
// snapshot that was written and stays as it is.
func RehydrateOutboxEvent(s OutboxEventSnapshot) (*OutboxEvent, error) {
	status := OutboxStatus(s.Status)
	switch status {
	case OutboxPending, OutboxPublished, OutboxFailed:
	default:
		return nil, fmt.Errorf("%w: unknown status %q", ErrInvalidEvent, s.Status)
	}
	if s.EventID == uuid.Nil || s.AggregateID == uuid.Nil {
		return nil, fmt.Errorf("%w: event and aggregate ids are required", ErrInvalidEvent)
	}
	return &OutboxEvent{
		id:            s.EventID,
		aggregateType: s.AggregateType,
		aggregateID:   s.AggregateID,
		eventType:     EventType(s.EventType),
		version:       s.EventVersion,
		correlationID: s.CorrelationID,
		causationID:   textValue(s.CausationID),
		payload:       append([]byte(nil), s.Payload...),
		occurredAt:    s.OccurredAt.UTC(),
		status:        status,
		attempts:      s.Attempts,
		nextAttemptAt: s.NextAttemptAt.UTC(),
		lockedBy:      textValue(s.LockedBy),
		lockedAt:      timeValue(s.LockedAt),
		publishedAt:   timeValue(s.PublishedAt),
	}, nil
}
