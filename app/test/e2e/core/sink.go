//go:build e2e

package core

import (
	"context"
	"sync"
	"testing"
	"time"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"
)

// Delivery is one message of the events queue as a downstream consumer receives it.
type Delivery struct {
	EventID         string
	Body            string
	GroupID         string
	DeduplicationID string
	Attributes      map[string]string
	ReceivedAt      time.Time
	// Sequence is the order in which the sink received it, across every event.
	Sequence int
}

// sink is the downstream consumer of the integration events. It reads the queue continuously,
// records what it gets and deletes it, the way a real consumer would.
//
// The suite needs one because the events queue is shared by every scenario, and a scenario that
// read the queue itself would hide the messages of the others for as long as their visibility
// lasts, which is how a suite becomes flaky. Reading is done here, once, and scenarios ask what
// arrived.
type sink struct {
	mu         sync.Mutex
	deliveries []Delivery
	cancel     context.CancelFunc
	done       chan struct{}
}

func startSink(ctx context.Context, endpoint, queueURL string) (*sink, error) {
	cfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		return nil, err
	}
	client := sqs.NewFromConfig(cfg, func(o *sqs.Options) { o.BaseEndpoint = awssdk.String(endpoint) })

	loopCtx, cancel := context.WithCancel(context.Background())
	s := &sink{cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.consume(loopCtx, client, queueURL)
	}()
	return s, nil
}

func (s *sink) consume(ctx context.Context, client *sqs.Client, queueURL string) {
	for ctx.Err() == nil {
		out, err := client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
			QueueUrl:                    awssdk.String(queueURL),
			MaxNumberOfMessages:         10,
			WaitTimeSeconds:             1,
			MessageAttributeNames:       []string{"All"},
			MessageSystemAttributeNames: []types.MessageSystemAttributeName{types.MessageSystemAttributeNameAll},
		})
		if err != nil {
			time.Sleep(200 * time.Millisecond)
			continue
		}
		for _, message := range out.Messages {
			s.record(message)
			_, _ = client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
				QueueUrl:      awssdk.String(queueURL),
				ReceiptHandle: message.ReceiptHandle,
			})
		}
	}
}

func (s *sink) record(message types.Message) {
	attributes := map[string]string{}
	for name, value := range message.MessageAttributes {
		attributes[name] = awssdk.ToString(value.StringValue)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.deliveries = append(s.deliveries, Delivery{
		EventID:         attributes["eventId"],
		Body:            awssdk.ToString(message.Body),
		GroupID:         message.Attributes[string(types.MessageSystemAttributeNameMessageGroupId)],
		DeduplicationID: message.Attributes[string(types.MessageSystemAttributeNameMessageDeduplicationId)],
		Attributes:      attributes,
		ReceivedAt:      time.Now(),
		Sequence:        len(s.deliveries) + 1,
	})
}

func (s *sink) stop() {
	s.cancel()
	<-s.done
}

func (s *sink) of(eventID string) []Delivery {
	s.mu.Lock()
	defer s.mu.Unlock()

	var found []Delivery
	for _, delivery := range s.deliveries {
		if delivery.EventID == eventID {
			found = append(found, delivery)
		}
	}
	return found
}

// EventDeliveries is every time the events queue delivered the event to the downstream consumer.
func (s *Stack) EventDeliveries(eventID string) []Delivery {
	return s.sink.of(eventID)
}

// WaitForEvent blocks until the event reaches the downstream consumer and returns the delivery.
func (s *Stack) WaitForEvent(t *testing.T, eventID string, timeout time.Duration) Delivery {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if found := s.sink.of(eventID); len(found) > 0 {
			return found[0]
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("event %s did not reach the events queue within %s", eventID, timeout)
	return Delivery{}
}
