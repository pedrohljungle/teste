// Package repositories holds the I/O adapters. Every conversation with Postgres, the cache and
// the queue lives here, behind a contract declared in interfaces/<domain>.
package repositories

import (
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/repositories/cache"
	"github.com/estrategiahq/pedro-test/app/src/repositories/queue"
)

// Module wires every adapter of this layer.
//
// The two below are domain-agnostic: a key-value cache and an SQS queue, useful to any domain.
// A domain adapter goes in repositories/<domain>/ with its own module, and binds itself to the
// contracts in interfaces/<domain>. When a shared adapter has to satisfy a domain contract,
// the binding goes here — making a shared adapter import a domain would tie it to the first
// one that happened to use it.
var Module = fx.Module("repositories",
	cache.Module,
	queue.Module,
)
