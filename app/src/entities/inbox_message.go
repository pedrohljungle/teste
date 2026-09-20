package entities

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// InboxMessage is the durable record that a consumer received a message: who received it,
// which message, the hash of its content, and when its handling was completed.
//
// The pair (consumer, message id) is unique in the database, which is what turns a redelivery
// into a duplicate instead of a second handling. The hash is what tells a redelivery apart from
// a different message that reused an id.
type InboxMessage struct {
	consumerName string
	messageID    string
	payloadHash  []byte
	receivedAt   time.Time
	completedAt  time.Time
}

// NewInboxMessage records a message as received and not yet completed.
func NewInboxMessage(consumerName, messageID string, payloadHash []byte, at time.Time) (*InboxMessage, error) {
	if strings.TrimSpace(consumerName) == "" || strings.TrimSpace(messageID) == "" {
		return nil, fmt.Errorf("%w: consumer name and message id are required", ErrInvalidInboxMessage)
	}
	if len(payloadHash) == 0 {
		return nil, fmt.Errorf("%w: the payload hash is required", ErrInvalidInboxMessage)
	}
	return &InboxMessage{
		consumerName: consumerName,
		messageID:    messageID,
		payloadHash:  append([]byte(nil), payloadHash...),
		receivedAt:   at.UTC(),
	}, nil
}

// Complete marks the handling as durably done. A message completes once.
func (m *InboxMessage) Complete(at time.Time) error {
	if m.IsCompleted() {
		return fmt.Errorf("%w: message %s was already completed", ErrInvalidTransition, m.messageID)
	}
	m.completedAt = at.UTC()
	return nil
}

// IsCompleted reports whether the handling finished.
func (m *InboxMessage) IsCompleted() bool { return !m.completedAt.IsZero() }

// MatchesPayload reports whether the hash is the one the message was first received with.
func (m *InboxMessage) MatchesPayload(hash []byte) bool {
	return bytes.Equal(m.payloadHash, hash)
}

// ConsumerName is the consumer that received the message.
func (m *InboxMessage) ConsumerName() string { return m.consumerName }

// MessageID is the durable identity of the message, taken from its envelope.
func (m *InboxMessage) MessageID() string { return m.messageID }

// ReceivedAt is when the message was first received, in UTC.
func (m *InboxMessage) ReceivedAt() time.Time { return m.receivedAt }

// CompletedAt is when the handling finished, the zero time before that.
func (m *InboxMessage) CompletedAt() time.Time { return m.completedAt }

// InboxMessageSnapshot is the persisted shape of an inbox record, as a row of inbox_messages.
type InboxMessageSnapshot struct {
	ConsumerName string     `db:"consumer_name"`
	MessageID    string     `db:"message_id"`
	PayloadHash  []byte     `db:"payload_hash"`
	ReceivedAt   time.Time  `db:"received_at"`
	CompletedAt  *time.Time `db:"completed_at"`
}

// Snapshot is what the repository writes.
func (m *InboxMessage) Snapshot() InboxMessageSnapshot {
	return InboxMessageSnapshot{
		ConsumerName: m.consumerName,
		MessageID:    m.messageID,
		PayloadHash:  append([]byte(nil), m.payloadHash...),
		ReceivedAt:   m.receivedAt,
		CompletedAt:  timePointer(m.completedAt),
	}
}

// RehydrateInboxMessage restores a stored record.
func RehydrateInboxMessage(s InboxMessageSnapshot) (*InboxMessage, error) {
	message, err := NewInboxMessage(s.ConsumerName, s.MessageID, s.PayloadHash, s.ReceivedAt)
	if err != nil {
		return nil, err
	}
	message.completedAt = timeValue(s.CompletedAt)
	return message, nil
}
