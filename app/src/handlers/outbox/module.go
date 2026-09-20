// Package outbox is the delivery of the outbox publisher: the periodic tick that drains it.
package outbox

import "go.uber.org/fx"

// Module provides the cronjob handler.
var Module = fx.Module("handlers.outbox",
	fx.Provide(NewCronjobHandler),
)
