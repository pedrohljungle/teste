//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Transactional outbox publication
//
//	As a downstream consumer, I want every committed event and never an uncommitted one,
//	so that what I read always happened.
//
//	The events queue is read by a downstream consumer that the suite runs, which records what it
//	receives. Failures that cannot be provoked from outside, such as a broker that refuses an
//	event or a process that dies right after publishing, are injected at the two ports of the
//	outbox, and everything under them is the real code against the real database and queue.
//
//	Scenarios:
//	  - An event is published only after its transaction commits
//	  - An event whose transaction rolled back is never published
//	  - A published event follows the routing contract
//	  - Three publishers competing over the same outbox publish each event once
//	  - A publisher interrupted between publishing and confirming republishes the same event id
//	  - An abandoned lock is reclaimed by another publisher
//	  - A failed publication is retried with backoff
//	  - The outbox payload is an immutable snapshot
//	  - The events of one aggregate are published in the order they occurred
//	  - A failed event holds back the later events of its aggregate

const publishTimeout = 20 * time.Second

type outboxRow struct {
	status      string
	attempts    int
	lockedBy    *string
	publishedAt *time.Time
}

func readOutbox(t *testing.T, eventID string) (outboxRow, bool) {
	t.Helper()

	var row outboxRow
	err := stack.DB(t).QueryRow(context.Background(),
		"SELECT status, attempts, locked_by, published_at FROM outbox_events WHERE event_id = $1", eventID).
		Scan(&row.status, &row.attempts, &row.lockedBy, &row.publishedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return outboxRow{}, false
	}
	if err != nil {
		t.Fatalf("read the outbox row: %v", err)
	}
	return row, true
}

func waitUntilPublished(t *testing.T, eventID string) outboxRow {
	t.Helper()

	deadline := time.Now().Add(publishTimeout)
	for time.Now().Before(deadline) {
		if row, found := readOutbox(t, eventID); found && row.status == "PUBLISHED" {
			return row
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("event %s was not marked PUBLISHED within %s", eventID, publishTimeout)
	return outboxRow{}
}

// Scenario: An event is published only after its transaction commits
//
//	Given a wallet opening written in a transaction that has not committed yet
//	Then the event is invisible to the publishers and nothing is on the events queue
//	When the transaction commits
//	Then the event reaches the events queue and its row becomes PUBLISHED
func TestAnEventIsPublishedOnlyAfterItsTransactionCommits(t *testing.T) {
	opening := openWallet(t, "100.00")
	written, release, finished := make(chan *entities.OutboxEvent, 1), make(chan struct{}), make(chan error, 1)

	go func() {
		finished <- stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
			written <- storeOpening(t, ctx, opening)
			<-release
			return nil
		})
	}()
	event := <-written
	id := event.ID().String()

	// Several publisher ticks pass while the transaction is open.
	time.Sleep(1500 * time.Millisecond)
	if _, visible := readOutbox(t, id); visible {
		t.Fatal("the event is visible to another connection before its transaction committed")
	}
	if got := stack.EventDeliveries(id); len(got) != 0 {
		t.Fatalf("the event reached the queue %d times before its transaction committed", len(got))
	}

	close(release)
	if err := <-finished; err != nil {
		t.Fatalf("the transaction failed: %v", err)
	}

	stack.WaitForEvent(t, id, publishTimeout)
	if row := waitUntilPublished(t, id); row.publishedAt == nil {
		t.Fatal("a published event has no publication time")
	}
}

// Scenario: An event whose transaction rolled back is never published
//
//	Given a wallet opening written in a transaction that then fails
//	Then neither the event row nor a message on the events queue ever exists
func TestAnEventWhoseTransactionRolledBackIsNeverPublished(t *testing.T) {
	opening := openWallet(t, "100.00")
	var id string

	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		id = storeOpening(t, ctx, opening).ID().String()
		return context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("the transaction was expected to fail")
	}

	time.Sleep(1500 * time.Millisecond)
	if _, exists := readOutbox(t, id); exists {
		t.Fatal("the event of a rolled back transaction exists")
	}
	if got := stack.EventDeliveries(id); len(got) != 0 {
		t.Fatalf("the event of a rolled back transaction was delivered %d times", len(got))
	}
}

