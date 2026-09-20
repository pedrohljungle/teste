// Package wagering holds the business rules of the wagering domain: applying an external
// operation to a wallet exactly once.
package wagering

import "go.uber.org/fx"

// Module provides the service behind the contract in interfaces/wagering.
var Module = fx.Module("services.wagering",
	fx.Provide(NewService),
)
