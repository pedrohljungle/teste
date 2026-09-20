// Package wallet is the delivery of the wallet domain: its HTTP routes.
package wallet

import "go.uber.org/fx"

// Module provides the HTTP handler.
var Module = fx.Module("handlers.wallet",
	fx.Provide(NewHandler),
)