var occurredAtFormat = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z$`)

// Scenario: A published event follows the routing contract
//
//	Given a wallet opened with a positive balance under a request id
//	When its events are published
//	Then the message group is the aggregate id and the deduplication id is the event id
//	And the attributes carry the type, the id and the correlation id
//	And the body is the envelope with a UTC timestamp, version 1 and money as strings
func TestAPublishedEventFollowsTheRoutingContract(t *testing.T) {
	requestID := "req-" + uuid.NewString()
	res := stack.RequestWithHeaders(t, http.MethodPost, "/wallets", stack.ClientToken(t, core.InternalService),
		openBody{PlayerID: uuid.NewString(), InitialBalance: moneyBody{Amount: "250.00", Currency: "BRL"}},
		map[string]string{"X-Request-Id": requestID})
	opened := core.Decode[walletResponse](t, core.KeepStatus(t, res, http.StatusCreated))

	var eventID string
	if err := stack.DB(t).QueryRow(context.Background(),
		"SELECT event_id FROM outbox_events WHERE event_type = 'WalletBalanceChanged' AND aggregate_id = $1", opened.ID).Scan(&eventID); err != nil {
		t.Fatalf("read the event: %v", err)
	}

	delivery := stack.WaitForEvent(t, eventID, publishTimeout)

	if delivery.GroupID != opened.ID {
		t.Errorf("message group = %q, want the aggregate id %q", delivery.GroupID, opened.ID)
	}
	if delivery.DeduplicationID != eventID {
		t.Errorf("deduplication id = %q, want the event id %q", delivery.DeduplicationID, eventID)
	}
	if delivery.Attributes["eventType"] != "WalletBalanceChanged" || delivery.Attributes["eventId"] != eventID ||
		delivery.Attributes["correlationId"] != requestID {
		t.Errorf("attributes = %v", delivery.Attributes)
	}

	var envelope struct {
		EventID       string          `json:"eventId"`
		EventType     string          `json:"eventType"`
		AggregateID   string          `json:"aggregateId"`
		CorrelationID string          `json:"correlationId"`
		CausationID   string          `json:"causationId"`
		OccurredAt    string          `json:"occurredAt"`
		Version       int             `json:"version"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal([]byte(delivery.Body), &envelope); err != nil {
		t.Fatalf("the body is not JSON: %v", err)
	}
	if envelope.EventID != eventID || envelope.EventType != "WalletBalanceChanged" || envelope.AggregateID != opened.ID ||
		envelope.CorrelationID != requestID || envelope.Version != 1 || envelope.CausationID == "" {
		t.Errorf("envelope = %+v", envelope)
	}
	if !occurredAtFormat.MatchString(envelope.OccurredAt) {
		t.Errorf("occurredAt = %q, want RFC 3339 in UTC with milliseconds", envelope.OccurredAt)
	}
	var data struct {
		Money         moneyBody `json:"money"`
		BalanceBefore moneyBody `json:"balanceBefore"`
		BalanceAfter  moneyBody `json:"balanceAfter"`
		Direction     string    `json:"direction"`
		WalletVersion int64     `json:"walletVersion"`
	}
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatalf("the data does not carry money as strings: %v", err)
	}
	if data.Money.Amount != "250.00" || data.BalanceBefore.Amount != "0.00" || data.BalanceAfter.Amount != "250.00" ||
		data.Direction != "CREDIT" || data.WalletVersion != 1 {
		t.Errorf("data = %+v", data)
	}
}

// Scenario: Three publishers competing over the same outbox publish each event once
//
//	Given three independent publishers, each with its own connections and memory
//	And sixty events committed at once
//	When the publishers drain the outbox together
//	Then every event is published, and each one was sent to the broker exactly once
func TestThreePublishersCompetingOverTheSameOutboxPublishEachEventOnce(t *testing.T) {
	stack.StartPublisher(t, "publisher-2")
	stack.StartPublisher(t, "publisher-3")

	const events = 60
	ids := make([]string, 0, events)
	for range events {
		ids = append(ids, writeOpening(t, openWallet(t, "10.00")).ID().String())
	}

	for _, id := range ids {
		stack.WaitForEvent(t, id, 30*time.Second)
		waitUntilPublished(t, id)
	}
	for _, id := range ids {
		if attempts := stack.Faults.PublishAttempts(id); len(attempts) != 1 {
			t.Errorf("event %s was sent to the broker %d times, want exactly once", id, len(attempts))
		}
		if row, _ := readOutbox(t, id); row.attempts != 1 {
			t.Errorf("event %s was claimed %d times, want once", id, row.attempts)
		}
	}
}

