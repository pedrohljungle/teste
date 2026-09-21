package health

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sqs"
	"github.com/aws/aws-sdk-go-v2/service/sqs/types"

	healthiface "github.com/estrategiahq/pedro-test/app/src/interfaces/health"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

var _ healthiface.Checker = (*sqsChecker)(nil)

type sqsChecker struct {
	client *sqs.Client
	queues []string
}

// NewSQSChecker checks both queues the application uses, the one it consumes and the one it
// publishes events to. A queue that answers a read of its attributes exists, is reachable and is one
// this application is allowed to see.
func NewSQSChecker(client *sqs.Client, worker config.Worker, events config.Events) healthiface.Checker {
	return &sqsChecker{client: client, queues: []string{worker.QueueURL, events.QueueURL}}
}

func (c *sqsChecker) Name() string { return "sqs" }

func (c *sqsChecker) Check(ctx context.Context) error {
	for _, queue := range c.queues {
		_, err := c.client.GetQueueAttributes(ctx, &sqs.GetQueueAttributesInput{
			QueueUrl:       awssdk.String(queue),
			AttributeNames: []types.QueueAttributeName{types.QueueAttributeNameQueueArn},
		})
		if err != nil {
			return fmt.Errorf("read the attributes of %s: %w", queue, err)
		}
	}
	return nil
}
