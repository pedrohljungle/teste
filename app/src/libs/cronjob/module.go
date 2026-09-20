// Package cronjob is the runtime of the work that is triggered by time and not by a message: the
// publisher that drains the outbox and the job that resolves pending references. It is the
// counterpart of jobrunner for what has no queue to poll, and like it holds no business rule and
// knows no domain.
//
// The vocabulary is kept apart on purpose. The worker is the process. A job is a message on a
// queue, consumed by jobrunner. A cronjob is a recurring tick, run by this package.
package cronjob

import "go.uber.org/fx"

// Module provides the runner. Registering the domain cronjobs is the worker entrypoint's job,
// the same way registering the queue handlers is.
var Module = fx.Module("cronjob",
	fx.Provide(NewRunner),
)
