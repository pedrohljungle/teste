//go:build e2e

package core

import (
	"encoding/json"
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/swaggo/swag"

	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/estrategiahq/pedro-test/app/docs"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
)

// docsRoutes mirrors the documentation wiring of cmd/server, including the rewrite of the
// token URL. Duplicating it is what lets the suite assert that the document a caller receives
// points at the realm that caller can reach — the bug this guards against is invisible until
// someone opens the page from outside the network.
func docsRoutes(e *echo.Echo, cfg config.Config) {
	if !cfg.DocsEnabled {
		return
	}

	e.GET("/swagger/doc.json", func(c echo.Context) error {
		raw, err := swag.ReadDoc()
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "could not render the document")
		}

		var document map[string]any
		if err := json.Unmarshal([]byte(raw), &document); err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "could not render the document")
		}
		if definitions, ok := document["securityDefinitions"].(map[string]any); ok {
			if scheme, ok := definitions[middleware.SecurityScheme].(map[string]any); ok {
				scheme["tokenUrl"] = cfg.Keycloak.PublicTokenURL()
			}
		}
		return c.JSON(http.StatusOK, document)
	})

	e.GET("/swagger/*", echoSwagger.EchoWrapHandler(echoSwagger.URL("/swagger/doc.json")))
	e.GET("/docs", func(c echo.Context) error {
		return c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")
	})
}
