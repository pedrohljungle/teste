//go:build e2e

package core

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/auth"
	"github.com/estrategiahq/pedro-test/app/src/libs/bootstrap"
	"github.com/estrategiahq/pedro-test/app/src/libs/cronjob"
)

// probeTask is a recurring task that only counts its ticks, so a scenario can tell whether the
// runtime is still ticking.
type probeTask struct{ ticks atomic.Int64 }

func (p *probeTask) Name() string            { return "probe" }
func (p *probeTask) Interval() time.Duration { return 20 * time.Millisecond }
func (p *probeTask) Run(context.Context) (int, error) {
	p.ticks.Add(1)
	return 0, nil
}

// Probe is an instance of the worker's runtime whose only job is to be started and stopped, with
// what a scenario needs to look at afterwards: whether its tasks still run and whether its database
// pool is still open.
type Probe struct {
	app  *fx.App
	pool *pgxpool.Pool
	task *probeTask
}

// StartProbe boots an instance with the shared modules, the cronjob runtime and a probe task.
func (s *Stack) StartProbe(t *testing.T) *Probe {
	t.Helper()

	probe := &Probe{task: &probeTask{}}
	probe.app = fx.New(
		fx.Supply(appinfo.App{Name: "probe", Role: appinfo.RoleWorker, Version: "test"}),
		bootstrap.Core,
		cronjob.Module,
		fx.Populate(&probe.pool),
		fx.Invoke(func(runner *cronjob.Runner) { runner.Register(probe.task) }),
		fx.Invoke(cronjob.Run),
		fx.NopLogger,
		fx.StartTimeout(60*time.Second),
		fx.StopTimeout(30*time.Second),
	)
	if err := probe.app.Start(context.Background()); err != nil {
		t.Fatalf("start the probe instance: %v", err)
	}
	return probe
}

// Ticks is how many times the probe task ran.
func (p *Probe) Ticks() int64 { return p.task.ticks.Load() }

// PoolOpen reports whether the instance's connection pool still answers.
func (p *Probe) PoolOpen() bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return p.pool.Ping(ctx) == nil
}

// Stop shuts the instance down and returns what the shutdown returned.
func (p *Probe) Stop() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return p.app.Stop(ctx)
}

// StartWithUnreachableIdP boots the part of the server that depends on the identity provider, with
// the provider's address pointing nowhere, and returns what the start answered. The address is
// restored before returning: it is process-wide, and the instances that are already running read
// theirs when they were built.
func (s *Stack) StartWithUnreachableIdP(t *testing.T, patience time.Duration) error {
	t.Helper()

	const key = "KEYCLOAK_ISSUER"
	original, existed := os.LookupEnv(key)
	if err := os.Setenv(key, "http://127.0.0.1:1/realms/nowhere"); err != nil {
		t.Fatalf("set the issuer: %v", err)
	}
	defer func() {
		if existed {
			_ = os.Setenv(key, original)
		} else {
			_ = os.Unsetenv(key)
		}
	}()

	app := fx.New(
		fx.Supply(appinfo.App{Name: "unreachable-idp", Role: appinfo.RoleServer, Version: "test"}),
		bootstrap.Core,
		auth.ServerModule,
		fx.Invoke(func(*auth.Verifier) {}),
		fx.NopLogger,
	)

	ctx, cancel := context.WithTimeout(context.Background(), patience)
	defer cancel()
	err := app.Start(ctx)
	if err == nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer stopCancel()
		_ = app.Stop(stopCtx)
	}
	return err
}
