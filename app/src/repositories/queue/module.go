// Package queue is the SQS adapter. It is not a domain: it carries bytes, and what those bytes
// mean is decided by whoever publishes and whoever handles them.
package queue

import "go.uber.org/fx"

// Module provides the adapter. It satisfies jobrunner.Source, so the worker entrypoint can
// register a handler against it.
var Module = fx.Module("repositories.queue",
	fx.Provide(NewSQS),
)
