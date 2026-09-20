//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Submitting an operation over HTTP
//
//	As a game provider, I want each operation applied exactly once with a truthful answer,
//	so that my ledger and the wallet never disagree.
//
//	Every scenario authenticates as provider-a with a real token, opens its own wallet through the
//	API and asserts on the database afterwards. The final check of the money scenarios is the one
//	the challenge asks for: the stored balance equals the credits minus the debits of the ledger.
//
//	Scenarios:
//	  - A bet is processed and debits the wallet
//	  - A bet without sufficient balance is rejected and moves nothing
//	  - A win is processed and credits the wallet
//	  - A loss is processed without touching the balance, the version or the ledger
//	  - A loss carrying a non-zero amount is rejected
//	  - A bet of zero is rejected
//	  - A request without the Idempotency-Key header is refused
//	  - An amount with more than two decimals is refused without rounding
//	  - An operation in another currency than the wallet is rejected
//	  - An operation for an unknown wallet is rejected
//	  - An operation naming another player than the wallet owner is rejected
//	  - The OPENING kind submitted over the wagering API is refused

type submitBody struct {
	ProviderID                     string    `json:"providerId"`
	ExternalTransactionID          string    `json:"externalTransactionId"`
	PlayerID                       string    `json:"playerId"`
	WalletID                       string    `json:"walletId"`
	RoundID                        string    `json:"roundId"`
	GameID                         string    `json:"gameId"`
	Kind                           string    `json:"kind"`
	Money                          moneyBody `json:"money"`
	ReferenceExternalTransactionID string    `json:"referenceExternalTransactionId,omitempty"`
}

type transactionResponse struct {
	TransactionID    string     `json:"transactionId"`
	Status           string     `json:"status"`
	Balance          *moneyBody `json:"balance"`
	FailureCode      string     `json:"failureCode"`
	IdempotentReplay bool       `json:"idempotentReplay"`
}

// wallet is a wallet opened through the API for one scenario.
type wallet struct {
	ID       string
	PlayerID string
}

// newWallet opens a wallet in BRL with the given balance as the internal service does.
func newWallet(t *testing.T, balance string) wallet {
	t.Helper()

	playerID := uuid.NewString()
	res := core.KeepStatus(t, openWalletOverHTTP(t, playerID, balance, "BRL"), http.StatusCreated)
	opened := core.Decode[walletResponse](t, res)
	return wallet{ID: opened.ID, PlayerID: playerID}
}

// operation builds an operation of the given kind for the wallet, from provider-a, with a fresh
// external transaction id.
func (w wallet) operation(kind, amount string) submitBody {
	return submitBody{
		ProviderID:            core.ProviderA,
		ExternalTransactionID: "ext-" + uuid.NewString(),
		PlayerID:              w.PlayerID,
		WalletID:              w.ID,
		RoundID:               "round-987",
		GameID:                "fortune-chimp",
		Kind:                  kind,
		Money:                 moneyBody{Amount: amount, Currency: "BRL"},
	}
}

// submitAs posts an operation as the given identity under an idempotency key.
func submitAs(t *testing.T, identity, key string, body submitBody) *http.Response {
	t.Helper()

	headers := map[string]string{}
	if key != "" {
		headers["Idempotency-Key"] = key
	}
	return stack.RequestWithHeaders(t, http.MethodPost, "/wagering/transactions",
		stack.ClientToken(t, identity), body, headers)
}

// submit posts an operation as provider-a, keyed the way the challenge suggests.
func submit(t *testing.T, body submitBody) *http.Response {
	t.Helper()
	return submitAs(t, core.ProviderA, body.ProviderID+":"+body.ExternalTransactionID, body)
}

// stored is the state of a wallet as the database holds it.
type stored struct {
	balanceMinor int64
	version      int64
	entries      int
	credits      int64
	debits       int64
}

func walletState(t *testing.T, walletID string) stored {
	t.Helper()

	var s stored
	db := stack.DB(t)
	if err := db.QueryRow(context.Background(),
		"SELECT balance_minor, version FROM wallets WHERE id = $1", walletID).Scan(&s.balanceMinor, &s.version); err != nil {
		t.Fatalf("read the wallet: %v", err)
	}
	if err := db.QueryRow(context.Background(), `
		SELECT count(*),
		       coalesce(sum(amount_minor) FILTER (WHERE direction = 'CREDIT'), 0),
		       coalesce(sum(amount_minor) FILTER (WHERE direction = 'DEBIT'), 0)
		FROM wallet_ledger_entries WHERE wallet_id = $1`, walletID).Scan(&s.entries, &s.credits, &s.debits); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	return s
}

