// Package outbox holds the rule of draining the outbox: claim what is due, publish it, and
// decide what happens to each event that could not be published.
package outbox

import "go.uber.org/fx"

// Module provides the service behind the contract in interfaces/outbox.
var Module = fx.Module("services.outbox",
	fx.Provide(NewService),
)
