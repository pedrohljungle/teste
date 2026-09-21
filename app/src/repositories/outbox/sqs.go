package outbox

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// traceAttributePrefix namespaces the trace headers among the message attributes, so they never
// collide with an attribute a consumer may want to read.
const traceAttributePrefix = "otel-"

// sqsPublisher sends events to the FIFO queue of integration events.
//
// The routing contract is what a consumer relies on. The group id is the aggregate id, so the
// events of one aggregate are delivered in the order they were sent. The deduplication id is the
// event id, so a republication after a crash is recognised and dropped by the queue within its
// window, and by the consumer beyond it.
type sqsPublisher struct {
	client *sqs.Client
	cfg    config.Events
	obs    *observability.Observer
}

// NewSQSPublisher builds the publisher. The queue is created by the infrastructure, never here.
func NewSQSPublisher(client *sqs.Client, cfg config.Events, obs *observability.Observer) outboxiface.Publisher {
	return &sqsPublisher{client: client, cfg: cfg, obs: obs}
}

func (p *sqsPublisher) Publish(ctx context.Context, event *entities.OutboxEvent) error {
	return observability.TraceErr(ctx, p.obs, observability.LayerRepository, "outbox.Publisher.Publish", func(ctx context.Context) error {
		attributes := map[string]types.MessageAttributeValue{
			"eventType":     stringAttribute(string(event.Type())),
			"eventId":       stringAttribute(event.ID().String()),
			"correlationId": stringAttribute(event.CorrelationID()),
		}
		for key, value := range observability.InjectTrace(ctx) {
			attributes[traceAttributePrefix+key] = stringAttribute(value)
		}

		_, err := p.client.SendMessage(ctx, &sqs.SendMessageInput{
			QueueUrl:               awssdk.String(p.cfg.QueueURL),
			MessageBody:            awssdk.String(string(event.Payload())),
			MessageGroupId:         awssdk.String(event.AggregateID().String()),
			MessageDeduplicationId: awssdk.String(event.ID().String()),
			MessageAttributes:      attributes,
		})
		if err != nil {
			return fmt.Errorf("publish event %s: %w", event.ID(), err)
		}
		return nil
	}, observability.String("queue", p.cfg.Name()),
		observability.String("eventId", event.ID().String()),
		observability.String("eventType", string(event.Type())))
}

func stringAttribute(value string) types.MessageAttributeValue {
	return types.MessageAttributeValue{DataType: awssdk.String("String"), StringValue: awssdk.String(value)}
}
