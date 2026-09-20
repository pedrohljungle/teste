package health

import (
	"net/http"

	"github.com/labstack/echo/v4"

	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
)

// Handler serves the liveness route.
type Handler struct {
	app appinfo.App
}

// NewHandler builds the handler.
func NewHandler(app appinfo.App) *Handler {
	return &Handler{app: app}
}

// ServerRoutes registers the probe. It takes no middleware: the probe is deliberately public,
// because one that depends on the IDP turns a Keycloak outage into every task being recycled.
func ServerRoutes(e *echo.Echo, h *Handler) {
	e.GET("/health", h.Check)
}

// Status is what the probe answers.
type Status struct {
	Status  string `json:"status" example:"ok"`
	Service string `json:"service" example:"pedro-test-server"`
	Role    string `json:"role" example:"server"`
	Version string `json:"version" example:"1.4.0"`
}

// Check reports liveness along with which application and version is answering.
//
//	@Summary		Liveness
//	@Description	Aberta de propósito, para um orquestrador sem token conseguir perguntar. Diz também qual aplicação e versão respondeu.
//	@Tags			operations
//	@Produce		json
//	@Success		200	{object}	Status
//	@Router			/health [get]
func (h *Handler) Check(c echo.Context) error {
	return c.JSON(http.StatusOK, Status{
		Status:  "ok",
		Service: h.app.Name,
		Role:    string(h.app.Role),
		Version: h.app.Version,
	})
}