// requireLedgerMatchesBalance is the closing check of every scenario that moves money: the stored
// balance is the credits minus the debits of the ledger.
func requireLedgerMatchesBalance(t *testing.T, walletID string) {
	t.Helper()

	s := walletState(t, walletID)
	if s.credits-s.debits != s.balanceMinor {
		t.Fatalf("the stored balance %d differs from the ledger: credits %d minus debits %d = %d",
			s.balanceMinor, s.credits, s.debits, s.credits-s.debits)
	}
}

func eventsOf(t *testing.T, aggregateIDs ...string) map[string]int {
	t.Helper()

	types := map[string]int{}
	for _, aggregate := range aggregateIDs {
		rows, err := stack.DB(t).Query(context.Background(),
			"SELECT event_type FROM outbox_events WHERE aggregate_id = $1", aggregate)
		if err != nil {
			t.Fatalf("read the outbox: %v", err)
		}
		for rows.Next() {
			var eventType string
			if err := rows.Scan(&eventType); err != nil {
				t.Fatal(err)
			}
			types[eventType]++
		}
		rows.Close()
	}
	return types
}

func transactionsOf(t *testing.T, walletID string) int {
	t.Helper()
	return count(t, stack.DB(t), "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1 AND origin = 'EXTERNAL'", walletID)
}

