package main

import (
	"testing"

	"go.uber.org/fx"
)

// The graph is checked without building anything: fx.ValidateApp resolves every type and reports a
// port that has no adapter, or a constructor that asks for something nobody provides, and it opens
// no connection to do it. It is what turns "the wiring is wrong" from a failure at boot into a
// failure of this test.
func TestTheServerGraphIsComplete(t *testing.T) {
	if err := fx.ValidateApp(options()...); err != nil {
		t.Fatalf("the server graph does not resolve: %v", err)
	}
}
