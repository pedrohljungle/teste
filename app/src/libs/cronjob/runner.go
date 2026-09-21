package cronjob

import (
	"context"
	"errors"
	"math/rand/v2"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// maxErrorBackoff caps how long a task that keeps failing waits between attempts.
const maxErrorBackoff = 30 * time.Second

// Task is one unit of recurring work. Returning how many items it handled lets the runtime tick
// again at once instead of idling while a backlog drains.
type Task interface {
	// Name is what the task appears as on spans and logs.
	Name() string
	// Interval is how long to wait between ticks when the last tick found nothing to do.
	Interval() time.Duration
	// Run does one tick. handled is the number of items it found: positive means there may be
	// more waiting. An error makes the runtime wait longer before the next tick.
	Run(ctx context.Context) (handled int, err error)
}

// Runner ticks every registered task on its own schedule. It opens no transaction and elects no
// leader: a task that must not run on two instances at once coordinates through the database,
// as the outbox does with SKIP LOCKED, and running on every instance is what gives the system its
// recovery.
type Runner struct {
	obs   *observability.Observer
	tasks []Task
}

// NewRunner builds the runtime.
func NewRunner(obs *observability.Observer) *Runner {
	return &Runner{obs: obs}
}

// Register adds a task. The worker entrypoint calls it with the handlers it wants scheduled, which
// is where a handler is held to this contract.
func (r *Runner) Register(task Task) {
	r.tasks = append(r.tasks, task)
}

// Run hands the runner to the fx lifecycle. Stopping cancels the schedules and waits for the ticks
// in flight, so the process does not die halfway through a commit.
func Run(lc fx.Lifecycle, r *Runner) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(startCtx context.Context) error {
			if len(r.tasks) == 0 {
				r.obs.Warn(startCtx, "no cronjob registered: the worker will run no recurring work")
				return nil
			}
			for _, task := range r.tasks {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r.loop(ctx, task)
				}()
				r.obs.Info(startCtx, "cronjob scheduled",
					observability.String("task", task.Name()),
					observability.Duration("interval", task.Interval()),
				)
			}
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			r.obs.Info(stopCtx, "shutting down cronjobs, waiting for the ticks in flight")
			cancel()

			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()

			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return errors.Join(errors.New("cronjobs did not finish before the deadline"), stopCtx.Err())
			}
		},
	})
}

func (r *Runner) loop(ctx context.Context, task Task) {
	// The first tick is delayed by a random part of the interval, so replicas that start together
	// do not all look at the database in the same instant.
	wait := jitter(task.Interval())
	failures := 0

	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}

		handled, err := r.tick(task)
		switch {
		case err != nil:
			// A task that fails waits longer each time, and never faster than its interval: the
			// pause is what keeps an outage from turning into a hot loop against the database.
			failures++
			wait = errorBackoff(task.Interval(), failures)
		case handled > 0:
			// There may be more waiting: look again at once, and only rest when a look is empty.
			failures, wait = 0, 0
		default:
			failures, wait = 0, jitter(task.Interval())
		}
	}
}

// tick runs the task once. Its context does not inherit the cancellation of the schedule: a tick
// that already started has to finish, because it may be in the middle of a commit, and OnStop is
// what waits for it.
func (r *Runner) tick(task Task) (int, error) {
	ctx := observability.WithErrorTrail(context.Background())
	return observability.Trace(ctx, r.obs, observability.LayerHandler, "cronjob."+task.Name(), task.Run)
}

func errorBackoff(interval time.Duration, failures int) time.Duration {
	wait := interval
	for i := 0; i < failures && wait < maxErrorBackoff; i++ {
		wait *= 2
	}
	if wait > maxErrorBackoff {
		wait = maxErrorBackoff
	}
	return jitter(wait)
}

// jitter returns a duration within a fifth either side of d.
func jitter(d time.Duration) time.Duration {
	window := int64(d) / 5
	if window <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*window+1)-window)
}
