// Package identity exposes who the authenticated caller is, as the IDP described it.
package identity

import "go.uber.org/fx"

// Module provides the identity handler.
var Module = fx.Module("handlers.identity",
	fx.Provide(NewHandler),
)
