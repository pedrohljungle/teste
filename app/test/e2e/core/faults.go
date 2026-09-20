//go:build e2e

package core

import (
	"context"
	"errors"
	"sync"
	"time"

	"go.uber.org/fx"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
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
	attempts     map[string][]time.Time
}

func newFaults() *Faults {
	return &Faults{
		failPublish:  map[string]int{},
		failComplete: map[string]int{},
		attempts:     map[string][]time.Time{},
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