// Scenario: A bet is processed and debits the wallet
//
//	Given a wallet with balance 1000.00 BRL at version 1
//	When the provider posts a BET of 25.00 BRL with an idempotency key
//	Then the response is 200 with status PROCESSED, balance 975.00 and idempotentReplay false
//	And the wallet is at version 2
//	And exactly one DEBIT entry exists with balanceBefore 1000.00 and balanceAfter 975.00
func TestABetIsProcessedAndDebitsTheWallet(t *testing.T) {
	w := newWallet(t, "1000.00")

	res := core.KeepStatus(t, submit(t, w.operation("BET", "25.00")), http.StatusOK)
	got := core.Decode[transactionResponse](t, res)

	if got.Status != "PROCESSED" || got.IdempotentReplay || got.Balance == nil ||
		got.Balance.Amount != "975.00" || got.Balance.Currency != "BRL" {
		t.Fatalf("response = %+v", got)
	}
	if _, err := uuid.Parse(got.TransactionID); err != nil {
		t.Fatalf("the transaction id is not a UUID: %q", got.TransactionID)
	}
	s := walletState(t, w.ID)
	if s.balanceMinor != 97500 || s.version != 2 {
		t.Fatalf("wallet = %d at version %d", s.balanceMinor, s.version)
	}
	var direction string
	var before, after int64
	if err := stack.DB(t).QueryRow(context.Background(), `
		SELECT direction, balance_before_minor, balance_after_minor
		FROM wallet_ledger_entries WHERE transaction_id = $1`, got.TransactionID).Scan(&direction, &before, &after); err != nil {
		t.Fatalf("read the entry of the bet: %v", err)
	}
	if direction != "DEBIT" || before != 100000 || after != 97500 {
		t.Fatalf("entry = %s from %d to %d", direction, before, after)
	}
	if s.entries != 2 {
		t.Fatalf("the ledger holds %d entries, want the opening and the bet", s.entries)
	}
	// Looked up by the bet and by the wallet: the bet's own processed event, and the two balance
	// changes of the wallet, the opening credit and the bet debit.
	if events := eventsOf(t, got.TransactionID, w.ID); events["WagerTransactionProcessed"] != 1 || events["WalletBalanceChanged"] != 2 {
		t.Fatalf("events = %v", events)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A bet without sufficient balance is rejected and moves nothing
//
//	Given a wallet with balance 10.00 BRL at version 1
//	When the provider posts a BET of 25.00 BRL
//	Then the response is 422 with failureCode INSUFFICIENT_FUNDS
//	And the transaction is stored as REJECTED
//	And the balance, the version and the ledger are unchanged
//	And WagerTransactionRejected is in the outbox
func TestABetWithoutSufficientBalanceIsRejectedAndMovesNothing(t *testing.T) {
	w := newWallet(t, "10.00")
	before := walletState(t, w.ID)

	res := core.KeepStatus(t, submit(t, w.operation("BET", "25.00")), http.StatusUnprocessableEntity)
	got := core.Decode[transactionResponse](t, res)

	if got.Status != "REJECTED" || got.FailureCode != "INSUFFICIENT_FUNDS" || got.Balance != nil {
		t.Fatalf("response = %+v", got)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("a rejection changed the wallet: before %+v, after %+v", before, after)
	}
	var status, code string
	if err := stack.DB(t).QueryRow(context.Background(),
		"SELECT status, failure_code FROM wager_transactions WHERE id = $1", got.TransactionID).Scan(&status, &code); err != nil {
		t.Fatalf("read the rejected transaction: %v", err)
	}
	if status != "REJECTED" || code != "INSUFFICIENT_FUNDS" {
		t.Fatalf("stored = %s, %s", status, code)
	}
	if events := eventsOf(t, got.TransactionID); events["WagerTransactionRejected"] != 1 || events["WalletBalanceChanged"] != 0 {
		t.Fatalf("events = %v", events)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A win is processed and credits the wallet
//
//	Given a wallet with balance 100.00 BRL
//	When the provider posts a WIN of 40.00 BRL
//	Then the balance is 140.00 BRL and exactly one CREDIT entry exists for it
func TestAWinIsProcessedAndCreditsTheWallet(t *testing.T) {
	w := newWallet(t, "100.00")

	res := core.KeepStatus(t, submit(t, w.operation("WIN", "40.00")), http.StatusOK)
	got := core.Decode[transactionResponse](t, res)

	if got.Status != "PROCESSED" || got.Balance == nil || got.Balance.Amount != "140.00" {
		t.Fatalf("response = %+v", got)
	}
	var direction string
	if err := stack.DB(t).QueryRow(context.Background(),
		"SELECT direction FROM wallet_ledger_entries WHERE transaction_id = $1", got.TransactionID).Scan(&direction); err != nil {
		t.Fatalf("read the entry of the win: %v", err)
	}
	if direction != "CREDIT" {
		t.Fatalf("direction = %s", direction)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A loss is processed without touching the balance, the version or the ledger
//
//	Given a wallet with balance 100.00 BRL at version 1
//	When the provider posts a LOSS with money 0.00 BRL
//	Then the response is 200 with status PROCESSED and balance 100.00
//	And the balance stays 100.00 and the version stays 1
//	And no ledger entry is created for it
//	And WagerTransactionProcessed is in the outbox and WalletBalanceChanged is not
func TestALossIsProcessedWithoutTouchingTheBalanceTheVersionOrTheLedger(t *testing.T) {
	w := newWallet(t, "100.00")
	before := walletState(t, w.ID)

	res := core.KeepStatus(t, submit(t, w.operation("LOSS", "0.00")), http.StatusOK)
	got := core.Decode[transactionResponse](t, res)

	if got.Status != "PROCESSED" || got.Balance == nil || got.Balance.Amount != "100.00" {
		t.Fatalf("response = %+v", got)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("a loss changed the wallet: before %+v, after %+v", before, after)
	}
	if got := count(t, stack.DB(t), "SELECT count(*) FROM wallet_ledger_entries WHERE transaction_id = $1", got.TransactionID); got != 0 {
		t.Fatalf("a loss created %d ledger entries", got)
	}
	if events := eventsOf(t, got.TransactionID); events["WagerTransactionProcessed"] != 1 || events["WalletBalanceChanged"] != 0 {
		t.Fatalf("events = %v", events)
	}
}

// Scenario: A loss carrying a non-zero amount is rejected
//
//	When the provider posts a LOSS with money 5.00 BRL
//	Then the response is 422 with failureCode INVALID_AMOUNT
func TestALossCarryingANonZeroAmountIsRejected(t *testing.T) {
	w := newWallet(t, "100.00")

	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, w.operation("LOSS", "5.00")), http.StatusUnprocessableEntity))

	if got.Status != "REJECTED" || got.FailureCode != "INVALID_AMOUNT" {
		t.Fatalf("response = %+v", got)
	}
	requireLedgerMatchesBalance(t, w.ID)
}

// Scenario: A bet of zero is rejected
//
//	When the provider posts a BET with money 0.00 BRL
//	Then the response is 422 with failureCode INVALID_AMOUNT
func TestABetOfZeroIsRejected(t *testing.T) {
	w := newWallet(t, "100.00")

	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, w.operation("BET", "0.00")), http.StatusUnprocessableEntity))

	if got.Status != "REJECTED" || got.FailureCode != "INVALID_AMOUNT" {
		t.Fatalf("response = %+v", got)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 10000 || s.version != 1 {
		t.Fatalf("the wallet changed: %+v", s)
	}
}

// Scenario: A request without the Idempotency-Key header is refused
//
//	When the provider posts a BET with no Idempotency-Key header
//	Then the response is 400 and nothing is persisted
func TestARequestWithoutTheIdempotencyKeyHeaderIsRefused(t *testing.T) {
	w := newWallet(t, "100.00")

	core.RequireStatus(t, submitAs(t, core.ProviderA, "", w.operation("BET", "25.00")), http.StatusBadRequest)

	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d transactions were stored without an idempotency key", got)
	}
}

