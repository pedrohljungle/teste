package awsclients

import (
	"context"
	"fmt"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

// NewSQS builds the SQS client.
//
// An explicit endpoint is only used outside AWS, to point the SDK at a local emulator such as
// LocalStack. In every real environment it is empty and the SDK resolves the endpoint from the
// region, so nothing about the deployed setup depends on this branch.
func NewSQS(cfg config.AWS) (*sqs.Client, error) {
	awsCfg, err := awsconfig.LoadDefaultConfig(context.Background(),
		awsconfig.WithRegion(cfg.Region),
	)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	var options []func(*sqs.Options)
	if cfg.Endpoint != "" {
		options = append(options, func(o *sqs.Options) {
			o.BaseEndpoint = awssdk.String(cfg.Endpoint)
		})
	}
	return sqs.NewFromConfig(awsCfg, options...), nil
}
