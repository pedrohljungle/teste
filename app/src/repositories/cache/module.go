// Package cache is the shared key-value adapter. It is not a domain: it knows nothing about
// what is being cached, and any domain port shaped like Get/Set can be bound to it.
package cache

import "go.uber.org/fx"

// Module provides the adapter. The binding to a domain port is done by the layer module,
// which is what keeps this package domain-agnostic.
var Module = fx.Module("repositories.cache",
	fx.Provide(NewRedisRepository),
)
