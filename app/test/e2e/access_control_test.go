//go:build e2e

package e2e

import (
	"net/http"
	"testing"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: access control
//
//   As the operator of the API
//   I want every protected route to require a valid token, and the admin routes a realm role
//   So that the border is decided by the IDP and not by the application
//
//   The tokens here are real: issued by Keycloak, signed by the realm keys, verified against
//   the JWKS the server loaded at boot. That is what makes these scenarios worth running — a
//   stubbed principal would prove the middleware calls a function, not that the token is good.
//
//   Scenarios:
//     - a request without a token is refused
//     - a request with a forged token is refused
//     - the probe is public
//     - the identity route reflects the realm

// Scenario: a request without a token is refused
//
//	Given no credentials
//	When a protected route is requested
//	Then the server answers 401
func TestRequestWithoutATokenIsRefused(t *testing.T) {
	core.RequireStatus(t, stack.Request(t, http.MethodGet, "/me", "", nil), http.StatusUnauthorized)
}

// Scenario: a request with a forged token is refused
//
//	Given a bearer token the realm never issued
//	When a protected route is requested
//	Then the server answers 401
func TestRequestWithAForgedTokenIsRefused(t *testing.T) {
	core.RequireStatus(t, stack.Request(t, http.MethodGet, "/me", "not.a.token", nil), http.StatusUnauthorized)
}

// Scenario: the probe is public
//
//	Given no credentials
//	When the health route is requested
//	Then the server answers 200
//
//	Deliberate: a probe that needs a token turns an IDP outage into every task being recycled.
func TestProbeIsPublic(t *testing.T) {
	core.RequireStatus(t, stack.Request(t, http.MethodGet, "/health", "", nil), http.StatusOK)
}

// Scenario: the identity route reflects the realm
//
//	Given pedro, authenticated
//	When he asks who he is
//	Then the username comes from the realm
//	And the subject is filled
//	And the app-admin role is listed
func TestIdentityRouteReflectsTheRealm(t *testing.T) {
	res := stack.Request(t, http.MethodGet, "/me", stack.Token(t, "pedro", "pedro"), nil)

	principal := core.Decode[struct {
		Subject  string   `json:"sub"`
		Username string   `json:"username"`
		Email    string   `json:"email"`
		Roles    []string `json:"roles"`
	}](t, res)

	if principal.Username != "pedro" {
		t.Fatalf("expected username pedro, got %q", principal.Username)
	}
	if principal.Subject == "" {
		t.Fatal("expected the subject from the realm")
	}
	if !contains(principal.Roles, "app-admin") {
		t.Fatalf("expected the app-admin role, got %v", principal.Roles)
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
