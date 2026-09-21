package outbox

import (
	"context"
	"fmt"
	"math/rand/v2"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	outboxiface "github.com/estrategiahq/pedro-test/app/src/interfaces/outbox"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

type service struct {
	repo      outboxiface.Repository
	publisher outboxiface.Publisher
	cfg       config.Outbox
	obs       *observability.Observer

	// name identifies this publisher to the outbox, so a claim can be traced to who holds it. It
	// is unique per instance, because two instances that shared a name could release each
	// other's events.
	name string
	now  func() time.Time
	// jitter spreads a delay so instances that failed together do not retry together.
	jitter func(time.Duration) time.Duration
}

// NewService builds the outbox service for this instance.
func NewService(repo outboxiface.Repository, publisher outboxiface.Publisher, cfg config.Outbox, app appinfo.App, obs *observability.Observer) outboxiface.Service {
	return &service{
		repo:      repo,
		publisher: publisher,
		cfg:       cfg,
		obs:       obs,
		name:      fmt.Sprintf("%s/%s", app.Name, uuid.Must(uuid.NewV7())),
		now:       time.Now,
		jitter:    spread,
	}
}

func (s *service) PublishDue(ctx context.Context) (int, error) {
	return observability.Trace(ctx, s.obs, observability.LayerService, "outbox.Service.PublishDue", func(ctx context.Context) (int, error) {
		events, err := s.repo.Claim(ctx, s.name, s.cfg.BatchSize, s.cfg.Lease, s.now())
		if err != nil {
			return 0, fmt.Errorf("claim due events: %w", err)
		}
		for _, event := range events {
			s.publish(ctx, event)
		}
		return len(events), nil
	}, observability.String("publisher", s.name))
}

// publish sends one claimed event and records what came of it. It never returns an error: one
// event that cannot be published must not hold back the others of the batch, and the failure is
// already the event being rescheduled.
func (s *service) publish(ctx context.Context, event *entities.OutboxEvent) {
	ctx = observability.WithFields(ctx,
		observability.String("eventId", event.ID().String()),
		observability.String("eventType", string(event.Type())),
		observability.String("correlationId", event.CorrelationID()),
	)
	if err := s.publisher.Publish(ctx, event); err != nil {
		s.obs.Count(ctx, "outbox_publish_attempts_total", observability.NewTag("result", "failure"))
		s.reschedule(ctx, event, err)
		return
	}
	s.obs.Count(ctx, "outbox_publish_attempts_total", observability.NewTag("result", "success"))
	// The delay between the event happening and the broker having it: the lag of the outbox, which is
	// what tells a healthy publisher from one that is falling behind.
	s.obs.Measure(ctx, "outbox_publish_delay_seconds", s.now().Sub(event.OccurredAt()),
		observability.NewTag("event_type", string(event.Type())))
	if err := event.MarkPublished(s.now()); err != nil {
		s.obs.Error(ctx, err, "could not mark the event as published", observability.String("eventId", event.ID().String()))
		return
	}
	// When storing this fails, the broker already has the event and the outbox still says PENDING.
	// The lease will run out and someone publishes it again under the same event id, which is the
	// at-least-once delivery the contract promises and not a loss.
	if err := s.repo.Complete(ctx, event); err != nil {
		s.obs.Error(ctx, err, "published the event but could not record it, it will be published again with the same id",
			observability.String("eventId", event.ID().String()))
	}
}

func (s *service) reschedule(ctx context.Context, event *entities.OutboxEvent, cause error) {
	wait := s.backoff(event.Attempts())
	s.obs.Error(ctx, cause, "could not publish the event, it will be retried",
		observability.String("eventId", event.ID().String()),
		observability.Int("attempts", event.Attempts()),
		observability.Duration("retryIn", wait),
	)
	if err := event.Reschedule(s.now().Add(wait)); err != nil {
		s.obs.Error(ctx, err, "could not reschedule the event", observability.String("eventId", event.ID().String()))
		return
	}
	if err := s.repo.Release(ctx, event, s.name); err != nil {
		// The lease is what brings it back if it cannot be released now.
		s.obs.Error(ctx, err, "could not release the event", observability.String("eventId", event.ID().String()))
	}
}

// backoff is how long to wait after the given attempt failed: the base doubled on every attempt,
// up to the maximum, spread so instances do not retry in step. An event is never given up on, so
// the wait is capped and does not grow without bound.
func (s *service) backoff(attempts int) time.Duration {
	wait := s.cfg.BackoffBase
	for i := 1; i < attempts && wait < s.cfg.BackoffMax; i++ {
		wait *= 2
	}
	if wait > s.cfg.BackoffMax {
		wait = s.cfg.BackoffMax
	}
	return s.jitter(wait)
}

// spread returns a duration within a fifth either side of d.
func spread(d time.Duration) time.Duration {
	window := int64(d) / 5
	if window <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*window+1)-window)
}
