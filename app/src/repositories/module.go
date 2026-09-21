// Package repositories holds the I/O adapters. Every conversation with Postgres and
// the queue lives here, behind a contract declared in interfaces/<domain>.
package repositories

import (
	"go.uber.org/fx"

	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/repositories/health"
	"github.com/estrategiahq/pedro-test/app/src/repositories/inbox"
	"github.com/estrategiahq/pedro-test/app/src/repositories/outbox"
	"github.com/estrategiahq/pedro-test/app/src/repositories/persistence"
	"github.com/estrategiahq/pedro-test/app/src/repositories/queue"
	"github.com/estrategiahq/pedro-test/app/src/repositories/wagering"
	"github.com/estrategiahq/pedro-test/app/src/repositories/wallet"
)

// Module wires every adapter of this layer.
//
// queue and persistence are domain-agnostic: an SQS queue and the unit of work every domain
// shares. A domain adapter goes in repositories/<domain>/ with its own
// module, and binds itself to the contracts in interfaces/<domain>. When a shared adapter has
// to satisfy a domain contract, the binding goes here — making a shared adapter import a domain
// would tie it to the first one that happened to use it.
var Module = fx.Module("repositories",
	queue.Module,
	persistence.Module,
	wallet.Module,
	wagering.Module,
	outbox.Module,
	inbox.Module,
	health.Module,
	// The queue adapter is domain-agnostic, and it also is what a domain calls the dead letter
	// queue. Binding it here, and not in the queue package, keeps the adapter from importing a
	// domain, and this function is what holds it to the contract at compile time.
	fx.Provide(func(q *queue.SQS) wageringiface.DeadLetter { return q }),
)