// Scenario: An amount with more than two decimals is refused without rounding
//
//	When the provider posts a BET of 25.001 BRL
//	Then the response is 400 and nothing is persisted
func TestAnAmountWithMoreThanTwoDecimalsIsRefusedWithoutRounding(t *testing.T) {
	w := newWallet(t, "100.00")

	failure := core.Decode[errorResponse](t, core.KeepStatus(t, submit(t, w.operation("BET", "25.001")), http.StatusBadRequest))

	if failure.FailureCode != "INVALID_AMOUNT" {
		t.Fatalf("failureCode = %q (%s)", failure.FailureCode, failure.Message)
	}
	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d transactions were stored for an invalid amount", got)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 10000 {
		t.Fatalf("the balance changed: %d", s.balanceMinor)
	}
}

// Scenario: An operation in another currency than the wallet is rejected
//
//	Given a wallet in BRL
//	When the provider posts a BET of 25.00 USD
//	Then the response is 422 with failureCode CURRENCY_MISMATCH
func TestAnOperationInAnotherCurrencyThanTheWalletIsRejected(t *testing.T) {
	w := newWallet(t, "100.00")
	body := w.operation("BET", "25.00")
	body.Money.Currency = "USD"

	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusUnprocessableEntity))

	if got.Status != "REJECTED" || got.FailureCode != "CURRENCY_MISMATCH" {
		t.Fatalf("response = %+v", got)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 10000 || s.version != 1 {
		t.Fatalf("the wallet changed: %+v", s)
	}
}

// Scenario: An operation for an unknown wallet is rejected
//
//	When the provider posts a BET for a wallet that does not exist
//	Then the response is 422 with failureCode WALLET_NOT_FOUND
//	And nothing is stored, because there is no wallet for a transaction to belong to
func TestAnOperationForAnUnknownWalletIsRejected(t *testing.T) {
	body := wallet{ID: uuid.NewString(), PlayerID: uuid.NewString()}.operation("BET", "25.00")

	failure := core.Decode[errorResponse](t, core.KeepStatus(t, submit(t, body), http.StatusUnprocessableEntity))

	if failure.FailureCode != "WALLET_NOT_FOUND" {
		t.Fatalf("failureCode = %q (%s)", failure.FailureCode, failure.Message)
	}
	if got := count(t, stack.DB(t), "SELECT count(*) FROM wager_transactions WHERE external_transaction_id = $1", body.ExternalTransactionID); got != 0 {
		t.Fatalf("%d transactions were stored for a wallet that does not exist", got)
	}
}

// Scenario: An operation naming another player than the wallet owner is rejected
//
//	Given a wallet that belongs to a player
//	When the provider posts a BET naming a different player for that wallet
//	Then the response is 422 with failureCode PLAYER_MISMATCH
//	And the wallet is unchanged
func TestAnOperationNamingAnotherPlayerThanTheWalletOwnerIsRejected(t *testing.T) {
	w := newWallet(t, "100.00")
	body := w.operation("BET", "25.00")
	body.PlayerID = uuid.NewString()

	got := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, body), http.StatusUnprocessableEntity))

	if got.Status != "REJECTED" || got.FailureCode != "PLAYER_MISMATCH" {
		t.Fatalf("response = %+v", got)
	}
	if s := walletState(t, w.ID); s.balanceMinor != 10000 || s.version != 1 {
		t.Fatalf("the wallet changed: %+v", s)
	}
}

// Scenario: The OPENING kind submitted over the wagering API is refused
//
//	Given a wallet with balance 100.00 BRL
//	When the provider posts an operation of kind OPENING
//	Then the response is 400 with failureCode OPENING_NOT_ALLOWED
//	And the balance, the version and the ledger are unchanged
func TestTheOpeningKindSubmittedOverTheWageringAPIIsRefused(t *testing.T) {
	w := newWallet(t, "100.00")
	before := walletState(t, w.ID)

	failure := core.Decode[errorResponse](t, core.KeepStatus(t, submit(t, w.operation("OPENING", "500.00")), http.StatusBadRequest))

	if failure.FailureCode != "OPENING_NOT_ALLOWED" {
		t.Fatalf("failureCode = %q (%s)", failure.FailureCode, failure.Message)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("an OPENING changed the wallet: before %+v, after %+v", before, after)
	}
	if got := transactionsOf(t, w.ID); got != 0 {
		t.Fatalf("%d external transactions were stored for an OPENING", got)
	}
}
