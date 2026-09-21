package health

import (
	"context"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"

	healthiface "github.com/estrategiahq/pedro-test/app/src/interfaces/health"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// checkTimeout bounds each dependency check. A probe that waits on a hung database is a probe that
// times out, which the orchestrator reads as "not ready" for the wrong reason and after too long.
const checkTimeout = 2 * time.Second

// Handler serves the liveness and readiness routes.
type Handler struct {
	app      appinfo.App
	obs      *observability.Observer
	checkers []healthiface.Checker
}

// NewHandler builds the handler. The checkers are the dependencies the application needs to be
// useful, gathered by the module.
func NewHandler(app appinfo.App, obs *observability.Observer, checkers []healthiface.Checker) *Handler {
	return &Handler{app: app, obs: obs, checkers: checkers}
}

// ServerRoutes registers the probes. They take no middleware: they are deliberately public, because
// a probe that depends on the IDP turns a Keycloak outage into every task being recycled.
//
// /health is the liveness probe under the name the infrastructure already points at.
func ServerRoutes(e *echo.Echo, h *Handler) {
	e.GET("/health", h.Live)
	e.GET("/health/live", h.Live)
	e.GET("/health/ready", h.Ready)
}

// Status is what the liveness probe answers.
type Status struct {
	Status  string `json:"status" example:"ok"`
	Service string `json:"service" example:"pedro-test-server"`
	Role    string `json:"role" example:"server"`
	Version string `json:"version" example:"1.4.0"`
}

// Readiness is what the readiness probe answers: one entry per dependency.
type Readiness struct {
	Status string            `json:"status" enums:"ok,unavailable" example:"ok"`
	Checks map[string]string `json:"checks"`
}

// Live reports that the process is up, and which application and version is answering.
//
//	@Summary		Liveness
//	@Description	O processo está de pé. Não olha para nenhuma dependência: um processo vivo que não alcança o banco deve sair de rotação, não ser morto. Aberta de propósito, para um orquestrador sem token conseguir perguntar. `/health` é o mesmo probe, com o nome que a infraestrutura já usa.
//	@Tags			operations
//	@Produce		json
//	@Success		200	{object}	Status
//	@Router			/health/live [get]
//	@Router			/health [get]
func (h *Handler) Live(c echo.Context) error {
	return c.JSON(http.StatusOK, Status{
		Status:  "ok",
		Service: h.app.Name,
		Role:    string(h.app.Role),
		Version: h.app.Version,
	})
}

// Ready reports whether the dependencies answer.
//
//	@Summary		Readiness
//	@Description	Pergunta ao PostgreSQL e ao SQS. 200 quando todos respondem, 503 quando algum não, com o estado de cada um. É o probe que decide se a instância recebe tráfego. Aberta de propósito.
//	@Tags			operations
//	@Produce		json
//	@Success		200	{object}	Readiness
//	@Failure		503	{object}	Readiness
//	@Router			/health/ready [get]
func (h *Handler) Ready(c echo.Context) error {
	ctx := c.Request().Context()
	report := Readiness{Status: "ok", Checks: make(map[string]string, len(h.checkers))}

	for _, checker := range h.checkers {
		if err := h.check(ctx, checker); err != nil {
			report.Status = "unavailable"
			report.Checks[checker.Name()] = "unavailable"
			// The detail goes to the log, which the operator reads, and not to the response, which a
			// stranger can: what the dependency is called is all a probe has to say.
			h.obs.Warn(ctx, "readiness check failed",
				observability.String("dependency", checker.Name()),
				observability.String("error", err.Error()))
			continue
		}
		report.Checks[checker.Name()] = "ok"
	}

	if report.Status != "ok" {
		return c.JSON(http.StatusServiceUnavailable, report)
	}
	return c.JSON(http.StatusOK, report)
}

func (h *Handler) check(ctx context.Context, checker healthiface.Checker) error {
	ctx, cancel := context.WithTimeout(ctx, checkTimeout)
	defer cancel()
	return checker.Check(ctx)
}
