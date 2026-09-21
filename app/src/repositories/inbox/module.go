// Package inbox is the Postgres adapter of the inbox contract.
package inbox

import "go.uber.org/fx"

// Module provides the repository behind the contract in interfaces/inbox.
var Module = fx.Module("repositories.inbox",
	fx.Provide(NewPostgresRepository),
)
