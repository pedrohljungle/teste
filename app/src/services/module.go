// Package services is the business rule layer. It holds no code of its own: every rule lives
// in a domain module below it, and this package only aggregates them.
package services

import (
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/services/wagering"
	"github.com/estrategiahq/pedro-test/app/src/services/wallet"
)

// Module wires every domain module of this layer.
var Module = fx.Module("services",
	wagering.Module,
	wallet.Module,
)
