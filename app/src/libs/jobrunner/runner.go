package jobrunner

import (
	"context"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// Source is the consuming port. Consume returns (nil, nil) when the poll window closed with
// no message, which is what gives the loop a chance to notice a shutdown.
type Source interface {
	Name() string
	Consume(ctx context.Context) (*structs.QueueMessage, error)
	// Ack confirms the message was processed. Not acking is how a failure asks for
	// redelivery.
	Ack(ctx context.Context, msg structs.QueueMessage) error
	Depth(ctx context.Context) (int64, error)
}

// Handler processes one message. It is satisfied by a handler, exactly as an HTTP route is.
//
// The contract is the whole point of this runtime, and it is short: **return nil and the
// message is deleted; return an error and it is not**. A message that is not deleted becomes
// visible again once the visibility timeout expires and is delivered another time, until the
// broker gives up and moves it to the dead letter queue. There is no third outcome and no way
// to ask for one.
//
// Two things follow, and a handler that ignores either of them will misbehave in production:
// it has to be idempotent, because delivery is at least once and the same message will arrive
// twice sooner or later; and it must return nil for a failure retrying cannot fix — a message
// naming a record that no longer exists is done, not failed.
type Handler func(ctx context.Context, msg structs.QueueMessage) error

const (
	depthInterval  = 15 * time.Second
	consumeBackoff = time.Second
)

type registration struct {
	source  Source
	handler Handler
}

// Runner polls every registered source and dispatches to its handler.
type Runner struct {
	cfg           config.Worker
	obs           *observability.Observer
	registrations []registration
}

// NewRunner builds the runtime.
func NewRunner(cfg config.Worker, obs *observability.Observer) *Runner {
	return &Runner{cfg: cfg, obs: obs}
}

// Register binds a source to the handler that processes its messages. Domains call it through
// their own PrepareWorker, which the worker entrypoint invokes.
func (r *Runner) Register(source Source, handler Handler) {
	r.registrations = append(r.registrations, registration{source: source, handler: handler})
}

// Run hands the runner to the fx lifecycle. Stopping cancels the loops and waits for the
// messages in flight, so the process does not die halfway through one.
func Run(lc fx.Lifecycle, r *Runner) {
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup

	lc.Append(fx.Hook{
		OnStart: func(startCtx context.Context) error {
			if len(r.registrations) == 0 {
				// Not an error: a repository with no domain yet has nothing to consume, and
				// failing the boot would mean the worker cannot even be started to check it
				// is wired. It is loud, though — a worker consuming nothing in an
				// environment that has domains is a deploy that quietly does no work.
				r.obs.Warn(startCtx, "no queue registered: the worker will consume nothing")
				return nil
			}

			wg.Add(1)
			go func() {
				defer wg.Done()
				r.loop(ctx)
			}()

			for _, reg := range r.registrations {
				r.obs.Info(startCtx, "consuming queue",
					observability.String("queue", reg.source.Name()),
					observability.Int("concurrency", r.cfg.Concurrency),
				)
			}
			return nil
		},
		OnStop: func(stopCtx context.Context) error {
			r.obs.Info(stopCtx, "shutting down worker, waiting for messages in flight")
			cancel()

			done := make(chan struct{})
			go func() { wg.Wait(); close(done) }()

			select {
			case <-done:
				return nil
			case <-stopCtx.Done():
				return stopCtx.Err()
			}
		},
	})
}

func (r *Runner) loop(ctx context.Context) {
	var wg sync.WaitGroup
	for _, reg := range r.registrations {
		for i := 0; i < r.cfg.Concurrency; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r.consume(ctx, reg)
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			r.reportDepth(ctx, reg.source)
		}()
	}
	wg.Wait()
}

func (r *Runner) consume(ctx context.Context, reg registration) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := reg.source.Consume(ctx)
		if err != nil {
			// The pause keeps a broker outage from becoming a hot loop with thousands of
			// attempts and log lines per second.
			r.obs.Error(ctx, err, "failed to consume the queue",
				observability.String("queue", reg.source.Name()))
			sleep(ctx, consumeBackoff)
			continue
		}
		if msg == nil {
			continue
		}
		r.dispatch(reg, *msg)
	}
}

// dispatch is where the publisher trace becomes the worker trace, and where the per-message
// error marker is installed. The context deliberately does not inherit the loop cancellation:
// a message must finish even once shutdown started, and OnStop is what waits for it.
func (r *Runner) dispatch(reg registration, msg structs.QueueMessage) {
	ctx := observability.ExtractTrace(context.Background(), msg.TraceContext)
	ctx = observability.WithErrorTrail(ctx)

	// The handler opens its own span, exactly as an HTTP handler does.
	if err := reg.handler(ctx, msg); err != nil {
		// The error was already reported by the handler span. Skipping the ack is what
		// asks the broker to deliver it again.
		return
	}

	if err := reg.source.Ack(ctx, msg); err != nil {
		// The work is done but the message was not removed, so it will arrive again. The
		// handlers are idempotent for exactly this case, and the failure has to be visible
		// because a queue that never drains looks the same from the outside.
		r.obs.Error(ctx, err, "failed to acknowledge message",
			observability.String("queue", reg.source.Name()))
	}
}

// reportDepth publishes the backlog size, the number that answers whether the worker is
// keeping up. Per-message duration alone does not: enough fast messages also overflow.
func (r *Runner) reportDepth(ctx context.Context, source Source) {
	ticker := time.NewTicker(depthInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			depth, err := source.Depth(ctx)
			if err != nil {
				r.obs.Error(ctx, err, "failed to measure the queue",
					observability.String("queue", source.Name()))
				continue
			}
			r.obs.Info(ctx, "queue backlog",
				observability.String("queue", source.Name()),
				observability.Int64("depth", depth),
			)
		}
	}
}

func sleep(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}
