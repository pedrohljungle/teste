//go:build e2e

// Package core is the machinery of the end to end suite: it starts the real dependencies with
// testcontainers, applies the migrations and boots the application, then hands the tests a
// Stack with everything they need to drive it.
//
// It exists so the test files hold scenarios and nothing else. A test that spends thirty lines
// wiring fx before asserting anything is a test nobody reads, and the wiring is identical for
// every one of them.
//
// There is no cycle: core imports app/src, the test files import core, and nothing in app/src
// knows this package exists. Every file here carries the e2e build tag, so none of it reaches
// the normal build.
package core
