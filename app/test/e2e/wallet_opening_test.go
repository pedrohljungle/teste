//go:build e2e

package e2e

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Wallet opening
//
//	As the internal wallet service, I want a wallet and its opening credit committed together,
//	so that no player ever starts with an unaudited balance.
//
//	Every scenario authenticates as the internal service with a real token from the realm, and
//	asserts on the database afterwards: what the response says is only half of what a commit
//	guarantees.
//
//	Scenarios:
//	  - Opening with a positive balance creates the credit, the ledger and the events
//	  - Opening with a zero balance creates no transaction and no ledger
//	  - A second wallet for the same player and currency conflicts
//	  - A wallet for the same player in another currency is accepted
//	  - The events of an opening carry the request id as their correlation
//	  - A player id that is not a UUID is refused
//	  - A balance with more than two decimal places is refused, not rounded
//	  - A negative initial balance is refused
//	  - A balance sent as a JSON number is refused
//	  - An unknown currency is refused

type openBody struct {
	PlayerID       string    `json:"playerId"`
	InitialBalance moneyBody `json:"initialBalance"`
}

type moneyBody struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type walletResponse struct {
	ID       string    `json:"id"`
	PlayerID string    `json:"playerId"`
	Balance  moneyBody `json:"balance"`
	Version  int64     `json:"version"`
}

type errorResponse struct {
	Message     string `json:"message"`
	FailureCode string `json:"failureCode"`
}

// openWalletOverHTTP posts as the internal service and returns the raw response.
func openWalletOverHTTP(t *testing.T, playerID, amount, currency string) *http.Response {
	t.Helper()

	return stack.Request(t, http.MethodPost, "/wallets", stack.ClientToken(t, core.InternalService),
		openBody{PlayerID: playerID, InitialBalance: moneyBody{Amount: amount, Currency: currency}})
}

func walletsOf(t *testing.T, playerID string) int {
	t.Helper()
	return count(t, stack.DB(t), "SELECT count(*) FROM wallets WHERE player_id = $1", playerID)
}

