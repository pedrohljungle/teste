//go:build e2e

package core

import (
	"context"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Publish writes straight to SQS, which is how a test reaches the worker without going through
// the server. The payload is opaque: the runtime carries bytes, and so does this.
func (s *Stack) Publish(t *testing.T, payload string) {
	t.Helper()

	_, err := s.sqs(t).SendMessage(context.Background(), &sqs.SendMessageInput{
		QueueUrl:    awssdk.String(s.queueURL),
		MessageBody: awssdk.String(payload),
	})
	if err != nil {
		t.Fatalf("publish a message: %v", err)
	}
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