// Scenario: A publisher interrupted between publishing and confirming republishes the same event id
//
//	Given an event whose publication the broker accepted
//	And the publisher died before recording it
//	When the lease runs out and a publisher takes the event over
//	Then it is published again carrying the same event id
//	And the row ends PUBLISHED
//	And the queue delivers it to the consumer only once
func TestAPublisherInterruptedBetweenPublishingAndConfirmingRepublishesTheSameEventId(t *testing.T) {
	stack.StartPublisher(t, "publisher-2")
	id := writeAndFailRecording(t)

	deadline := time.Now().Add(publishTimeout)
	for time.Now().Before(deadline) && len(stack.Faults.PublishAttempts(id)) < 2 {
		time.Sleep(100 * time.Millisecond)
	}
	if attempts := stack.Faults.PublishAttempts(id); len(attempts) != 2 {
		t.Fatalf("the event was sent %d times, want the original and one republication", len(attempts))
	}
	row := waitUntilPublished(t, id)
	if row.attempts != 2 {
		t.Fatalf("the event was claimed %d times, want 2", row.attempts)
	}

	// The queue drops the second send: same deduplication id, which is the event id.
	time.Sleep(1500 * time.Millisecond)
	if deliveries := stack.EventDeliveries(id); len(deliveries) != 1 || deliveries[0].EventID != id {
		t.Fatalf("the consumer received the event %d times, want once with id %s", len(deliveries), id)
	}
}

// writeAndFailRecording commits an event and arranges for the recording of its first publication to
// fail, before any publisher can see it.
func writeAndFailRecording(t *testing.T) string {
	t.Helper()

	opening := openWallet(t, "10.00")
	event := processedEvent(t, opening.Transaction)
	stack.Faults.FailRecordingPublication(event.ID().String(), 1)

	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		if err := stack.Repos.Wallets.Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := stack.Repos.Wagering.Insert(ctx, opening.Transaction); err != nil {
			return err
		}
		if err := stack.Repos.Wallets.InsertEntry(ctx, *opening.Entry); err != nil {
			return err
		}
		return stack.Repos.Outbox.Insert(ctx, event)
	})
	if err != nil {
		t.Fatalf("write the event: %v", err)
	}
	return event.ID().String()
}

// Scenario: An abandoned lock is reclaimed by another publisher
//
//	Given an event reserved by a publisher that died
//	Then no publisher takes it while the lease is still running
//	When the lease expires
//	Then a publisher claims it and publishes it
func TestAnAbandonedLockIsReclaimedByAnotherPublisher(t *testing.T) {
	opening := openWallet(t, "10.00")
	event := processedEvent(t, opening.Transaction)
	snapshot := event.Snapshot()

	if _, err := stack.DB(t).Exec(context.Background(), `
		INSERT INTO outbox_events
			(event_id, aggregate_type, aggregate_id, event_type, event_version, correlation_id,
			 payload, occurred_at, status, attempts, next_attempt_at, locked_by, locked_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, 'PENDING', 1, $8, 'dead-publisher', now())`,
		snapshot.EventID, snapshot.AggregateType, snapshot.AggregateID, snapshot.EventType,
		snapshot.EventVersion, snapshot.CorrelationID, snapshot.Payload, snapshot.OccurredAt); err != nil {
		t.Fatalf("insert the abandoned event: %v", err)
	}
	id := snapshot.EventID.String()

	// The lease is three seconds: for the first one and a half, the event is still reserved.
	time.Sleep(1500 * time.Millisecond)
	if row, _ := readOutbox(t, id); row.status != "PENDING" || row.attempts != 1 {
		t.Fatalf("the event was taken while its lease was running: %+v", row)
	}
	if got := stack.EventDeliveries(id); len(got) != 0 {
		t.Fatalf("the event was published while its lease was running")
	}

	stack.WaitForEvent(t, id, publishTimeout)
	row := waitUntilPublished(t, id)
	if row.attempts != 2 || row.lockedBy != nil {
		t.Fatalf("after the takeover: attempts %d, lockedBy %v", row.attempts, row.lockedBy)
	}
}

