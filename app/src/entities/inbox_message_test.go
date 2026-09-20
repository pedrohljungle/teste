package entities

import (
	"errors"
	"testing"
	"time"
)

func TestAnInboxMessageIsRecordedAsReceivedAndNotCompleted(t *testing.T) {
	message, err := NewInboxMessage("wager-consumer", "msg-123", []byte{1, 2, 3}, t0)
	if err != nil {
		t.Fatalf("NewInboxMessage: %v", err)
	}
	if message.ConsumerName() != "wager-consumer" || message.MessageID() != "msg-123" ||
		!message.ReceivedAt().Equal(t0) || message.IsCompleted() || !message.CompletedAt().IsZero() {
		t.Fatalf("message = %+v", message.Snapshot())
	}
}

func TestAnInboxMessageCompletesOnce(t *testing.T) {
	message, err := NewInboxMessage("wager-consumer", "msg-123", []byte{1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Complete(t0.Add(time.Second)); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if !message.IsCompleted() || !message.CompletedAt().Equal(t0.Add(time.Second)) {
		t.Fatalf("completedAt = %s", message.CompletedAt())
	}
	if err := message.Complete(t0.Add(time.Hour)); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("completing twice: error = %v, want ErrInvalidTransition", err)
	}
	if !message.CompletedAt().Equal(t0.Add(time.Second)) {
		t.Fatalf("the first completion was overwritten: %s", message.CompletedAt())
	}
}

func TestAnInboxMessageTellsARedeliveryFromADifferentBodyUnderTheSameId(t *testing.T) {
	message, err := NewInboxMessage("wager-consumer", "msg-123", []byte{1, 2, 3}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if !message.MatchesPayload([]byte{1, 2, 3}) {
		t.Error("the same hash must match")
	}
	if message.MatchesPayload([]byte{1, 2, 4}) {
		t.Error("a different hash must not match")
	}
}

func TestAnInboxMessageRefusesWhatCannotIdentifyIt(t *testing.T) {
	cases := []struct {
		name     string
		consumer string
		message  string
		hash     []byte
	}{
		{"no consumer", "", "msg", []byte{1}},
		{"blank consumer", "  ", "msg", []byte{1}},
		{"no message id", "consumer", "", []byte{1}},
		{"no hash", "consumer", "msg", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := NewInboxMessage(tc.consumer, tc.message, tc.hash, t0); !errors.Is(err, ErrInvalidInboxMessage) {
				t.Fatalf("error = %v, want ErrInvalidInboxMessage", err)
			}
		})
	}
}

func TestRehydratingAnInboxMessageKeepsItsCompletion(t *testing.T) {
	message, err := NewInboxMessage("wager-consumer", "msg-123", []byte{1, 2}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Complete(t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}

	restored, err := RehydrateInboxMessage(message.Snapshot())
	if err != nil {
		t.Fatalf("RehydrateInboxMessage: %v", err)
	}
	if !restored.IsCompleted() || !restored.CompletedAt().Equal(t0.Add(time.Minute)) || !restored.MatchesPayload([]byte{1, 2}) {
		t.Fatalf("restored = %+v", restored.Snapshot())
	}
	if err := restored.Complete(t0); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("a restored completed message must stay completed: error = %v", err)
	}
	if _, err := RehydrateInboxMessage(InboxMessageSnapshot{}); !errors.Is(err, ErrInvalidInboxMessage) {
		t.Fatalf("an empty snapshot: error = %v", err)
	}
}
