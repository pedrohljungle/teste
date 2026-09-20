package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/src/libs/appinfo"
	"github.com/estrategiahq/pedro-test/app/src/libs/config"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

var fixedNow = time.Date(2026, time.September, 8, 12, 0, 0, 0, time.UTC)

// repository is an outbox double that records what the service asks of it.
type repository struct {
	claimed      []*entities.OutboxEvent
	claimErr     error
	completeErr  error
	claimedBy    string
	claimedLimit int
	claimedLease time.Duration

	completed []uuid.UUID
	released  map[uuid.UUID]releasedEvent
}

type releasedEvent struct {
	nextAttemptAt time.Time
	publisher     string
}

func (r *repository) Insert(context.Context, *entities.OutboxEvent) error { return nil }

func (r *repository) Claim(_ context.Context, publisher string, limit int, lease time.Duration, _ time.Time) ([]*entities.OutboxEvent, error) {
	r.claimedBy, r.claimedLimit, r.claimedLease = publisher, limit, lease
	return r.claimed, r.claimErr
}

func (r *repository) Complete(_ context.Context, event *entities.OutboxEvent) error {
	if r.completeErr != nil {
		return r.completeErr
	}
	r.completed = append(r.completed, event.ID())
	return nil
}

func (r *repository) Release(_ context.Context, event *entities.OutboxEvent, publisher string) error {
	if r.released == nil {
		r.released = map[uuid.UUID]releasedEvent{}
	}
	r.released[event.ID()] = releasedEvent{nextAttemptAt: event.NextAttemptAt(), publisher: publisher}
	return nil
}

// publisher is a broker double that fails for the events it was told to.
type publisher struct {
	failing   map[uuid.UUID]error
	published []uuid.UUID
}

func (p *publisher) Publish(_ context.Context, event *entities.OutboxEvent) error {
	if err := p.failing[event.ID()]; err != nil {
		return err
	}
	p.published = append(p.published, event.ID())
	return nil
}

var testConfig = config.Outbox{
	PollInterval: time.Second,
	BatchSize:    10,
	Lease:        time.Minute,
	BackoffBase:  time.Second,
	BackoffMax:   30 * time.Second,
}

func newTestService(repo *repository, pub *publisher) *service {
	return &service{
		repo:      repo,
		publisher: pub,
		cfg:       testConfig,
		obs:       observability.NewNop(),
		name:      "worker/test",
		now:       func() time.Time { return fixedNow },
		jitter:    func(d time.Duration) time.Duration { return d },
	}
}

