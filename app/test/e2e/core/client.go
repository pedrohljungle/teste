//go:build e2e

package core

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

var httpClient = &http.Client{Timeout: 15 * time.Second}

// Token fetches a real access token from Keycloak with the direct grant the realm enables for
// development. Using a real token is the point: it exercises signature verification, the
// audience check and the role mapping, none of which a stubbed principal would.
func (s *Stack) Token(t *testing.T, username, password string) string {
	t.Helper()

	form := url.Values{
		"client_id":  {"pedro-test-api"},
		"grant_type": {"password"},
		"username":   {username},
		"password":   {password},
	}
	endpoint := s.KeycloakURL + "/realms/pedro-test/protocol/openid-connect/token"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("request a token: %v", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("token endpoint answered %d: %s", res.StatusCode, body)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode the token: %v", err)
	}
	return payload.AccessToken
}

// The identities of the realm used by the scenarios: one internal service and two providers, all
// machine accounts that authenticate with client_credentials.
const (
	InternalService = "pedro-test-wallet-service"
	ProviderA       = "provider-a"
	ProviderB       = "provider-b"
	// ShortLived is an internal service whose tokens expire after one second.
	ShortLived = "pedro-test-short-lived"
)

// clientSecrets are the secrets the realm file declares for those identities.
var clientSecrets = map[string]string{
	InternalService: "wallet-service-secret-local",
	ProviderA:       "provider-a-secret-local",
	ProviderB:       "provider-b-secret-local",
	ShortLived:      "short-lived-secret-local",
}

var (
	tokenCacheMu sync.Mutex
	tokenCache   = map[string]cachedToken{}
)

type cachedToken struct {
	value   string
	fetched time.Time
}

// tokenReuse is how long a fetched token is reused. The realm issues them for fifteen minutes,
// and the suite runs in well under one, so reusing them is safe and spares Keycloak a call for
// every request of a scenario that sends dozens.
const tokenReuse = 5 * time.Minute

// ClientToken returns a real access token for a machine identity with client_credentials, the
// way a provider or an internal service authenticates in production. The short lived identity
// is never cached: its whole point is that its token expires.
func (s *Stack) ClientToken(t *testing.T, clientID string) string {
	t.Helper()

	if clientID == ShortLived {
		return s.fetchClientToken(t, clientID)
	}
	tokenCacheMu.Lock()
	defer tokenCacheMu.Unlock()
	if cached, ok := tokenCache[clientID]; ok && time.Since(cached.fetched) < tokenReuse {
		return cached.value
	}
	token := s.fetchClientToken(t, clientID)
	tokenCache[clientID] = cachedToken{value: token, fetched: time.Now()}
	return token
}

func (s *Stack) fetchClientToken(t *testing.T, clientID string) string {
	t.Helper()

	form := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecrets[clientID]},
		"grant_type":    {"client_credentials"},
	}
	endpoint := s.KeycloakURL + "/realms/pedro-test/protocol/openid-connect/token"

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatalf("build the token request: %v", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("request a token for %s: %v", clientID, err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("token endpoint answered %d for %s: %s", res.StatusCode, clientID, body)
	}

	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(res.Body).Decode(&payload); err != nil {
		t.Fatalf("decode the token: %v", err)
	}
	return payload.AccessToken
}

// Request calls the server. An empty bearer sends no Authorization header.
func (s *Stack) Request(t *testing.T, method, path, bearer string, body any) *http.Response {
	t.Helper()
	return s.RequestWithHeaders(t, method, path, bearer, body, nil)
}

// RequestWithHeaders is Request with extra headers, such as an Idempotency-Key or a request id.
func (s *Stack) RequestWithHeaders(t *testing.T, method, path, bearer string, body any, headers map[string]string) *http.Response {
	t.Helper()

	var reader io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode the body: %v", err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(context.Background(), method, s.BaseURL+path, reader)
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}

	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// RawRequest sends a body exactly as given, for the scenarios about what arrives malformed: a
// JSON number where a string belongs cannot be built from a Go value that marshals it as one.
func (s *Stack) RawRequest(t *testing.T, method, path, bearer, rawBody string) *http.Response {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, s.BaseURL+path, strings.NewReader(rawBody))
	if err != nil {
		t.Fatalf("build the request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}

	res, err := httpClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	return res
}

// Decode reads a JSON response body into T and closes it.
func Decode[T any](t *testing.T, res *http.Response) T {
	t.Helper()
	defer func() { _ = res.Body.Close() }()

	var value T
	if err := json.NewDecoder(res.Body).Decode(&value); err != nil {
		t.Fatalf("decode the response: %v", err)
	}
	return value
}

// RequireStatus asserts the status code and closes the body, printing the body on a mismatch
// because the message is usually what explains it.
func RequireStatus(t *testing.T, res *http.Response, want int) {
	t.Helper()
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != want {
		body, _ := io.ReadAll(res.Body)
		t.Fatalf("expected status %d, got %d: %s", want, res.StatusCode, body)
	}
}

// KeepStatus asserts the status code like RequireStatus but hands the response back with its body
// still open, for a scenario that goes on to read it.
func KeepStatus(t *testing.T, res *http.Response, want int) *http.Response {
	t.Helper()

	if res.StatusCode != want {
		body, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		t.Fatalf("expected status %d, got %d: %s", want, res.StatusCode, body)
	}
	return res
}

// UniqueTitle keeps tests from colliding on a shared database that is never reset between
// them.
func UniqueTitle(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
