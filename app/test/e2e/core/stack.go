//go:build e2e

package core

import (
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"time"

	"github.com/go-playground/validator/v10"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/handlers"
	"github.com/estrategiahq/pedro-test/app/src/handlers/health"
	"github.com/estrategiahq/pedro-test/app/src/handlers/identity"
	wageringhandler "github.com/estrategiahq/pedro-test/app/src/handlers/wagering"
	wallethandler "github.com/estrategiahq/pedro-test/app/src/handlers/wallet"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/cronjob"
	"github.com/estrategiahq/pedro-test/app/src/libs/jobrunner"
	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
	"github.com/estrategiahq/pedro-test/app/src/repositories/queue"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Stack is the running application under test: the server and the worker in one process,
// against the real containers.
//
// Running both roles together is what lets a single test follow a request from the HTTP call
// to the row the worker updated. In production they are separate binaries, but they compose
// from the same modules and register through the same ServerRoutes and PrepareWorker functions
// this file calls, so what the suite drives is the real registration. Only the process
// lifecycle around it is the suite's.
type Stack struct {
	// BaseURL is where the server answers.
	BaseURL string
	// Repos are the adapters of the running application, for scenarios that exercise storage
	// directly instead of through a route.
	Repos *Repositories
	// KeycloakURL is the realm root, for fetching tokens.
	KeycloakURL string
	// Faults injects the failures a scenario cannot provoke from outside, at the ports of the
	// outbox.
	Faults *Faults

	queueURL    string
	dlqURL      string
	awsEndpoint string
	sink        *sink
	deadLetters *sink
	poolOnce    sync.Once
	pool        *pgxpool.Pool
	poolErr     error
	app         *fx.App
	infra       *infra
}

// Repositories are the storage contracts as the running application wired them: the real
// adapters, against the real database.
type Repositories struct {
	UnitOfWork persistenceiface.UnitOfWork
	Wallets    walletiface.Repository
	Wagering   wageringiface.Repository
	Outbox     outboxiface.Repository
}

// Start brings up the containers, applies the migrations and boots the application. The
// containers take close to a minute, so a suite pays for this once, in TestMain.
func Start(ctx context.Context) (*Stack, error) {
	in, err := startInfra(ctx)
	if err != nil {
		return nil, err
	}

	if err := migrate(in.databaseURL); err != nil {
		in.terminate(context.Background())
		return nil, err
	}

	stack, err := boot(ctx, in)
	if err != nil {
		in.terminate(context.Background())
		return nil, err
	}
	return stack, nil
}

// Stop shuts the application down and terminates the containers.
func (s *Stack) Stop(ctx context.Context) error {
	s.sink.stop()
	s.deadLetters.stop()
	err := s.app.Stop(ctx)
	if s.pool != nil {
		s.pool.Close()
	}
	s.infra.terminate(context.Background())
	return err
}

func boot(ctx context.Context, in *infra) (*Stack, error) {
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("pick a port: %w", err)
	}

	env := map[string]string{
		"APP_ENV":                "development",
		"PORT":                   port,
		"DATABASE_URL":           in.databaseURL,
		"REDIS_URL":              in.redisURL,
		"KEYCLOAK_ISSUER":        in.keycloakURL + "/realms/pedro-test",
		"KEYCLOAK_AUDIENCE":      "pedro-test-api",
		"KEYCLOAK_CLIENT_ID":     "pedro-test-worker",
		"KEYCLOAK_CLIENT_SECRET": "worker-secret-local",
		"AWS_REGION":             "us-east-1",
		"AWS_ENDPOINT_URL":       in.awsEndpoint,
		"AWS_ACCESS_KEY_ID":      "test",
		"AWS_SECRET_ACCESS_KEY":  "test",
		"SQS_QUEUE_URL":          in.queueURL,
		"SQS_DLQ_URL":            in.dlqURL,
		"SQS_EVENTS_QUEUE_URL":   in.eventsURL,
		"WORKER_POLL_TIMEOUT":    "2s",
		// Short on purpose: a redelivery scenario waits for this to expire, and ten seconds
		// of waiting per test is how a suite stops being run.
		"WORKER_VISIBILITY_TIMEOUT": "2",
		"WORKER_CONCURRENCY":        "2",
		// The outbox publisher is fast and impatient here, and its lease short, so a scenario about
		// a dead publisher or a failing broker waits seconds and not a minute.
		"OUTBOX_POLL_INTERVAL": "200ms",
		"OUTBOX_LEASE":         "3s",
		"OUTBOX_BACKOFF_BASE":  "400ms",
		"OUTBOX_BACKOFF_MAX":   "2s",
		// A pending reference is looked for again quickly, and gives up after a few seconds, so a
		// scenario about a reversal that arrives early or never waits seconds and not a day.
		"REFERENCE_POLL_INTERVAL": "200ms",
		"REFERENCE_BACKOFF_BASE":  "300ms",
		"REFERENCE_BACKOFF_MAX":   "1s",
		"REFERENCE_TTL":           "5s",
		"REFERENCE_MAX_ATTEMPTS":  "200",
		// Telemetry off: the suite asserts on behaviour, and an unreachable collector would
		// only add noise and startup time.
		"OTEL_EXPORTER_OTLP_ENDPOINT": "",
		// Documentation on, so the suite can assert the document describes what is served.
		"DOCS_ENABLED": "true",
	}
	for key, value := range env {
		if err := os.Setenv(key, value); err != nil {
			return nil, err
		}
	}

	repos := &Repositories{}
	faults := newFaults()

	app := fx.New(
		fx.Supply(appinfo.App{Name: "pedro-test-e2e", Role: appinfo.RoleServer, Version: "test"}),

		bootstrap.Core,
		auth.ServerModule,
		auth.WorkerModule,
		middleware.Module,
		handlers.Module,
		jobrunner.Module,
		cronjob.Module,
		faults.options(),

		fx.Provide(newEcho),
		fx.Invoke(serverRoutes),
		fx.Invoke(runServer),

		fx.Populate(&repos.UnitOfWork, &repos.Wallets, &repos.Wagering, &repos.Outbox),

		fx.Invoke(prepareWorkers),
		fx.Invoke(registerCronjobs),
		fx.Invoke(jobrunner.Run),
		fx.Invoke(cronjob.Run),

		fx.NopLogger,

		fx.StartTimeout(90*time.Second),
		fx.StopTimeout(30*time.Second),
	)

	if err := app.Start(ctx); err != nil {
		return nil, fmt.Errorf("start the application: %w", err)
	}

	events, err := startSink(ctx, in.awsEndpoint, in.eventsURL)
	if err != nil {
		_ = app.Stop(ctx)
		return nil, fmt.Errorf("start the events sink: %w", err)
	}
	deadLetters, err := startSink(ctx, in.awsEndpoint, in.dlqURL)
	if err != nil {
		events.stop()
		_ = app.Stop(ctx)
		return nil, fmt.Errorf("start the dead letter sink: %w", err)
	}

	return &Stack{
		Repos:       repos,
		Faults:      faults,
		sink:        events,
		deadLetters: deadLetters,
		BaseURL:     "http://127.0.0.1:" + port,
		KeycloakURL: in.keycloakURL,
		queueURL:    in.queueURL,
		dlqURL:      in.dlqURL,
		awsEndpoint: in.awsEndpoint,
		app:         app,
		infra:       in,
	}, nil
}

