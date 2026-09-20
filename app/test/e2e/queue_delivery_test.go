//go:build e2e

package e2e

import (
	"testing"
	"time"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: queue delivery
//
//   As the worker runtime
//   I want the outcome of a handler to decide the fate of the message
//   So that work is never lost and never silently repeated forever
//
//   This is the contract the whole runtime exists to enforce, and it has exactly two outcomes:
//
//     handler returns nil    -> the message is deleted from SQS
//     handler returns error  -> the message is NOT deleted, and SQS delivers it again once the
//                               visibility timeout expires
//
//   Against real SQS, not a fake, because the half that can actually be wrong is the delete
//   call and the visibility timeout — neither of which a double would exercise.
//
//   Scenarios:
//     - a handled message is deleted from the queue
//     - a message whose handler fails is delivered again
//     - a message that fails once and then succeeds ends up deleted

// Scenario: a handled message is deleted from the queue
//
//	Given a worker consuming the queue
//	When a message is published
//	Then the handler receives it
//	And the queue drains
func TestHandledMessageIsDeletedFromTheQueue(t *testing.T) {
	payload := core.UniqueTitle("handled")

	stack.Publish(t, payload)

	stack.WaitForDeliveries(t, payload, 1, 30*time.Second)
	stack.WaitForEmptyQueue(t, 30*time.Second)

	if got := stack.DeliveriesOf(payload); got != 1 {
		t.Fatalf("a message handled without error must be delivered once, got %d", got)
	}
}

// Scenario: a message whose handler fails is delivered again
//
//	Given a worker whose next delivery will fail
//	When a message is published
//	Then the handler receives it and fails
//	And SQS delivers the same message again after the visibility timeout
//
//	Not acking is the ONLY way this runtime asks for a retry. If the runner ever deleted the
//	message before knowing the outcome, this scenario is what would catch it.
func TestFailedMessageIsDeliveredAgain(t *testing.T) {
	payload := core.UniqueTitle("failed-once")
	stack.FailNextDeliveries(1)

	stack.Publish(t, payload)

	stack.WaitForDeliveries(t, payload, 2, 60*time.Second)
}

// Scenario: a message that fails once and then succeeds ends up deleted
//
//	Given a message that failed on its first delivery
//	When the second delivery succeeds
//	Then the queue drains
//
//	The complement of the scenario above: a retry that works has to end the cycle, otherwise
//	the message would keep coming back until the dead letter queue.
func TestMessageDeletedAfterASuccessfulRetry(t *testing.T) {
	payload := core.UniqueTitle("retried")
	stack.FailNextDeliveries(1)

	stack.Publish(t, payload)

	stack.WaitForDeliveries(t, payload, 2, 60*time.Second)
	stack.WaitForEmptyQueue(t, 30*time.Second)
}
