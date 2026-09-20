// Package wagering is the Postgres adapter of the wagering contract.
package wagering

import "go.uber.org/fx"

// Module provides the repository behind the contract in interfaces/wagering.
var Module = fx.Module("repositories.wagering",
	fx.Provide(NewPostgresRepository),
)
