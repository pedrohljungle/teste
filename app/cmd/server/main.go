// Command server is the HTTP entrypoint.
//
// Everything about serving HTTP lives here: the Echo instance, the middleware order, the route
// registration, the documentation and the server lifecycle. The domains expose ServerRoutes and
// this file calls them, so adding a domain is one line here and a file there — and there is no
// runtime package in between hiding what the process actually serves.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"github.com/swaggo/swag"
	"go.opentelemetry.io/contrib/instrumentation/github.com/labstack/echo/otelecho"
	"go.uber.org/fx"

	echoSwagger "github.com/swaggo/echo-swagger"

	_ "github.com/estrategiahq/pedro-test/app/docs"
	"github.com/estrategiahq/pedro-test/app/src/handlers"
	"github.com/estrategiahq/pedro-test/app/src/handlers/health"
	"github.com/estrategiahq/pedro-test/app/src/handlers/identity"
	wallethandler "github.com/estrategiahq/pedro-test/app/src/handlers/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// version is injected at build time with -ldflags and becomes service.version.
var version = "dev"

//	@title			pedro-test
//	@version		1.0
//	@description	Carteiras de jogadores e operações de provedores de jogos: apostas, prêmios, derrotas, estornos e reversões.
//	@BasePath		/
//
//	@securityDefinitions.oauth2.password	OAuth2Password
//	@tokenUrl								http://localhost:8080/realms/pedro-test/protocol/openid-connect/token
//	@scope.openid							Acesso à API
//
// The tokenUrl above is a placeholder: swag bakes it at generation time, and the realm address
// differs per environment. It is rewritten from the configuration when the document is served
// (see docsRoutes), so the Authorize button points at the realm the caller can actually reach.
func main() {
	fx.New(options()...).Run()
}

// options is separate from main so the wiring can be exercised without running the process.
func options() []fx.Option {
	return []fx.Option{
		fx.Supply(appinfo.App{
			Name:    "pedro-test-server",
			Role:    appinfo.RoleServer,
			Version: version,
		}),

		bootstrap.Core,

		auth.ServerModule,
		middleware.Module,
		handlers.Module,

		fx.Provide(newEcho),
		fx.Invoke(serverRoutes),
		fx.Invoke(run),

		fx.StartTimeout(60 * time.Second),
		fx.StopTimeout(30 * time.Second),
	}
}

type requestValidator struct {
	validate *validator.Validate
}

func (v *requestValidator) Validate(i any) error {
	return v.validate.Struct(i)
}

// newEcho builds the router with the middlewares that run on every request.
//
// Order matters: Recover must be outermost so a panic still becomes a response; otelecho
// creates the server span from the incoming traceparent; the telemetry middleware then opens
// the handler layer span inside it. Anything that is a decision of one route is declared on
// that route, not here.
func newEcho(app appinfo.App, telemetry *middleware.Telemetry) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Validator = &requestValidator{validate: validator.New()}

	e.Use(echomiddleware.Recover())
	e.Use(echomiddleware.RequestID())
	e.Use(otelecho.Middleware(app.Name))
	e.Use(telemetry.TraceRequest)

	return e
}

// routeParams groups the handlers. fx.In keeps adding a domain to one line instead of a change
// to the signature and to every caller.
type routeParams struct {
	fx.In

	Echo     *echo.Echo
	Config   config.Config
	Auth     *middleware.Auth
	Health   *health.Handler
	Identity *identity.Handler
	Wallet   *wallethandler.Handler
}

// serverRoutes is the map of what this process serves. Each domain registers itself and names,
// on each route, the middlewares that route requires.
//
// health and identity are the two routes any service has regardless of what it does; the wallet
// is the first domain, and it is reserved to the internal service role.
func serverRoutes(p routeParams) {
	health.ServerRoutes(p.Echo, p.Health)
	identity.ServerRoutes(p.Echo, p.Identity, p.Auth.RequireAuthentication)
	wallethandler.ServerRoutes(p.Echo, p.Wallet,
		p.Auth.RequireAuthentication, p.Auth.RequireRealmRole(structs.RoleInternalService))

	// A domain registers itself in one line:
	//
	//	<domain>.ServerRoutes(p.Echo, p.<Domain>, p.Auth.RequireAuthentication)
	//
	// where ServerRoutes lives in handlers/<domain> and names, on each route, the middlewares
	// that route requires.

	docsRoutes(p.Echo, p.Config)
}

// docsRoutes serves the Swagger document and its UI.
//
// Off by default: with the flag down nothing is registered, so the answer is 404 rather than a
// 403 that tells a stranger the page exists. The most reliable control for an optional surface
// is the surface not being there.
func docsRoutes(e *echo.Echo, cfg config.Config) {
	if !cfg.DocsEnabled {
		return
	}

	e.GET("/swagger/doc.json", func(c echo.Context) error {
		document, err := swaggerDocument(cfg)
		if err != nil {
			return echo.NewHTTPError(http.StatusInternalServerError, "could not render the document")
		}
		return c.JSONBlob(http.StatusOK, document)
	})

	e.GET("/swagger/*", echoSwagger.EchoWrapHandler(
		echoSwagger.URL("/swagger/doc.json"),
		// Prefills the Authorize dialog, so getting a token is username and password and
		// nothing else to look up.
		echoSwagger.OAuth(&echoSwagger.OAuthConfig{
			ClientId: cfg.Keycloak.PublicClientID(),
			AppName:  "pedro-test",
		}),
		echoSwagger.PersistAuthorization(true),
	))

	// /docs is the address people try first.
	e.GET("/docs", func(c echo.Context) error {
		return c.Redirect(http.StatusMovedPermanently, "/swagger/index.html")
	})
}

// swaggerDocument rewrites the token URL of the generated document with the one from the
// configuration.
//
// swag bakes the URL at generation time, and the application talks to Keycloak by its internal
// address while the browser talks to the external one. Publishing the generated value would
// break the Authorize button for everyone outside the network.
func swaggerDocument(cfg config.Config) ([]byte, error) {
	raw, err := swag.ReadDoc()
	if err != nil {
		return nil, err
	}

	var document map[string]any
	if err := json.Unmarshal([]byte(raw), &document); err != nil {
		return nil, err
	}

	definitions, ok := document["securityDefinitions"].(map[string]any)
	if ok {
		if scheme, ok := definitions[middleware.SecurityScheme].(map[string]any); ok {
			scheme["tokenUrl"] = cfg.Keycloak.PublicTokenURL()
		}
	}
	return json.Marshal(document)
}

// run hands the server to the fx lifecycle, so it starts after the pools and stops before them.
func run(lc fx.Lifecycle, e *echo.Echo, cfg config.Config, obs *observability.Observer) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := e.Start(cfg.Addr()); err != nil && !errors.Is(err, http.ErrServerClosed) {
					obs.Error(ctx, err, "http server stopped unexpectedly")
				}
			}()
			obs.Info(ctx, "http server started",
				observability.String("addr", cfg.Addr()),
				observability.Bool("docs", cfg.DocsEnabled),
			)
			return nil
		},
		OnStop: func(ctx context.Context) error {
			obs.Info(ctx, "shutting down http server")
			return e.Shutdown(ctx)
		},
	})
}
