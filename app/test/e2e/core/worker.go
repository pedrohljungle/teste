//go:build e2e

package core

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// errAskedToFail is what the recorder returns when a test told it to fail a delivery. The
// runtime cannot tell it apart from a real failure, which is the point.
var errAskedToFail = errors.New("handler asked to fail")

// recorder is the handler the suite registers on the queue runtime.
//
// This repository carries no domain, so there is no real handler to drive. What the suite can
// still prove — and what matters most about the runtime — is the delivery contract itself:
// return nil and the message is gone, return an error and it comes back.
type recorder struct {
	mu        sync.Mutex
	handled   []string
	failCount int
}

func (r *recorder) handle(_ context.Context, msg structs.QueueMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handled = append(r.handled, string(msg.Payload))

	if r.failCount > 0 {
		r.failCount--
		return errAskedToFail
	}
	return nil
}

// FailNextDeliveries makes the next n deliveries return an error, whatever they carry.
func (s *Stack) FailNextDeliveries(n int) {
	s.recorder.mu.Lock()
	defer s.recorder.mu.Unlock()
	s.recorder.failCount = n
}

// DeliveriesOf counts how many times a payload reached the handler. More than one means the
// message was redelivered.
func (s *Stack) DeliveriesOf(payload string) int {
	s.recorder.mu.Lock()
	defer s.recorder.mu.Unlock()

	count := 0
	for _, handled := range s.recorder.handled {
		if handled == payload {
			count++
		}
	}
	return count
}

// WaitForDeliveries blocks until a payload was handled at least n times.
func (s *Stack) WaitForDeliveries(t *testing.T, payload string, n int, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.DeliveriesOf(payload) >= n {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("payload %q was delivered %d times within %s, expected at least %d",
		payload, s.DeliveriesOf(payload), timeout, n)
}
