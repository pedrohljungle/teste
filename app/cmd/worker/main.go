// Command worker is the queue entrypoint.
//
// It shares bootstrap.Core with the server — same config, same telemetry, same repositories,
// same services — and differs in what it dispatches to: each domain exposes PrepareWorker and
// this file calls it, mirroring how the server calls ServerRoutes.
//
// It also serves a probe of its own: an orchestrator needs to know whether the consumer is
// alive, and a worker with no HTTP surface has no way to say so.
package main

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/labstack/echo/v4"
	echomiddleware "github.com/labstack/echo/v4/middleware"
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/handlers"
	"github.com/estrategiahq/pedro-test/app/src/handlers/health"
	outboxhandler "github.com/estrategiahq/pedro-test/app/src/handlers/outbox"
	referencehandler "github.com/estrategiahq/pedro-test/app/src/handlers/reference"
	wageringhandler "github.com/estrategiahq/pedro-test/app/src/handlers/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/cronjob"
	"github.com/estrategiahq/pedro-test/app/src/libs/jobrunner"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/repositories/queue"
)

var version = "dev"

func main() {
	fx.New(options()...).Run()
}

// options is separate from main so the wiring can be exercised without running the process.
func options() []fx.Option {
	return []fx.Option{
		fx.Supply(appinfo.App{
			Name:    "pedro-test-worker",
			Role:    appinfo.RoleWorker,
			Version: version,
		}),

		bootstrap.Core,

		auth.WorkerModule,
		handlers.Module,
		jobrunner.Module,
		cronjob.Module,

		fx.Invoke(prepareWorkers),
		fx.Invoke(jobrunner.Run),
		fx.Invoke(cronjob.Run),

		fx.Provide(newProbeEcho),
		fx.Invoke(probeRoutes),
		fx.Invoke(runProbe),

		fx.StartTimeout(60 * time.Second),
		// Stopping must fit the longest message in flight, not the drain of an HTTP
		// connection.
		fx.StopTimeout(60 * time.Second),
	}
}

// workerParams groups what the consumers need. Adding a domain is one line here and a
// PrepareWorker there.
type workerParams struct {
	fx.In

	Runner    *jobrunner.Runner
	Queue     *queue.SQS
	Account   *auth.ServiceAccount
	Cronjobs  *cronjob.Runner
	Outbox    *outboxhandler.CronjobHandler
	Reference *referencehandler.CronjobHandler
	Wagering  *wageringhandler.JobHandler
}

// prepareWorkers is the map of what this process does. It mirrors serverRoutes in cmd/server:
// each domain registers itself on the runtime it needs.
//
// A queue consumer registers with PrepareWorker, on the job runner: the wagering messages are the
// first. A recurring task registers
// with PrepareCronjob, on the cronjob runner. The outbox publisher and the resolver of pending
// references are of the second kind: each is a tick over the database and has no queue to poll.
func prepareWorkers(lc fx.Lifecycle, p workerParams, obs *observability.Observer) {
	wageringhandler.PrepareWorker(p.Runner, p.Queue, p.Wagering)
	outboxhandler.PrepareCronjob(p.Cronjobs, p.Outbox)
	referencehandler.PrepareCronjob(p.Cronjobs, p.Reference)

	// Asking for the service account token at boot surfaces a bad credential while the deploy
	// is still on someone's screen, instead of on the first message at 3am.
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if _, err := p.Account.Token(ctx); err != nil {
				return err
			}
			obs.Info(ctx, "worker authenticated with keycloak service account")
			return nil
		},
	})
}

// newProbeEcho builds the probe server. It carries no telemetry middleware on purpose: a
// liveness probe every few seconds would be most of the worker trace volume and none of its
// meaning.
func newProbeEcho() *echo.Echo {
	e := echo.New()
	e.HideBanner = true
	e.HidePort = true
	e.Use(echomiddleware.Recover())
	return e
}

func probeRoutes(e *echo.Echo, h *health.Handler) {
	health.ServerRoutes(e, h)
}

func runProbe(lc fx.Lifecycle, e *echo.Echo, cfg config.Config, obs *observability.Observer) {
	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			go func() {
				if err := e.Start(cfg.Addr()); err != nil && !errors.Is(err, http.ErrServerClosed) {
					obs.Error(ctx, err, "worker probe server stopped unexpectedly")
				}
			}()
			obs.Info(ctx, "worker probe started", observability.String("addr", cfg.Addr()))
			return nil
		},
		OnStop: func(ctx context.Context) error {
			return e.Shutdown(ctx)
		},
	})
}
