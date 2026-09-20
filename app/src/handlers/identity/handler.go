package identity

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Handler serves the caller identity route.
type Handler struct{}

// NewHandler builds the handler.
func NewHandler() *Handler {
	return &Handler{}
}

// ServerRoutes registers this domain on the HTTP server.
func ServerRoutes(e *echo.Echo, h *Handler, requireAuthentication echo.MiddlewareFunc) {
	e.GET("/me", h.Me, requireAuthentication)
}

// Me returns the principal the authentication middleware stored for this request.
//
//	@Summary		Quem sou eu
//	@Description	Devolve quem chamou, como o realm descreveu: subject, username, e-mail e papéis.
//	@Tags			identity
//	@Produce		json
//	@Success		200	{object}	structs.Principal
//	@Failure		401	{object}	structs.APIError
//	@Security		OAuth2Password
//	@Router			/me [get]
func (h *Handler) Me(c echo.Context) error {
	var principal structs.Principal

	principal, ok := middleware.AuthenticatedPrincipal(c.Request().Context())
	if !ok {
		return echo.NewHTTPError(http.StatusUnauthorized, "not authenticated")
	}
	return c.JSON(http.StatusOK, principal)
}
