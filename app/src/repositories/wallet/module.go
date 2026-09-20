// Package wallet is the Postgres adapter of the wallet contract.
package wallet

import "go.uber.org/fx"

// Module provides the repository behind the contract in interfaces/wallet.
var Module = fx.Module("repositories.wallet",
	fx.Provide(NewPostgresRepository),
)
