//go:build e2e

// Package e2e drives the whole backend against real dependencies.
//
// Every file here is one Feature, written as Gherkin in the comments: the file header states
// what the feature is and under which background, and each test function states its scenario.
// The Go code below a scenario is that scenario, step by step — if the two ever disagree, the
// comment is the bug.
//
// The machinery lives in ./core. Nothing in this package starts a container or wires fx.
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// stack is the running application, shared by every test in the package. The containers take
// close to a minute to come up, so paying for them once is the difference between a suite
// people run and one they skip.
//
//	Background:
//	  Given Postgres, Redis, LocalStack and Keycloak running as containers
//	  And the migrations applied to that database
//	  And the server and the worker running against them
//	  And the realm holding the users pedro (app-admin) and joana (no role)
var stack *core.Stack

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	var err error
	stack, err = core.Start(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "e2e: %v\n", err)
		return 1
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer stopCancel()
		_ = stack.Stop(stopCtx)
	}()

	return m.Run()
}
