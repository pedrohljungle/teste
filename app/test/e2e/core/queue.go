//go:build e2e

package core

import (
	"context"
	"strconv"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// PublishMessage sends a message to the FIFO queue of operations the way a producer does, choosing
// the message group and the deduplication id. A scenario about the application deduplicating sends
// the same body under different deduplication ids, because the queue drops a repeated one inside its
// window and would hide the very thing the scenario is about.
func (s *Stack) PublishMessage(t *testing.T, groupID, deduplicationID, body string) {
	t.Helper()

	_, err := s.sqs(t).SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:               awssdk.String(s.queueURL),
		MessageBody:            awssdk.String(body),
		MessageGroupId:         awssdk.String(groupID),
		MessageDeduplicationId: awssdk.String(deduplicationID),
	})
	if err != nil {
		t.Fatalf("publish a message: %v", err)
	}
}

// QueueState is how many messages are waiting and how many are being processed.
func (s *Stack) QueueState(t *testing.T) (int, int) {
	t.Helper()

	out, err := s.sqs(t).GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
		QueueUrl: awssdk.String(s.queueURL),
		AttributeNames: []types.QueueAttributeName{
			types.QueueAttributeNameApproximateNumberOfMessages,
			types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
		},
	})
	if err != nil {
		t.Fatalf("read queue attributes: %v", err)
	}
	visible, _ := strconv.Atoi(out.Attributes["ApproximateNumberOfMessages"])
	inFlight, _ := strconv.Atoi(out.Attributes["ApproximateNumberOfMessagesNotVisible"])
	return visible, inFlight
}

// WaitForEmptyQueue waits until nothing is visible and nothing is in flight. It is how a test
// asserts that a message was consumed AND acknowledged, instead of quietly cycling until the
// dead letter queue.
func (s *Stack) WaitForEmptyQueue(t *testing.T, timeout time.Duration) {
	t.Helper()

	client := s.sqs(t)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		out, err := client.GetQueueAttributes(context.Background(), &sqs.GetQueueAttributesInput{
			QueueUrl: awssdk.String(s.queueURL),
			AttributeNames: []types.QueueAttributeName{
				types.QueueAttributeNameApproximateNumberOfMessages,
				types.QueueAttributeNameApproximateNumberOfMessagesNotVisible,
			},
		})
		if err != nil {
			t.Fatalf("read queue attributes: %v", err)
		}
		if out.Attributes["ApproximateNumberOfMessages"] == "0" &&
			out.Attributes["ApproximateNumberOfMessagesNotVisible"] == "0" {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
	t.Fatalf("the queue did not drain within %s", timeout)
}

func (s *Stack) sqs(t *testing.T) *sqs.Client {
	t.Helper()

	cfg, err := awsconfig.LoadDefaultConfig(context.Background(), awsconfig.WithRegion("us-east-1"))
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return sqs.NewFromConfig(cfg, func(o *sqs.Options) {
		o.BaseEndpoint = awssdk.String(s.awsEndpoint)
	})
}
