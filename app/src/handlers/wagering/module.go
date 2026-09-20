// Package wagering is the delivery of the wagering domain: its HTTP routes.
package wagering

import "go.uber.org/fx"

// Module provides the HTTP handler.
var Module = fx.Module("handlers.wagering",
	fx.Provide(NewHandler),
)