// event builds an outbox event the way the service that writes them does, then hands it over as a
// claim would: rehydrated, with the attempt the claim counted.
func event(t *testing.T, n int, attempts int) *entities.OutboxEvent {
	t.Helper()
	opening, err := entities.OpenWallet(
		entities.OpeningIDs{
			Wallet:      uuid.MustParse("00000000-0000-7000-8000-000000000001"),
			Transaction: uuid.MustParse("00000000-0000-7000-8000-000000000002"),
			Entry:       uuid.MustParse("00000000-0000-7000-8000-000000000003"),
		},
		uuid.MustParse("00000000-0000-7000-8000-000000000004"), mustBRL(t, "10.00"), fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	built, err := entities.NewWagerTransactionProcessedEvent(
		uuid.MustParse("00000000-0000-7000-8000-0000000001"+twoDigits(n)), opening.Transaction, "corr", "", fixedNow)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := built.Snapshot()
	snapshot.Attempts = attempts
	claimed, err := entities.RehydrateOutboxEvent(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	return claimed
}

func twoDigits(n int) string {
	return string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}

func mustBRL(t *testing.T, amount string) entities.Money {
	t.Helper()
	m, err := entities.ParseMoney(amount, "BRL")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestPublishingADueEventSendsItAndRecordsIt(t *testing.T) {
	e := event(t, 1, 1)
	repo := &repository{claimed: []*entities.OutboxEvent{e}}
	pub := &publisher{}

	found, err := newTestService(repo, pub).PublishDue(context.Background())

	if err != nil || found != 1 {
		t.Fatalf("found %d, error %v", found, err)
	}
	if len(pub.published) != 1 || pub.published[0] != e.ID() {
		t.Fatalf("published = %v", pub.published)
	}
	if len(repo.completed) != 1 || repo.completed[0] != e.ID() {
		t.Fatalf("completed = %v", repo.completed)
	}
	if e.Status() != entities.OutboxPublished {
		t.Fatalf("status = %s", e.Status())
	}
	if len(repo.released) != 0 {
		t.Fatal("a published event must not be released")
	}
}

func TestTheServiceClaimsWithItsOwnNameTheConfiguredBatchAndLease(t *testing.T) {
	repo := &repository{}

	found, err := newTestService(repo, &publisher{}).PublishDue(context.Background())

	if err != nil || found != 0 {
		t.Fatalf("found %d, error %v", found, err)
	}
	if repo.claimedBy != "worker/test" || repo.claimedLimit != 10 || repo.claimedLease != time.Minute {
		t.Fatalf("claimed by %q, limit %d, lease %s", repo.claimedBy, repo.claimedLimit, repo.claimedLease)
	}
}

func TestAFailedPublicationIsRescheduledWithBackoffAndTheOthersGoOn(t *testing.T) {
	failing := event(t, 1, 3)
	healthy := event(t, 2, 1)
	repo := &repository{claimed: []*entities.OutboxEvent{failing, healthy}}
	pub := &publisher{failing: map[uuid.UUID]error{failing.ID(): errors.New("broker unreachable")}}

	found, err := newTestService(repo, pub).PublishDue(context.Background())

	if err != nil || found != 2 {
		t.Fatalf("found %d, error %v: one event failing must not fail the call", found, err)
	}
	released, ok := repo.released[failing.ID()]
	if !ok {
		t.Fatal("the failed event was not released")
	}
	// The third attempt failed: the base doubled twice, 1s -> 4s.
	if want := fixedNow.Add(4 * time.Second); !released.nextAttemptAt.Equal(want) || released.publisher != "worker/test" {
		t.Fatalf("released = %+v, want next attempt at %s by worker/test", released, want)
	}
	if failing.Status() != entities.OutboxPending {
		t.Fatalf("a failed event must stay PENDING, it is %s", failing.Status())
	}
	if len(repo.completed) != 1 || repo.completed[0] != healthy.ID() {
		t.Fatalf("the healthy event was not published: completed %v", repo.completed)
	}
}

func TestTheBackoffDoublesFromTheBaseAndStopsAtTheMaximum(t *testing.T) {
	s := newTestService(&repository{}, &publisher{})

	cases := map[int]time.Duration{
		1:  time.Second,
		2:  2 * time.Second,
		3:  4 * time.Second,
		4:  8 * time.Second,
		5:  16 * time.Second,
		6:  30 * time.Second,
		7:  30 * time.Second,
		50: 30 * time.Second,
	}
	for attempts, want := range cases {
		if got := s.backoff(attempts); got != want {
			t.Errorf("backoff(%d) = %s, want %s", attempts, got, want)
		}
	}
}

func TestAnEventThatCannotBeRecordedAfterPublishingIsNotAnErrorAndIsNotLost(t *testing.T) {
	e := event(t, 1, 1)
	repo := &repository{claimed: []*entities.OutboxEvent{e}, completeErr: errors.New("database went away")}
	pub := &publisher{}

	found, err := newTestService(repo, pub).PublishDue(context.Background())

	if err != nil || found != 1 {
		t.Fatalf("found %d, error %v", found, err)
	}
	if len(pub.published) != 1 {
		t.Fatal("the event must have been published")
	}
	// Nothing was stored, so the outbox still says PENDING and the lease brings it back: it will
	// be published again with the same event id.
	if len(repo.completed) != 0 || len(repo.released) != 0 {
		t.Fatalf("completed %v, released %v: nothing should be stored", repo.completed, repo.released)
	}
}

func TestAClaimThatFailsIsReturned(t *testing.T) {
	repo := &repository{claimErr: errors.New("database went away")}

	found, err := newTestService(repo, &publisher{}).PublishDue(context.Background())

	if err == nil || found != 0 {
		t.Fatalf("found %d, error %v, want the failure of the claim", found, err)
	}
}

func TestAnEventThatIsNotPendingCannotBePublishedNorRescheduled(t *testing.T) {
	published := event(t, 1, 1)
	if err := published.MarkPublished(fixedNow); err != nil {
		t.Fatal(err)
	}
	repo := &repository{claimed: []*entities.OutboxEvent{published}}
	pub := &publisher{failing: map[uuid.UUID]error{published.ID(): errors.New("nope")}}

	// It is claimed as PENDING in reality; this exercises the guard that keeps a settled event
	// from being touched if it ever arrives here.
	if _, err := newTestService(repo, pub).PublishDue(context.Background()); err != nil {
		t.Fatalf("PublishDue: %v", err)
	}
	if len(repo.released) != 0 {
		t.Fatal("a settled event was released")
	}
}

func TestTheSpreadStaysWithinAFifthEitherSide(t *testing.T) {
	base := 10 * time.Second
	for range 500 {
		got := spread(base)
		if got < 8*time.Second || got > 12*time.Second {
			t.Fatalf("spread(%s) = %s, outside 8s..12s", base, got)
		}
	}
	if got := spread(0); got != 0 {
		t.Fatalf("spread(0) = %s", got)
	}
	if got := spread(3 * time.Nanosecond); got != 3*time.Nanosecond {
		t.Fatalf("a delay too small to spread must be returned as it is, got %s", got)
	}
}

func TestEveryInstanceGetsItsOwnPublisherName(t *testing.T) {
	first, ok := NewService(&repository{}, &publisher{}, testConfig, appinfo.App{Name: "pedro-test-worker"}, observability.NewNop()).(*service)
	second, ok2 := NewService(&repository{}, &publisher{}, testConfig, appinfo.App{Name: "pedro-test-worker"}, observability.NewNop()).(*service)
	if !ok || !ok2 {
		t.Fatal("NewService must build the service")
	}
	if first.name == second.name {
		t.Fatalf("two instances share the publisher name %q: they could release each other's events", first.name)
	}
	if time.Since(first.now()) > time.Minute {
		t.Fatal("now is not the current time")
	}
}
