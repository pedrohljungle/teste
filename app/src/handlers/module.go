// Package handlers is the delivery layer. It holds no handler of its own: each domain has its
// own module below, and this package aggregates them.
//
// health and identity are not domains of this system — they are the two routes any service has
// regardless of what it does. A domain adds handlers/<domain>/ with its own module.go, its
// ServerRoutes for the HTTP side and its PrepareWorker for the queue side.
package handlers

import (
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/handlers/health"
	"github.com/estrategiahq/pedro-test/app/src/handlers/identity"
	"github.com/estrategiahq/pedro-test/app/src/handlers/outbox"
	"github.com/estrategiahq/pedro-test/app/src/handlers/reference"
	"github.com/estrategiahq/pedro-test/app/src/handlers/wagering"
	"github.com/estrategiahq/pedro-test/app/src/handlers/wallet"
)

// Module wires every delivery module. Each runtime (HTTP or queue) picks what it dispatches
// to; wiring them all keeps a handler from being missing because someone forgot a line.
var Module = fx.Module("handlers",
	health.Module,
	identity.Module,
	outbox.Module,
	reference.Module,
	wagering.Module,
	wallet.Module,
)
