// Package persistence is the Postgres adapter of the unit of work. It is not a domain: every
// domain's services share it, which is why it sits beside queue and not inside a domain, and why its wiring is done in the layer module and not in a domain's.
package persistence

import "go.uber.org/fx"

// Module provides the unit of work.
var Module = fx.Module("repositories.persistence",
	fx.Provide(NewUnitOfWork),
)
