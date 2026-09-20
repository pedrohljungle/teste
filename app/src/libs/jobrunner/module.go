// Package jobrunner is the queue runtime: it polls the registered sources and dispatches each
// message to the handler registered with it. It is the worker counterpart of the HTTP server,
// and like it holds no business rule and knows no domain.
package jobrunner

import "go.uber.org/fx"

// Module provides the runner. Registering the domain workers is the worker entrypoint's job,
// the same way registering routes is the server entrypoint's job.
var Module = fx.Module("jobrunner",
	fx.Provide(NewRunner),
)
