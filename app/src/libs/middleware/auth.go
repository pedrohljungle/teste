package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// SecurityScheme is the name the Swagger document gives the Keycloak scheme, and the one each
// operation refers to with @Security. It is what puts the Authorize button in the docs page.
const SecurityScheme = "OAuth2Password"

type principalKeyType struct{}

// principalKey is a private type so nothing outside this package can overwrite what the
// middleware stored, by accident or otherwise.
var principalKey = principalKeyType{}

// Auth turns a Bearer JWT into an authenticated caller. It depends on the port, so swapping
// the IDP or faking it does not touch this file.
type Auth struct {
	verifier auth.TokenVerifier
}

// NewAuth builds the middlewares.
func NewAuth(verifier auth.TokenVerifier) *Auth {
	return &Auth{verifier: verifier}
}

// RequireAuthentication rejects a request that does not carry a valid JWT and stores the
// caller for the handler. Routes declare it one by one, so reading the route tells you whether
// it is protected.
func (m *Auth) RequireAuthentication(next echo.HandlerFunc) echo.HandlerFunc {
	return func(c echo.Context) error {
		raw, ok := bearerToken(c.Request().Header.Get(echo.HeaderAuthorization))
		if !ok {
			return echo.NewHTTPError(http.StatusUnauthorized, "missing token")
		}

		principal, err := m.verifier.Verify(c.Request().Context(), raw)
		if err != nil {
			// A verifier that has not loaded the realm metadata yet is a server problem,
			// not a caller problem, and 503 is what tells a client to retry.
			if errors.Is(err, auth.ErrVerifierNotReady) {
				return echo.NewHTTPError(http.StatusServiceUnavailable, "authentication unavailable")
			}
			// The reason was already logged with the real cause. Telling the caller why the
			// token failed hands information to whoever is guessing.
			return echo.NewHTTPError(http.StatusUnauthorized, "invalid token")
		}

		// The principal goes into the REQUEST context, not into the Echo context: everything
		// below the handler takes a context.Context, and a service that needed the caller
		// would otherwise have to know what web framework is on top.
		req := c.Request()
		c.SetRequest(req.WithContext(context.WithValue(req.Context(), principalKey, principal)))
		return next(c)
	}
}

// RequireRealmRole rejects an authenticated caller that does not hold the given realm role.
// It runs after RequireAuthentication and is edge authorization only: a rule that has to look
// at the data, such as "only the owner may close a task", belongs to a service.
func (m *Auth) RequireRealmRole(role string) echo.MiddlewareFunc {
	return func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c echo.Context) error {
			principal, ok := AuthenticatedPrincipal(c.Request().Context())
			if !ok || !principal.HasRole(role) {
				return echo.NewHTTPError(http.StatusForbidden, "forbidden")
			}
			return next(c)
		}
	}
}

// AuthenticatedPrincipal returns the caller stored by RequireAuthentication. The second value
// is false on a route that does not require authentication.
func AuthenticatedPrincipal(ctx context.Context) (structs.Principal, bool) {
	principal, ok := ctx.Value(principalKey).(structs.Principal)
	return principal, ok
}

func bearerToken(header string) (string, bool) {
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", false
	}
	token := strings.TrimSpace(header[len(prefix):])
	return token, token != ""
}
