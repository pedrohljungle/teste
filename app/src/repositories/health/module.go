// Package health is the adapter of the readiness probe: one checker for each dependency the
// application cannot work without.
package health

import "go.uber.org/fx"

// Module provides the checkers into the group the health handler reads. Adding a dependency to
// readiness is adding a checker here.
var Module = fx.Module("repositories.health",
	fx.Provide(
		fx.Annotate(NewPostgresChecker, fx.ResultTags(`group:"health"`)),
		fx.Annotate(NewSQSChecker, fx.ResultTags(`group:"health"`)),
	),
)
