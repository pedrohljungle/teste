package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// traceAttributePrefix namespaces the trace headers among the message attributes, so they
// never collide with an attribute a producer may want to add.
const traceAttributePrefix = "otel-"

// maxReasonLength keeps the reason inside what a message attribute comfortably carries.
const maxReasonLength = 500

// SQS is the job queue as the worker sees it: it consumes messages (jobrunner.Source) and sends
// the ones it gave up on to the dead letter queue. Nothing in this system produces jobs: the
// messages come from the systems that call this one.
//
// It carries bytes on purpose: the payload is the domain's business, and an adapter that
// unmarshalled it would have to know every kind of message that will ever exist.
type SQS struct {
	client *sqs.Client
	cfg    config.Worker
	obs    *observability.Observer
}

// NewSQS builds the adapter.
func NewSQS(client *sqs.Client, cfg config.Worker, obs *observability.Observer) *SQS {
	return &SQS{client: client, cfg: cfg, obs: obs}
}

// Name is the queue this adapter reads from and writes to.
func (q *SQS) Name() string { return q.cfg.Name() }

// Send puts a message the consumer gave up on, unchanged, on the dead letter queue, with the
// reason it was given up. A person finds out why from the attribute, instead of having to guess from
// the body.
//
// The queue is FIFO, so it needs a group and a deduplication id: one group for everything, since
// nothing reads it in order, and the hash of the payload, so the same poison message sent twice is
// kept once.
func (q *SQS) Send(ctx context.Context, msg structs.QueueMessage, reason string) error {
	return observability.TraceErr(ctx, q.obs, observability.LayerRepository, "queue.DeadLetter", func(ctx context.Context) error {
		sum := sha256.Sum256(msg.Payload)
		_, err := q.client.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:               awssdk.String(q.cfg.DeadLetterURL),
			MessageBody:            awssdk.String(string(msg.Payload)),
			MessageGroupId:         awssdk.String("dead-letter"),
			MessageDeduplicationId: awssdk.String(hex.EncodeToString(sum[:])),
			MessageAttributes: map[string]types.MessageAttributeValue{
				"failureReason": stringAttribute(truncate(reason, maxReasonLength)),
				"sourceQueue":   stringAttribute(q.cfg.Name()),
			},
		})
		if err != nil {
			return fmt.Errorf("send to the dead letter queue: %w", err)
		}
		return nil
	}, observability.String("queue", q.cfg.Name()))
}

func stringAttribute(value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: awssdk.String("String"), StringValue: awssdk.String(value)}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// Consume long polls the queue.
//
// It opens no span: it spends most of its time blocked, and a span per wait would fill the
// trace with noise. Returning (nil, nil) on an empty poll is what gives the runtime a chance
// to notice it was asked to stop.
func (q *SQS) Consume(ctx context.Context) (*structs.QueueMessage, error) {
	out, err := q.client.ReceiveMessage(ctx, &sqs.ReceiveMessageInput{
		QueueUrl:              awssdk.String(q.cfg.QueueURL),
		MaxNumberOfMessages:   1,
		WaitTimeSeconds:       int32(q.cfg.PollTimeout.Seconds()),
		VisibilityTimeout:     q.cfg.VisibilityTimeout,
		MessageAttributeNames: []string{"All"},
	})
	if err != nil {
		if ctx.Err() != nil {
			// A cancelled poll is the shutdown path, not a failure.
			return nil, nil
		}
		return nil, fmt.Errorf("receive message: %w", err)
	}
	if len(out.Messages) == 0 {
		return nil, nil
	}

	msg := out.Messages[0]
	return &structs.QueueMessage{
		Queue:        q.cfg.Name(),
		TraceContext: traceContextFrom(msg.MessageAttributes),
		Payload:      []byte(awssdk.ToString(msg.Body)),
		AckToken:     awssdk.ToString(msg.ReceiptHandle),
	}, nil
}

// Ack deletes the message. Not acking is how a failed handler asks for redelivery: the message
// reappears once the visibility timeout expires and, after enough attempts, SQS moves it to
// the dead letter queue instead of retrying forever.
func (q *SQS) Ack(ctx context.Context, msg structs.QueueMessage) error {
	_, err := q.client.DeleteMessage(ctx, &sqs.DeleteMessageInput{
		QueueUrl:      awssdk.String(q.cfg.QueueURL),
		ReceiptHandle: awssdk.String(msg.AckToken),
	})
	if err != nil {
		return fmt.Errorf("acknowledge message: %w", err)
	}
	return nil
}

// Depth is the approximate backlog, the number that answers whether the worker is keeping up.
func (q *SQS) Depth(ctx context.Context) (int64, error) {
	out, err := q.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
		QueueUrl:       awssdk.String(q.cfg.QueueURL),
		AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameApproximateNumberOfMessages},
	})
	if err != nil {
		return 0, fmt.Errorf("measure queue depth: %w", err)
	}

	raw, ok := out.Attributes[string(types.QueueAttributeNameApproximateNumberOfMessages)]
	if !ok {
		return 0, nil
	}
	depth, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse queue depth %q: %w", raw, err)
	}
	return depth, nil
}

func traceContextFrom(attributes map[string]types.MessageAttributeValue) map[string]string {
	carrier := map[string]string{}
	for key, value := range attributes {
		if len(key) <= len(traceAttributePrefix) || key[:len(traceAttributePrefix)] != traceAttributePrefix {
			continue
		}
		carrier[key[len(traceAttributePrefix):]] = awssdk.ToString(value.StringValue)
	}
	if len(carrier) == 0 {
		return nil
	}
	return carrier
}
