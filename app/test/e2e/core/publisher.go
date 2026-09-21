//go:build e2e

package core

import (
	"context"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/handlers"
	outboxhandler "github.com/estrategiahq/pedro-test/app/src/handlers/outbox"
	referencehandler "github.com/estrategiahq/pedro-test/app/src/handlers/reference"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/cronjob"
)

// registerCronjobs is what cmd/worker does for the recurring work: it puts the outbox publisher and
// the resolver of pending references on the cronjob runtime, the same way and in the same place.
func registerCronjobs(runner *cronjob.Runner, outbox *outboxhandler.CronjobHandler, reference *referencehandler.CronjobHandler) {
	runner.Register(outbox)
	runner.Register(reference)
}

// Publisher is one more, independent instance of the worker's recurring jobs, the outbox publisher and
// the resolver of pending references: its own connection pool, its own SQS client and its own claim
// name, sharing nothing with the others but the database and the queue. Several of them running at
// once is the situation both jobs have to be correct in.
type Publisher struct {
	app *fx.App
}

// StartPublisher boots an extra publisher and stops it when the test ends.
func (s *Stack) StartPublisher(t *testing.T, name string) *Publisher {
	t.Helper()

	app := fx.New(
		fx.Supply(appinfo.App{Name: name, Role: appinfo.RoleWorker, Version: "test"}),

		bootstrap.Core,
		handlers.Module,
		cronjob.Module,
		s.Faults.options(),
		s.telemetry.options(),

		fx.Invoke(registerCronjobs),
		fx.Invoke(cronjob.Run),

		fx.NopLogger,
		fx.StartTimeout(60*time.Second),
		fx.StopTimeout(30*time.Second),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("start the publisher %s: %v", name, err)
	}

	publisher := &Publisher{app: app}
	t.Cleanup(publisher.Stop)
	return publisher
}

// Stop shuts the instance down, letting the tick in flight finish.
func (p *Publisher) Stop() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = p.app.Stop(ctx)
}
