// Package health answers the orchestrator probes.
package health

import "go.uber.org/fx"

// Module provides the health handler, wired to the group of checkers the repositories publish.
var Module = fx.Module("handlers.health",
	fx.Provide(
		fx.Annotate(NewHandler, fx.ParamTags(``, ``, `group:"health"`)),
	),
)
