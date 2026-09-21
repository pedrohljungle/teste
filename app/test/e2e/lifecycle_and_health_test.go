//go:build e2e

package e2e

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Composition and lifecycle
//
//	As an operator, I want boot and shutdown to be observable and complete,
//	so that a deploy neither starts broken nor stops halfway.
//
//	The composition of the two entrypoints is validated without opening a connection, by
//	fx.ValidateApp in app/cmd/server and app/cmd/worker, which fails when a port has no adapter. The
//	scenarios here are the ones that need the real dependencies.
//
//	Scenarios:
//	  - The application starts and stops releasing every resource
//	  - Readiness answers ok when every dependency answers
//	  - The probes are reachable without a token
//	  - Readiness fails while Postgres is unavailable
//	  - Readiness fails while SQS is unavailable
//	  - Boot fails loudly when the identity provider is unreachable

type readiness struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func probe(t *testing.T, path string) (int, readiness) {
	t.Helper()

	res := stack.Request(t, http.MethodGet, path, "", nil)
	status := res.StatusCode
	return status, core.Decode[readiness](t, res)
}

// Scenario: The application starts and stops releasing every resource
//
//	When an instance of the worker runtime starts and its recurring task ticks
//	And the instance is stopped
//	Then the shutdown returns without error
//	And the recurring task stops ticking
//	And the database pool is closed
func TestTheApplicationStartsAndStopsReleasingEveryResource(t *testing.T) {
	instance := stack.StartProbe(t)
	deadline := time.Now().Add(5 * time.Second)
	for instance.Ticks() < 3 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if instance.Ticks() < 3 || !instance.PoolOpen() {
		t.Fatalf("the instance did not come up: %d ticks, pool open %v", instance.Ticks(), instance.PoolOpen())
	}

	if err := instance.Stop(); err != nil {
		t.Fatalf("the shutdown failed: %v", err)
	}

	atStop := instance.Ticks()
	time.Sleep(400 * time.Millisecond)
	if instance.Ticks() != atStop {
		t.Fatalf("the recurring task kept running after the shutdown: %d ticks became %d", atStop, instance.Ticks())
	}
	if instance.PoolOpen() {
		t.Fatal("the database pool is still open after the shutdown")
	}
}

// Scenario: Readiness answers ok when every dependency answers
//
//	When /health/ready is requested with no token
//	Then it answers 200 and names each dependency it checked
func TestReadinessAnswersOkWhenEveryDependencyAnswers(t *testing.T) {
	status, got := probe(t, "/health/ready")

	if status != http.StatusOK || got.Status != "ok" || got.Checks["postgres"] != "ok" || got.Checks["sqs"] != "ok" {
		t.Fatalf("readiness = %d %+v", status, got)
	}
}

// Scenario: The probes are reachable without a token
//
//	When /health, /health/live and /health/ready are requested with no token
//	Then each answers 200
func TestTheProbesAreReachableWithoutAToken(t *testing.T) {
	for _, path := range []string{"/health", "/health/live", "/health/ready"} {
		core.RequireStatus(t, stack.Request(t, http.MethodGet, path, "", nil), http.StatusOK)
	}
}

// Scenario: Readiness fails while Postgres is unavailable
//
//	Given Postgres is down
//	When /health/ready is requested
//	Then it answers 503 naming postgres, while /health/live still answers 200
//	And once Postgres is back it answers 200 again
func TestReadinessFailsWhilePostgresIsUnavailable(t *testing.T) {
	requireReadinessFailsWhileDown(t, "postgres", "sqs")
}

// Scenario: Readiness fails while SQS is unavailable
//
//	Given the queue is unreachable
//	When /health/ready is requested
//	Then it answers 503 naming sqs, while /health/live still answers 200
func TestReadinessFailsWhileSQSIsUnavailable(t *testing.T) {
	requireReadinessFailsWhileDown(t, "sqs", "postgres")
}

func requireReadinessFailsWhileDown(t *testing.T, down, healthy string) {
	t.Helper()

	stack.Faults.SetDependencyDown(down, true)
	t.Cleanup(func() { stack.Faults.SetDependencyDown(down, false) })

	status, got := probe(t, "/health/ready")
	if status != http.StatusServiceUnavailable || got.Status != "unavailable" ||
		got.Checks[down] != "unavailable" || got.Checks[healthy] != "ok" {
		t.Fatalf("readiness while %s is down = %d %+v", down, status, got)
	}
	// Alive is not ready: a process that cannot reach its database is taken out of rotation, not
	// killed.
	if live, _ := probe(t, "/health/live"); live != http.StatusOK {
		t.Fatalf("liveness answered %d while only %s was down", live, down)
	}

	stack.Faults.SetDependencyDown(down, false)
	if status, got := probe(t, "/health/ready"); status != http.StatusOK || got.Status != "ok" {
		t.Fatalf("readiness after %s came back = %d %+v", down, status, got)
	}
}

// Scenario: Boot fails loudly when the identity provider is unreachable
//
//	Given the identity provider cannot be reached
//	When the server starts
//	Then the start fails naming the provider once its retries are spent, instead of serving
//	unauthenticated traffic
func TestBootFailsLoudlyWhenTheIdentityProviderIsUnreachable(t *testing.T) {
	err := stack.StartWithUnreachableIdP(t, 60*time.Second)

	if err == nil {
		t.Fatal("the server started although it could not load the keys of the realm")
	}
	if !strings.Contains(strings.ToLower(err.Error()), "keycloak") {
		t.Fatalf("the failure does not say what failed: %v", err)
	}
}
