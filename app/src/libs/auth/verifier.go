package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// ErrInvalidToken is returned for any token the IDP does not vouch for. Callers match it with
// errors.Is instead of inspecting the underlying message.
var ErrInvalidToken = errors.New("invalid token")

// ErrVerifierNotReady is returned when a token arrives before the realm metadata was loaded.
var ErrVerifierNotReady = errors.New("token verifier not ready")

// TokenVerifier is the port used by the HTTP middleware.
type TokenVerifier interface {
	Verify(ctx context.Context, rawToken string) (structs.Principal, error)
}

const (
	discoveryAttempts = 10
	discoveryBackoff  = 2 * time.Second
	discoveryTimeout  = 10 * time.Second
)

// Verifier validates Keycloak access tokens against the realm signing keys.
type Verifier struct {
	cfg  config.Keycloak
	obs  *observability.Observer
	http *http.Client

	mu       sync.RWMutex
	verifier *oidc.IDTokenVerifier
}

// NewVerifier registers the realm discovery on application start, so a misconfigured IDP
// fails the boot instead of failing the first authenticated request.
func NewVerifier(lc fx.Lifecycle, cfg config.Keycloak, obs *observability.Observer) *Verifier {
	v := &Verifier{
		cfg:  cfg,
		obs:  obs,
		http: &http.Client{Timeout: discoveryTimeout},
	}
	lc.Append(fx.Hook{OnStart: v.start})
	return v
}

// start loads the realm metadata and keys, retrying while Keycloak warms up.
func (v *Verifier) start(ctx context.Context) error {
	var lastErr error
	for attempt := 1; attempt <= discoveryAttempts; attempt++ {
		if err := v.load(ctx); err == nil {
			v.obs.Info(ctx, "keycloak realm metadata loaded",
				observability.String("issuer", v.cfg.Issuer),
				observability.Int("attempt", attempt),
			)
			return nil
		} else {
			lastErr = err
			v.obs.Warn(ctx, "keycloak discovery failed, retrying",
				observability.Int("attempt", attempt),
				observability.String("error", err.Error()),
			)
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("keycloak discovery cancelled: %w", ctx.Err())
		case <-time.After(discoveryBackoff):
		}
	}
	return fmt.Errorf("keycloak discovery failed after %d attempts: %w", discoveryAttempts, lastErr)
}

func (v *Verifier) load(ctx context.Context) error {
	// The token iss claim carries the external address, which is often unreachable from
	// inside the network. InsecureIssuerURLContext fetches the metadata from the reachable
	// address while still requiring the document to declare the configured issuer.
	discoveryCtx := oidc.InsecureIssuerURLContext(ctx, v.cfg.Issuer)
	if _, err := oidc.NewProvider(discoveryCtx, v.cfg.DiscoveryURL()); err != nil {
		return fmt.Errorf("load realm metadata: %w", err)
	}

	// The key set is built from the reachable JWKS address rather than from the jwks_uri the
	// metadata advertises, which points at the external address. Fetching it here proves at
	// boot that the keys are reachable and non-empty.
	if err := v.checkKeys(ctx); err != nil {
		return err
	}

	// Background context: the key set outlives the start hook and refreshes on its own.
	keySet := oidc.NewRemoteKeySet(context.Background(), v.cfg.JWKSURL())

	v.mu.Lock()
	v.verifier = oidc.NewVerifier(v.cfg.Issuer, keySet, &oidc.Config{
		ClientID: v.cfg.Audience,
		// Pinning the algorithm closes the "alg: none" and algorithm substitution doors.
		SupportedSigningAlgs: []string{oidc.RS256},
	})
	v.mu.Unlock()
	return nil
}

func (v *Verifier) checkKeys(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, v.cfg.JWKSURL(), nil)
	if err != nil {
		return fmt.Errorf("build jwks request: %w", err)
	}
	res, err := v.http.Do(req)
	if err != nil {
		return fmt.Errorf("fetch jwks: %w", err)
	}
	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("fetch jwks: unexpected status %d", res.StatusCode)
	}

	var document struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := json.NewDecoder(res.Body).Decode(&document); err != nil {
		return fmt.Errorf("decode jwks: %w", err)
	}
	if len(document.Keys) == 0 {
		return errors.New("realm published no signing keys")
	}
	return nil
}

// Verify returns the Principal carried by the token.
func (v *Verifier) Verify(ctx context.Context, rawToken string) (structs.Principal, error) {
	return observability.Trace(ctx, v.obs, observability.LayerGateway, "Keycloak.Verify", func(ctx context.Context) (structs.Principal, error) {
		v.mu.RLock()
		verifier := v.verifier
		v.mu.RUnlock()
		if verifier == nil {
			return structs.Principal{}, ErrVerifierNotReady
		}

		token, err := verifier.Verify(ctx, rawToken)
		if err != nil {
			// Wrapped into a sentinel so the middleware answers 401 with errors.Is instead of
			// matching error strings. The original cause stays in the log.
			return structs.Principal{}, fmt.Errorf("%w: %w", ErrInvalidToken, err)
		}

		var claims keycloakClaims
		if err := token.Claims(&claims); err != nil {
			return structs.Principal{}, fmt.Errorf("read token claims: %w", err)
		}

		return structs.Principal{
			Subject:  token.Subject,
			Username: claims.Username,
			Email:    claims.Email,
			Roles:    claims.roles(),
			// A token without the claim leaves this empty, which is what an internal service or a
			// person looks like: none of them acts for a provider.
			ProviderID: claims.ProviderID,
		}, nil
	})
}

// keycloakClaims mirrors how Keycloak lays out an access token. It is adapter-shaped on
// purpose: the rest of the system only ever sees structs.Principal.
type keycloakClaims struct {
	Username   string `json:"preferred_username"`
	Email      string `json:"email"`
	ProviderID string `json:"provider_id"`

	RealmAccess struct {
		Roles []string `json:"roles"`
	} `json:"realm_access"`

	ResourceAccess map[string]struct {
		Roles []string `json:"roles"`
	} `json:"resource_access"`
}

func (c keycloakClaims) roles() []string {
	roles := append([]string{}, c.RealmAccess.Roles...)
	for _, resource := range c.ResourceAccess {
		roles = append(roles, resource.Roles...)
	}
	return roles
}
