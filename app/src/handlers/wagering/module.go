// Package wagering is the delivery of the wagering domain: its HTTP routes and the consumer of its
// queue.
package wagering

import "go.uber.org/fx"

// Module provides the HTTP handler and the queue handler.
var Module = fx.Module("handlers.wagering",
	fx.Provide(
		NewHandler,
		NewJobHandler,
	),
)
