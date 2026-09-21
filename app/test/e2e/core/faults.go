//go:build e2e

package core

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	healthiface "github.com/estrategiahq/pedro-test/app/src/interfaces/health"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	persistenceiface "github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
)

// Faults is where a scenario asks for a failure that cannot be provoked from outside: a broker
// that refuses an event, or a process that dies after publishing and before recording it.
//
// The failures are injected at the two ports of the outbox, the publisher and the repository,
// and nowhere else. Everything below them, the claim, the lease, the backoff and the recovery, is
// the real code running against the real database, which is what the scenarios are about.
//
// It is shared by every instance the suite starts, so a failure asked for is consumed by whichever
// instance happens to claim the event, exactly as it would be by a real fleet.
type Faults struct {
	mu           sync.Mutex
	failPublish  map[string]int
	failComplete map[string]int
	failStorage  map[string]int
	attempts     map[string][]time.Time
	down         map[string]bool
}

func newFaults() *Faults {
	return &Faults{
		failPublish:  map[string]int{},
		failComplete: map[string]int{},
		failStorage:  map[string]int{},
		attempts:     map[string][]time.Time{},
		down:         map[string]bool{},
	}
}

// FailPublishing makes the next publications of an event fail, as a broker that is unreachable
// does.
func (f *Faults) FailPublishing(eventID string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failPublish[eventID] = times
}

// FailRecordingPublication makes the next attempts to record that an event was published fail,
// after the broker already has it. It is the crash between publishing and confirming.
func (f *Faults) FailRecordingPublication(eventID string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failComplete[eventID] = times
}

// FailStorageFor makes the storage unavailable, as an unreachable database is, the next times the
// operation with that idempotency key is looked up. It is a transient failure by construction: the
// operation succeeds as soon as the failures are used up.
func (f *Faults) FailStorageFor(idempotencyKey string, times int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failStorage[idempotencyKey] = times
}

// SetDependencyDown makes a dependency answer the readiness probe as an unreachable one does, until
// it is set back. The probe is what is under test, not the dependency: the failure is injected at
// the checker's port, and the response, the status and the log are the real ones.
func (f *Faults) SetDependencyDown(name string, down bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.down[name] = down
}

func (f *Faults) isDown(name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.down[name]
}

// PublishAttempts is when each publication of an event was attempted, by any instance, whether it
// succeeded or not.
func (f *Faults) PublishAttempts(eventID string) []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.attempts[eventID]...)
}

func (f *Faults) record(eventID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts[eventID] = append(f.attempts[eventID], time.Now())
}

func (f *Faults) consume(remaining map[string]int, eventID string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if remaining[eventID] > 0 {
		remaining[eventID]--
		return true
	}
	return false
}

// options decorates the outbox ports of an application with the fault injection.
func (f *Faults) options() fx.Option {
	return fx.Options(
		fx.Decorate(func(inner outboxiface.Publisher) outboxiface.Publisher {
			return &faultyPublisher{inner: inner, faults: f}
		}),
		fx.Decorate(func(inner outboxiface.Repository) outboxiface.Repository {
			return &faultyRepository{Repository: inner, faults: f}
		}),
		fx.Decorate(func(inner wageringiface.Repository) wageringiface.Repository {
			return &faultyWagering{Repository: inner, faults: f}
		}),
		fx.Decorate(fx.Annotate(
			func(checkers []healthiface.Checker) []healthiface.Checker {
				wrapped := make([]healthiface.Checker, len(checkers))
				for i, checker := range checkers {
					wrapped[i] = &faultyChecker{inner: checker, faults: f}
				}
				return wrapped
			},
			fx.ParamTags(`group:"health"`), fx.ResultTags(`group:"health"`),
		)),
	)
}

type faultyPublisher struct {
	inner  outboxiface.Publisher
	faults *Faults
}

func (p *faultyPublisher) Publish(ctx context.Context, event *entities.OutboxEvent) error {
	id := event.ID().String()
	p.faults.record(id)
	if p.faults.consume(p.faults.failPublish, id) {
		return errors.New("injected: the broker is unavailable")
	}
	return p.inner.Publish(ctx, event)
}

// faultyRepository passes everything through but the recording of a publication.
type faultyRepository struct {
	outboxiface.Repository
	faults *Faults
}

func (r *faultyRepository) Complete(ctx context.Context, event *entities.OutboxEvent) error {
	if r.faults.consume(r.faults.failComplete, event.ID().String()) {
		return errors.New("injected: the process died before recording the publication")
	}
	return r.Repository.Complete(ctx, event)
}

// faultyWagering passes everything through but the lookup by idempotency key, which every
// operation makes first and which is where an unreachable database shows up.
type faultyWagering struct {
	wageringiface.Repository
	faults *Faults
}

func (r *faultyWagering) FindByKey(ctx context.Context, providerID, key string) (*entities.WagerTransaction, error) {
	if r.faults.consume(r.faults.failStorage, key) {
		return nil, fmt.Errorf("injected: %w", persistenceiface.ErrUnavailable)
	}
	return r.Repository.FindByKey(ctx, providerID, key)
}

type faultyChecker struct {
	inner  healthiface.Checker
	faults *Faults
}

func (c *faultyChecker) Name() string { return c.inner.Name() }

func (c *faultyChecker) Check(ctx context.Context) error {
	if c.faults.isDown(c.inner.Name()) {
		return errors.New("injected: the dependency is unreachable")
	}
	return c.inner.Check(ctx)
}