// Scenario: Opening with a positive balance creates the credit, the ledger and the events
//
//	Given no wallet exists for the player and currency
//	When the internal service posts /wallets with an initial balance of 1000.00 BRL
//	Then the wallet is created at version 1 with balance 1000.00 BRL
//	And a PROCESSED OPENING transaction exists carrying no provider metadata
//	And exactly one CREDIT entry exists with balanceBefore 0.00 and balanceAfter 1000.00
//	And WagerTransactionProcessed and WalletBalanceChanged are in the outbox
func TestOpeningWithAPositiveBalanceCreatesTheCreditTheLedgerAndTheEvents(t *testing.T) {
	playerID := uuid.NewString()

	res := core.KeepStatus(t, openWalletOverHTTP(t, playerID, "1000.00", "BRL"), http.StatusCreated)
	wallet := core.Decode[walletResponse](t, res)

	if wallet.PlayerID != playerID || wallet.Balance.Amount != "1000.00" || wallet.Balance.Currency != "BRL" || wallet.Version != 1 {
		t.Fatalf("response = %+v", wallet)
	}
	if _, err := uuid.Parse(wallet.ID); err != nil {
		t.Fatalf("the wallet id is not a UUID: %q", wallet.ID)
	}

	db := stack.DB(t)
	ctx := context.Background()

	var balance, version int64
	if err := db.QueryRow(ctx, "SELECT balance_minor, version FROM wallets WHERE id = $1", wallet.ID).Scan(&balance, &version); err != nil {
		t.Fatalf("read the wallet: %v", err)
	}
	if balance != 100000 || version != 1 {
		t.Fatalf("stored wallet: balance %d at version %d", balance, version)
	}

	var (
		transactionID                                   uuid.UUID
		kind, status, origin                            string
		provider, external, key, round, game, reference *string
		payloadHash                                     []byte
		resultBalance, resultVersion                    int64
	)
	if err := db.QueryRow(ctx, `
		SELECT id, kind, status, origin, provider_id, external_transaction_id, idempotency_key,
		       payload_hash, round_id, game_id, reference_external_transaction_id,
		       result_balance_minor, result_wallet_version
		FROM wager_transactions WHERE wallet_id = $1`, wallet.ID).Scan(
		&transactionID, &kind, &status, &origin, &provider, &external, &key,
		&payloadHash, &round, &game, &reference, &resultBalance, &resultVersion); err != nil {
		t.Fatalf("read the opening transaction: %v", err)
	}
	if kind != "OPENING" || status != "PROCESSED" || origin != "INTERNAL" {
		t.Fatalf("opening = %s, %s, %s", kind, status, origin)
	}
	if provider != nil || external != nil || key != nil || payloadHash != nil || round != nil || game != nil || reference != nil {
		t.Fatalf("an opening must carry no external metadata")
	}
	if resultBalance != 100000 || resultVersion != 1 {
		t.Fatalf("result = %d at version %d", resultBalance, resultVersion)
	}

	var entries int
	var direction string
	var before, after int64
	if err := db.QueryRow(ctx, `
		SELECT count(*) OVER (), direction, balance_before_minor, balance_after_minor
		FROM wallet_ledger_entries WHERE wallet_id = $1`, wallet.ID).Scan(&entries, &direction, &before, &after); err != nil {
		t.Fatalf("read the ledger: %v", err)
	}
	if entries != 1 || direction != "CREDIT" || before != 0 || after != 100000 {
		t.Fatalf("ledger: %d entries, %s from %d to %d", entries, direction, before, after)
	}

	types := map[string]uuid.UUID{}
	rows, err := db.Query(ctx, `SELECT event_type, aggregate_id FROM outbox_events WHERE aggregate_id IN ($1, $2)`, transactionID, wallet.ID)
	if err != nil {
		t.Fatalf("read the outbox: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var eventType string
		var aggregate uuid.UUID
		if err := rows.Scan(&eventType, &aggregate); err != nil {
			t.Fatal(err)
		}
		types[eventType] = aggregate
	}
	if len(types) != 2 || types["WagerTransactionProcessed"] != transactionID || types["WalletBalanceChanged"].String() != wallet.ID {
		t.Fatalf("outbox events = %v", types)
	}
}

// Scenario: Opening with a zero balance creates no transaction and no ledger
//
//	Given no wallet exists for the player and currency
//	When the internal service posts /wallets with an initial balance of 0.00 BRL
//	Then the wallet is created at version 1 with balance 0.00 BRL
//	And no OPENING transaction, no ledger entry and no financial event exist
func TestOpeningWithAZeroBalanceCreatesNeitherTransactionNorLedger(t *testing.T) {
	playerID := uuid.NewString()

	res := core.KeepStatus(t, openWalletOverHTTP(t, playerID, "0.00", "BRL"), http.StatusCreated)
	wallet := core.Decode[walletResponse](t, res)

	if wallet.Balance.Amount != "0.00" || wallet.Version != 1 {
		t.Fatalf("response = %+v", wallet)
	}
	db := stack.DB(t)
	if got := count(t, db, "SELECT count(*) FROM wager_transactions WHERE wallet_id = $1", wallet.ID); got != 0 {
		t.Errorf("%d transactions were created for a zero opening", got)
	}
	if got := count(t, db, "SELECT count(*) FROM wallet_ledger_entries WHERE wallet_id = $1", wallet.ID); got != 0 {
		t.Errorf("%d ledger entries were created for a zero opening", got)
	}
	if got := count(t, db, "SELECT count(*) FROM outbox_events WHERE aggregate_id = $1", wallet.ID); got != 0 {
		t.Errorf("%d events were created for a zero opening", got)
	}
}

// Scenario: A second wallet for the same player and currency conflicts
//
//	Given a wallet exists for the player in BRL
//	When the internal service posts /wallets for the same player in BRL
//	Then the response is 409 and only one wallet exists
//	And the first wallet keeps its balance
func TestASecondWalletForTheSamePlayerAndCurrencyConflicts(t *testing.T) {
	playerID := uuid.NewString()
	first := core.Decode[walletResponse](t, core.KeepStatus(t, openWalletOverHTTP(t, playerID, "100.00", "BRL"), http.StatusCreated))

	res := openWalletOverHTTP(t, playerID, "999.00", "BRL")

	core.RequireStatus(t, res, http.StatusConflict)
	if got := walletsOf(t, playerID); got != 1 {
		t.Fatalf("the player holds %d wallets, want 1", got)
	}
	var balance int64
	if err := stack.DB(t).QueryRow(context.Background(), "SELECT balance_minor FROM wallets WHERE id = $1", first.ID).Scan(&balance); err != nil {
		t.Fatal(err)
	}
	if balance != 10000 {
		t.Fatalf("the first wallet's balance became %d", balance)
	}
}

// Scenario: A wallet for the same player in another currency is accepted
//
//	Given a wallet exists for the player in BRL
//	When the internal service posts /wallets for the same player in USD
//	Then the response is 201 and the player holds two wallets
func TestAWalletForTheSamePlayerInAnotherCurrencyIsAccepted(t *testing.T) {
	playerID := uuid.NewString()
	core.KeepStatus(t, openWalletOverHTTP(t, playerID, "0.00", "BRL"), http.StatusCreated).Body.Close()

	core.RequireStatus(t, openWalletOverHTTP(t, playerID, "0.00", "USD"), http.StatusCreated)

	if got := walletsOf(t, playerID); got != 2 {
		t.Fatalf("the player holds %d wallets, want 2", got)
	}
}

// Scenario: The events of an opening carry the request id as their correlation
//
//	Given a request id chosen by the caller
//	When the internal service opens a wallet with a positive balance under that request id
//	Then both events of the opening carry it as their correlation id
//	And the balance event names the opening transaction as its cause
func TestTheEventsOfAnOpeningCarryTheRequestIdAsTheirCorrelation(t *testing.T) {
	requestID := "req-" + uuid.NewString()
	playerID := uuid.NewString()

	res := stack.RequestWithHeaders(t, http.MethodPost, "/wallets", stack.ClientToken(t, core.InternalService),
		openBody{PlayerID: playerID, InitialBalance: moneyBody{Amount: "50.00", Currency: "BRL"}},
		map[string]string{"X-Request-Id": requestID})
	wallet := core.Decode[walletResponse](t, core.KeepStatus(t, res, http.StatusCreated))

	db := stack.DB(t)
	if got := count(t, db, "SELECT count(*) FROM outbox_events WHERE correlation_id = $1", requestID); got != 2 {
		t.Fatalf("%d events carry the request id, want 2", got)
	}
	var causation string
	var payload []byte
	if err := db.QueryRow(context.Background(),
		"SELECT causation_id, payload FROM outbox_events WHERE event_type = 'WalletBalanceChanged' AND aggregate_id = $1", wallet.ID).
		Scan(&causation, &payload); err != nil {
		t.Fatalf("read the balance event: %v", err)
	}
	var envelope struct {
		CorrelationID string `json:"correlationId"`
		Data          struct {
			TransactionID string `json:"transactionId"`
			WalletVersion int64  `json:"walletVersion"`
			Direction     string `json:"direction"`
		} `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		t.Fatalf("the payload is not JSON: %v", err)
	}
	if envelope.CorrelationID != requestID || causation != envelope.Data.TransactionID ||
		envelope.Data.WalletVersion != 1 || envelope.Data.Direction != "CREDIT" {
		t.Fatalf("envelope = %+v, causation %q", envelope, causation)
	}
}

// Scenario: A player id that is not a UUID is refused
//
//	When the internal service posts /wallets with the player id "player-1"
//	Then the response is 400 and no wallet is created
func TestAPlayerIdThatIsNotAUUIDIsRefused(t *testing.T) {
	core.RequireStatus(t, openWalletOverHTTP(t, "player-1", "10.00", "BRL"), http.StatusBadRequest)
}

// Scenario: A balance with more than two decimal places is refused, not rounded
//
//	When the internal service posts /wallets with an initial balance of 10.005 BRL
//	Then the response is 400 with failureCode INVALID_AMOUNT and no wallet is created
func TestABalanceWithMoreThanTwoDecimalPlacesIsRefusedNotRounded(t *testing.T) {
	playerID := uuid.NewString()

	res := openWalletOverHTTP(t, playerID, "10.005", "BRL")

	failure := core.Decode[errorResponse](t, core.KeepStatus(t, res, http.StatusBadRequest))
	if failure.FailureCode != "INVALID_AMOUNT" {
		t.Fatalf("failureCode = %q, want INVALID_AMOUNT (%s)", failure.FailureCode, failure.Message)
	}
	if got := walletsOf(t, playerID); got != 0 {
		t.Fatalf("%d wallets were created from an invalid amount", got)
	}
}

// Scenario: A negative initial balance is refused
//
//	When the internal service posts /wallets with an initial balance of -1.00 BRL
//	Then the response is 400 and no wallet is created
func TestANegativeInitialBalanceIsRefused(t *testing.T) {
	playerID := uuid.NewString()

	core.RequireStatus(t, openWalletOverHTTP(t, playerID, "-1.00", "BRL"), http.StatusBadRequest)

	if got := walletsOf(t, playerID); got != 0 {
		t.Fatalf("%d wallets were created from a negative balance", got)
	}
}

// Scenario: A balance sent as a JSON number is refused
//
//	When the internal service posts /wallets with the amount as the JSON number 10.00
//	Then the response is 400 and no wallet is created
func TestABalanceSentAsAJSONNumberIsRefused(t *testing.T) {
	playerID := uuid.NewString()
	body := `{"playerId":"` + playerID + `","initialBalance":{"amount":10.00,"currency":"BRL"}}`

	res := stack.RawRequest(t, http.MethodPost, "/wallets", stack.ClientToken(t, core.InternalService), body)

	core.RequireStatus(t, res, http.StatusBadRequest)
	if got := walletsOf(t, playerID); got != 0 {
		t.Fatalf("%d wallets were created from a numeric amount", got)
	}
}

// Scenario: An unknown currency is refused
//
//	When the internal service posts /wallets with the currency "ZZZ"
//	Then the response is 400 and no wallet is created
func TestAnUnknownCurrencyIsRefused(t *testing.T) {
	playerID := uuid.NewString()

	core.RequireStatus(t, openWalletOverHTTP(t, playerID, "10.00", "ZZZ"), http.StatusBadRequest)

	if got := walletsOf(t, playerID); got != 0 {
		t.Fatalf("%d wallets were created with an unknown currency", got)
	}
}