// Scenario: A failed publication is retried with backoff
//
//	Given a broker that refuses an event twice
//	When the publisher tries
//	Then the row stays PENDING between the attempts
//	And each wait is longer than the last
//	And the third attempt publishes it
func TestAFailedPublicationIsRetriedWithBackoff(t *testing.T) {
	opening := openWallet(t, "10.00")
	event := processedEvent(t, opening.Transaction)
	id := event.ID().String()
	stack.Faults.FailPublishing(id, 2)
	writeEvent(t, opening, event)

	stack.WaitForEvent(t, id, publishTimeout)
	row := waitUntilPublished(t, id)

	attempts := stack.Faults.PublishAttempts(id)
	if len(attempts) != 3 || row.attempts != 3 {
		t.Fatalf("sent %d times, claimed %d times, want 3 and 3", len(attempts), row.attempts)
	}
	// The base is 400ms and doubles, spread by a fifth: at least 320ms, then at least 640ms.
	first, second := attempts[1].Sub(attempts[0]), attempts[2].Sub(attempts[1])
	if first < 300*time.Millisecond || second < 600*time.Millisecond || second <= first {
		t.Fatalf("waits were %s then %s, want the second longer and both above the backoff", first, second)
	}
}

// writeEvent commits an opening together with a chosen event.
func writeEvent(t *testing.T, opening entities.WalletOpening, event *entities.OutboxEvent) {
	t.Helper()

	err := stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		if err := stack.Repos.Wallets.Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := stack.Repos.Wagering.Insert(ctx, opening.Transaction); err != nil {
			return err
		}
		if err := stack.Repos.Wallets.InsertEntry(ctx, *opening.Entry); err != nil {
			return err
		}
		return stack.Repos.Outbox.Insert(ctx, event)
	})
	if err != nil {
		t.Fatalf("write the event: %v", err)
	}
}

