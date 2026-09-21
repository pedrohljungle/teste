// Package health is the contract of the readiness probe: what it asks of the things the application
// depends on.
package health

import "context"

// Checker answers whether one dependency of the application is reachable right now. Readiness is
// the answer of all of them, and it is what tells an orchestrator whether to send this instance
// traffic. Liveness is not: a process that is alive but cannot reach its database should be taken
// out of rotation and not killed, and the two probes exist to tell those apart.
type Checker interface {
	// Name is what the dependency is called in the probe response.
	Name() string
	// Check returns nil when the dependency answers, and an error when it does not. The caller
	// bounds it with a deadline.
	Check(ctx context.Context) error
}
