//go:build e2e

package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Concurrent writers on one wallet
//
//	As the platform, I want per-wallet coordination without a global lock,
//	so that money stays correct while unrelated wallets keep running in parallel.
//
//	Every scenario ends with the closing check of the challenge: the stored balance is the sum of
//	the credits minus the sum of the debits of the ledger.
//
//	Scenarios:
//	  - Fifty parallel submissions of the same bet produce a single debit
//	  - Two competing bets of eighty over one hundred leave twenty
//	  - Distinct wallets are processed in parallel
//	  - The eighty-eighty race holds across three independent instances
//	  - Concurrent HTTP and SQS submissions of one operation apply it once

// outcome is what one concurrent call came to. The calls run outside the test goroutine, where a
// test may not fail, so the error travels with the result.
type outcome struct {
	status   int
	response transactionResponse
	err      error
}

// call posts an operation to an instance as provider-a, under the idempotency key the challenge
// suggests. It never fails the test: it is meant to run in its own goroutine.
func call(baseURL, token string, body submitBody) outcome {
	encoded, err := json.Marshal(body)
	if err != nil {
		return outcome{err: err}
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost,
		baseURL+"/wagering/transactions", bytes.NewReader(encoded))
	if err != nil {
		return outcome{err: err}
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", body.ProviderID+":"+body.ExternalTransactionID)

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return outcome{err: err}
	}
	defer func() { _ = res.Body.Close() }()

	got := outcome{status: res.StatusCode}
	if err := json.NewDecoder(res.Body).Decode(&got.response); err != nil {
		got.err = fmt.Errorf("decode the response (status %d): %w", res.StatusCode, err)
	}
	return got
}

// inParallel starts every call together and returns their outcomes in order. The calls are held at
// a barrier so they all leave at the same moment, which is what makes the race real.
func inParallel(t *testing.T, calls ...func() outcome) []outcome {
	t.Helper()

	results := make([]outcome, len(calls))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, fn := range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			results[i] = fn()
		}()
	}
	close(start)
	wg.Wait()

	for i, got := range results {
		if got.err != nil {
			t.Fatalf("call %d: %v", i, got.err)
		}
	}
	return results
}

