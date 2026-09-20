// Package wallet holds the business rules of the wallet domain.
package wallet

import "go.uber.org/fx"

// Module provides the service behind the contract in interfaces/wallet.
var Module = fx.Module("services.wallet",
	fx.Provide(NewService),
)
