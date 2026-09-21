// Package reference is the delivery of the job that resolves the reversals waiting for a
// transaction that had not arrived: the periodic tick that looks for them again.
package reference

import "go.uber.org/fx"

// Module provides the cronjob handler.
var Module = fx.Module("handlers.reference",
	fx.Provide(NewCronjobHandler),
)
