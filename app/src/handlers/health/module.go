// Package health answers the orchestrator probe.
package health

import "go.uber.org/fx"

// Module provides the health handler.
var Module = fx.Module("handlers.health",
	fx.Provide(NewHandler),
)
