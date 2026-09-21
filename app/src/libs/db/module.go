// Package db opens the connections to the data services and hands their lifecycle to fx.
// Only connections live here: queries belong to repositories.
package db

import "go.uber.org/fx"

// Module is shared by every entrypoint.
var Module = fx.Module("db",
	fx.Provide(
		NewPostgres,
		NewAccessor,
	),
)
