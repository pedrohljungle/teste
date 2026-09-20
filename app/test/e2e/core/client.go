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

// Request calls the server. An empty bearer sends no Authorization header.
func (s *Stack) Request(t *testing.T, method, path, bearer string, body any) *http.Response {
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

// UniqueTitle keeps tests from colliding on a shared database that is never reset between
// them.
func UniqueTitle(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