type requestValidator struct{ validate *validator.Validate }

func (v *requestValidator) Validate(i any) error { return v.validate.Struct(i) }

func newEcho(telemetry *middleware.Telemetry) *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Validator = &requestValidator{validate: validator.New()}
	e.Use(echomiddleware.Recover())
	e.Use(echomiddleware.RequestID())
	e.Use(telemetry.TraceRequest)
	return e
}

type routeParams struct {
	fx.In

	Echo     *echo.Echo
	Config   config.Config
	Auth     *middleware.Auth
	Health   *health.Handler
	Identity *identity.Handler
	Wallet   *wallethandler.Handler
	Wagering *wageringhandler.Handler
}

// serverRoutes mirrors cmd/server: the same ServerRoutes functions, the same middlewares on the
// same routes. The documentation routes are registered here too, so the suite can assert the
// document describes what is served.
func serverRoutes(p routeParams) {
	health.ServerRoutes(p.Echo, p.Health)
	identity.ServerRoutes(p.Echo, p.Identity, p.Auth.RequireAuthentication)
	wallethandler.ServerRoutes(p.Echo, p.Wallet,
		p.Auth.RequireAuthentication, p.Auth.RequireRealmRole(structs.RoleInternalService))
	wageringhandler.ServerRoutes(p.Echo, p.Wagering,
		p.Auth.RequireAuthentication, p.Auth.RequireRealmRole(structs.RoleProvider))

	docsRoutes(p.Echo, p.Config)
}

func runServer(lc fx.Lifecycle, e *echo.Echo, cfg config.Config) {
	lc.Append(fx.Hook{
		OnStart: func(context.Context) error {
			go func() { _ = e.Start(cfg.Addr()) }()
			return waitForPort(cfg.Addr())
		},
		OnStop: func(ctx context.Context) error { return e.Shutdown(ctx) },
	})
}

// prepareWorkers mirrors cmd/worker: the wagering messages are registered on the job runner through
// the same PrepareWorker, so what the suite consumes is the real handler.
func prepareWorkers(runner *jobrunner.Runner, source *queue.SQS, handler *wageringhandler.JobHandler) {
	wageringhandler.PrepareWorker(runner, source, handler)
}

func freePort() (string, error) {
	var lc net.ListenConfig
	listener, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return "", err
	}
	defer func() { _ = listener.Close() }()

	_, port, err := net.SplitHostPort(listener.Addr().String())
	return port, err
}

// waitForPort keeps a test from racing the server: Echo.Start is asynchronous, and the first
// request would otherwise land before the listener exists.
func waitForPort(addr string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	dialer := &net.Dialer{Timeout: time.Second}
	for {
		conn, err := dialer.DialContext(ctx, "tcp", "127.0.0.1"+addr)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("server did not start listening on %s", addr)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