// Scenario: Fifty parallel submissions of the same bet produce a single debit
//
//	Given a wallet with balance 1000.00 BRL
//	When the same bet is submitted fifty times in parallel
//	Then exactly one transaction and one ledger entry exist
//	And the balance is 975.00 BRL
func TestFiftyParallelSubmissionsOfTheSameBetProduceASingleDebit(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	token := stack.ClientToken(t, core.ProviderA)

	calls := make([]func() outcome, 50)
	for i := range calls {
		calls[i] = func() outcome { return call(stack.BaseURL, token, body) }
	}
	results := inParallel(t, calls...)

	originals := 0
	ids := map[string]bool{}
	for _, got := range results {
		if got.status != http.StatusOK || got.response.Status != "PROCESSED" {
			t.Fatalf("a submission answered %d %+v", got.status, got.response)
		}
		ids[got.response.TransactionID] = true
		if !got.response.IdempotentReplay {
			originals++
		}
	}
	if originals != 1 || len(ids) != 1 {
		t.Fatalf("%d submissions claim to be the original over %d transactions, want 1 over 1", originals, len(ids))
	}
	state := walletState(t, w.ID)
	// The opening credit and the one debit.
	if state.balanceMinor != 97500 || state.entries != 2 || state.debits != 2500 || transactionsOf(t, w.ID) != 1 {
		t.Fatalf("state = %+v, transactions = %d", state, transactionsOf(t, w.ID))
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: Two competing bets of eighty over one hundred leave twenty
//
//	Given a wallet with balance 100.00 BRL
//	When two distinct bets of 80.00 BRL are submitted at the same time
//	Then one is PROCESSED and one is REJECTED with INSUFFICIENT_FUNDS
//	And the balance is 20.00 BRL with exactly one DEBIT entry
//	And resubmitting both changes nothing
func TestTwoCompetingBetsOfEightyOverOneHundredLeaveTwenty(t *testing.T) {
	w := newWallet(t, "100.00")
	first, second := w.operation("BET", "80.00"), w.operation("BET", "80.00")
	token := stack.ClientToken(t, core.ProviderA)

	results := inParallel(t,
		func() outcome { return call(stack.BaseURL, token, first) },
		func() outcome { return call(stack.BaseURL, token, second) })

	requireOneWonTheFunds(t, results)
	state := walletState(t, w.ID)
	if state.balanceMinor != 2000 || state.debits != 8000 {
		t.Fatalf("state = %+v, want balance 2000 after a single debit of 8000", state)
	}

	replays := inParallel(t,
		func() outcome { return call(stack.BaseURL, token, first) },
		func() outcome { return call(stack.BaseURL, token, second) })
	for i, replay := range replays {
		if !replay.response.IdempotentReplay || replay.status != results[i].status ||
			replay.response.TransactionID != results[i].response.TransactionID {
			t.Fatalf("replay %d = %d %+v, original = %d %+v", i, replay.status, replay.response, results[i].status, results[i].response)
		}
	}
	if after := walletState(t, w.ID); after != state {
		t.Fatalf("resubmitting changed the wallet: before %+v, after %+v", state, after)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// requireOneWonTheFunds asserts the outcome of two bets that could not both be afforded: one was
// processed, the other rejected for lack of funds.
func requireOneWonTheFunds(t *testing.T, results []outcome) {
	t.Helper()

	processed, refused := 0, 0
	for _, got := range results {
		switch {
		case got.status == http.StatusOK && got.response.Status == "PROCESSED":
			processed++
		case got.status == http.StatusUnprocessableEntity && got.response.FailureCode == "INSUFFICIENT_FUNDS":
			refused++
		default:
			t.Fatalf("an unexpected outcome: %d %+v", got.status, got.response)
		}
	}
	if processed != 1 || refused != 1 {
		t.Fatalf("%d processed and %d refused, want 1 and 1", processed, refused)
	}
}

// Scenario: Distinct wallets are processed in parallel
//
//	Given the wallet of one player is locked by another writer
//	When a bet on that wallet waits for it
//	And operations on many distinct wallets are submitted at the same time
//	Then every one of them is processed while the first is still waiting
//	And once the lock is released the waiting bet is processed too
func TestDistinctWalletsAreProcessedInParallel(t *testing.T) {
	blocked := newWallet(t, "100.00")
	tx, err := stack.DB(t).Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(context.Background(), "SELECT id FROM wallets WHERE id = $1 FOR UPDATE", blocked.ID); err != nil {
		t.Fatalf("lock the wallet: %v", err)
	}

	token := stack.ClientToken(t, core.ProviderA)
	waiting := make(chan outcome, 1)
	go func() { waiting <- call(stack.BaseURL, token, blocked.operation("BET", "10.00")) }()

	others := make([]wallet, 30)
	calls := make([]func() outcome, len(others))
	for i := range others {
		others[i] = newWallet(t, "100.00")
		body := others[i].operation("BET", "10.00")
		calls[i] = func() outcome { return call(stack.BaseURL, token, body) }
	}
	results := inParallel(t, calls...)

	for _, got := range results {
		if got.status != http.StatusOK || got.response.Status != "PROCESSED" {
			t.Fatalf("an operation on a free wallet answered %d %+v", got.status, got.response)
		}
	}
	select {
	case got := <-waiting:
		t.Fatalf("the bet on the locked wallet finished while the lock was held: %d %+v", got.status, got.response)
	default:
	}

	if err := tx.Rollback(context.Background()); err != nil {
		t.Fatalf("release the lock: %v", err)
	}
	select {
	case got := <-waiting:
		if got.err != nil || got.status != http.StatusOK || got.response.Status != "PROCESSED" {
			t.Fatalf("the waiting bet answered %d %+v (%v)", got.status, got.response, got.err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("the bet on the wallet did not proceed after the lock was released")
	}
	for _, other := range append(others, blocked) {
		requireLedgerMatchesBalance(t, other.ID)
	}
}

// Scenario: The eighty-eighty race holds across three independent instances
//
//	Given three instances with their own pools and memory
//	And a wallet with balance 100.00 BRL
//	When two competing bets of 80.00 BRL reach them, one of them on two instances
//	Then the outcome is one processed, one rejected and a balance of 20.00
//	And the instances that saw the same bet agree about it
func TestTheEightyEightyRaceHoldsAcrossThreeIndependentInstances(t *testing.T) {
	instances := []*core.Instance{
		stack.StartServer(t, "instance-a"),
		stack.StartServer(t, "instance-b"),
		stack.StartServer(t, "instance-c"),
	}
	w := newWallet(t, "100.00")
	first, second := w.operation("BET", "80.00"), w.operation("BET", "80.00")
	token := stack.ClientToken(t, core.ProviderA)

	results := inParallel(t,
		func() outcome { return call(instances[0].BaseURL, token, first) },
		func() outcome { return call(instances[1].BaseURL, token, second) },
		func() outcome { return call(instances[2].BaseURL, token, first) })

	requireOneWonTheFunds(t, []outcome{results[0], results[1]})
	if results[0].status != results[2].status || results[0].response.TransactionID != results[2].response.TransactionID {
		t.Fatalf("two instances disagree about the same bet: %d %+v and %d %+v",
			results[0].status, results[0].response, results[2].status, results[2].response)
	}
	state := walletState(t, w.ID)
	if state.balanceMinor != 2000 || state.debits != 8000 {
		t.Fatalf("state = %+v, want balance 2000 after a single debit of 8000", state)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: Concurrent HTTP and SQS submissions of one operation apply it once
//
//	Given a wallet with balance 1000.00 BRL
//	When the same operation is posted over HTTP and published to the queue at the same time
//	Then exactly one ledger entry exists and both callers see a consistent outcome
func TestConcurrentHTTPAndSQSSubmissionsOfOneOperationApplyItOnce(t *testing.T) {
	token := stack.ClientToken(t, core.ProviderA)

	// Repeated on fresh wallets: one race that happens to resolve the same way says little.
	for round := range 5 {
		w := newWallet(t, "1000.00")
		body := w.operation("BET", "25.00")
		message := w.queuedWith("msg-"+uuid.NewString(), body.ExternalTransactionID, "BET", "25.00")

		viaHTTP := make(chan outcome, 1)
		go func() { viaHTTP <- call(stack.BaseURL, token, body) }()
		w.send(t, message)

		got := <-viaHTTP
		if got.err != nil || got.status != http.StatusOK || got.response.Status != "PROCESSED" {
			t.Fatalf("round %d: the HTTP caller saw %d %+v (%v)", round, got.status, got.response, got.err)
		}
		if queued := waitForTransaction(t, body.ExternalTransactionID); queued.id != got.response.TransactionID {
			t.Fatalf("round %d: the queue consumer saw transaction %s, the HTTP caller %s", round, queued.id, got.response.TransactionID)
		}
		stack.WaitForEmptyQueue(t, consumeTimeout)

		state := walletState(t, w.ID)
		if state.balanceMinor != 97500 || state.entries != 2 || transactionsOf(t, w.ID) != 1 {
			t.Fatalf("round %d: state = %+v, transactions = %d", round, state, transactionsOf(t, w.ID))
		}
		requireLedgerMatchesBalance(t, w.ID)
	}
}
