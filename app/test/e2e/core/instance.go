//go:build e2e

package core

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/handlers"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/middleware"
)

// bootEnv serialises the boots that change PORT: the configuration is read from the environment
// while the graph is built, and the environment is process-wide.
var bootEnv sync.Mutex

// Instance is one more, independent copy of the HTTP server: its own connection pool, its own
// verifier and its own memory, sharing nothing with the others but the database. Several of them
// answering at once is what a fleet behind a load balancer looks like.
type Instance struct {
	// BaseURL is where this instance answers.
	BaseURL string

	app  *fx.App
	once sync.Once
}

// StartServer boots an independent server and stops it when the test ends.
func (s *Stack) StartServer(t *testing.T, name string) *Instance {
	t.Helper()

	port, err := freePort()
	if err != nil {
		t.Fatalf("pick a port: %v", err)
	}

	bootEnv.Lock()
	defer bootEnv.Unlock()
	if err := os.Setenv("PORT", port); err != nil {
		t.Fatalf("set the port: %v", err)
	}
	defer func() { _ = os.Setenv("PORT", s.port) }()

	app := fx.New(
		fx.Supply(appinfo.App{Name: name, Role: appinfo.RoleServer, Version: "test"}),

		bootstrap.Core,
		auth.ServerModule,
		middleware.Module,
		handlers.Module,
		s.Faults.options(),
		s.telemetry.options(),

		fx.Provide(newEcho),
		fx.Invoke(serverRoutes),
		fx.Invoke(runServer),

		fx.NopLogger,
		fx.StartTimeout(60*time.Second),
		fx.StopTimeout(30*time.Second),
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("start the server %s: %v", name, err)
	}

	instance := &Instance{BaseURL: "http://127.0.0.1:" + port, app: app}
	t.Cleanup(instance.Stop)
	return instance
}

// Stop shuts the instance down. It is safe to call more than once.
func (i *Instance) Stop() {
	i.once.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = i.app.Stop(ctx)
	})
}
