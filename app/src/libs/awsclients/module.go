// Package awsclients builds the AWS SDK clients. Only construction lives here: who sends and
// receives messages is repositories/, the same rule the database pools follow.
package awsclients

import "go.uber.org/fx"

// Module is shared by every entrypoint: the server publishes and the worker consumes, both
// against the same queue.
var Module = fx.Module("awsclients",
	fx.Provide(NewSQS),
)
