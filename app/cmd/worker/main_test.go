package main

import (
	"testing"

	"go.uber.org/fx"
)

// See the server's: the wiring is validated without opening a connection.
func TestTheWorkerGraphIsComplete(t *testing.T) {
	if err := fx.ValidateApp(options()...); err != nil {
		t.Fatalf("the worker graph does not resolve: %v", err)
	}
}
