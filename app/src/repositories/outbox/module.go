// Package outbox is the Postgres adapter of the outbox contract.
package outbox

import "go.uber.org/fx"

// Module provides the repository behind the contract in interfaces/outbox.
var Module = fx.Module("repositories.outbox",
	fx.Provide(NewPostgresRepository),
)