// Scenario: The outbox payload is an immutable snapshot
//
//	Given a WalletBalanceChanged written for a bet that left the balance at 975.00
//	When later operations move the balance
//	Then the stored payload still reads 975.00 with the version it had
func TestTheOutboxPayloadIsAnImmutableSnapshot(t *testing.T) {
	w := newWallet(t, "1000.00")
	bet := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, w.operation("BET", "25.00")), http.StatusOK))

	read := func() (string, int64) {
		var payload []byte
		if err := stack.DB(t).QueryRow(context.Background(), `
			SELECT payload FROM outbox_events
			WHERE event_type = 'WalletBalanceChanged' AND aggregate_id = $1
			  AND payload -> 'data' ->> 'transactionId' = $2`, w.ID, bet.TransactionID).Scan(&payload); err != nil {
			t.Fatalf("read the event of the bet: %v", err)
		}
		var envelope struct {
			Data struct {
				BalanceAfter  moneyBody `json:"balanceAfter"`
				WalletVersion int64     `json:"walletVersion"`
			} `json:"data"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			t.Fatal(err)
		}
		return envelope.Data.BalanceAfter.Amount, envelope.Data.WalletVersion
	}

	before, versionBefore := read()
	core.RequireStatus(t, submit(t, w.operation("WIN", "225.00")), http.StatusOK)
	core.RequireStatus(t, submit(t, w.operation("BET", "100.00")), http.StatusOK)
	after, versionAfter := read()

	if before != "975.00" || versionBefore != 2 || after != before || versionAfter != versionBefore {
		t.Fatalf("payload read %s at version %d, then %s at version %d: it must not change", before, versionBefore, after, versionAfter)
	}
}

// Scenario: The events of one aggregate are published in the order they occurred
//
//	Given three publishers running
//	And a wallet that is opened and then bet on five times
//	When its events are published
//	Then the consumer receives the balance changes in wallet version order
func TestTheEventsOfOneAggregateArePublishedInTheOrderTheyOccurred(t *testing.T) {
	stack.StartPublisher(t, "publisher-2")
	stack.StartPublisher(t, "publisher-3")
	w := newWallet(t, "1000.00")
	for range 5 {
		core.RequireStatus(t, submit(t, w.operation("BET", "10.00")), http.StatusOK)
	}

	rows, err := stack.DB(t).Query(context.Background(), `
		SELECT event_id, (payload -> 'data' ->> 'walletVersion')::int
		FROM outbox_events WHERE event_type = 'WalletBalanceChanged' AND aggregate_id = $1
		ORDER BY 2`, w.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var sequence []int
	for rows.Next() {
		var id string
		var version int
		if err := rows.Scan(&id, &version); err != nil {
			t.Fatal(err)
		}
		delivery := stack.WaitForEvent(t, id, 30*time.Second)
		sequence = append(sequence, delivery.Sequence)
	}
	if len(sequence) != 6 {
		t.Fatalf("expected 6 balance changes (the opening and five bets), found %d", len(sequence))
	}
	for i := 1; i < len(sequence); i++ {
		if sequence[i] < sequence[i-1] {
			t.Fatalf("the consumer received the events out of order: arrival sequence by wallet version is %v", sequence)
		}
	}
}

// Scenario: A failed event holds back the later events of its aggregate
//
//	Given two balance changes of one wallet, the first refused by the broker three times
//	Then the second is not published while the first is failing
//	And once the first is published the second follows it
func TestAFailedEventHoldsBackTheLaterEventsOfItsAggregate(t *testing.T) {
	opening := openWallet(t, "100.00")
	first := processedBalanceChange(t, opening, 1, time.Now())
	stack.Faults.FailPublishing(first.ID().String(), 3)

	later, err := entities.NewLedgerEntry(uuid.New(), opening.Wallet.ID(), opening.Transaction.ID(),
		entities.DirectionDebit, money(t, "10.00"), money(t, "100.00"), money(t, "90.00"), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	second, err := entities.NewWalletBalanceChangedEvent(uuid.New(), later, 2, "corr-"+uuid.NewString(), "", time.Now().Add(5*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}

	err = stack.Repos.UnitOfWork.Atomic(context.Background(), func(ctx context.Context) error {
		if err := stack.Repos.Wallets.Insert(ctx, opening.Wallet); err != nil {
			return err
		}
		if err := stack.Repos.Wagering.Insert(ctx, opening.Transaction); err != nil {
			return err
		}
		if err := stack.Repos.Wallets.InsertEntry(ctx, *opening.Entry); err != nil {
			return err
		}
		if err := stack.Repos.Outbox.Insert(ctx, first); err != nil {
			return err
		}
		return stack.Repos.Outbox.Insert(ctx, second)
	})
	if err != nil {
		t.Fatalf("write the events: %v", err)
	}

	// The first is refused, waits, is refused again: the second must not overtake it.
	time.Sleep(1200 * time.Millisecond)
	if got := stack.EventDeliveries(second.ID().String()); len(got) != 0 {
		t.Fatal("the later event was published while the earlier one of its aggregate was failing")
	}

	firstDelivery := stack.WaitForEvent(t, first.ID().String(), publishTimeout)
	secondDelivery := stack.WaitForEvent(t, second.ID().String(), publishTimeout)
	if secondDelivery.Sequence < firstDelivery.Sequence {
		t.Fatalf("the later event arrived first: %d before %d", secondDelivery.Sequence, firstDelivery.Sequence)
	}
}

// processedBalanceChange builds the opening's balance change at a given version and time.
func processedBalanceChange(t *testing.T, opening entities.WalletOpening, version int64, at time.Time) *entities.OutboxEvent {
	t.Helper()

	event, err := entities.NewWalletBalanceChangedEvent(uuid.New(), *opening.Entry, version, "corr-"+uuid.NewString(), "", at)
	if err != nil {
		t.Fatalf("build the event: %v", err)
	}
	return event
}
