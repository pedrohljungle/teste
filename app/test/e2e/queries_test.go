//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"testing"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Reading wallets, ledger and transactions
//
//	As a provider, I want to follow an operation and page a ledger reliably,
//	so that I can reconcile on my side.
//
//	Wallet reads belong to the internal service; transaction reads belong to the provider that owns
//	the transaction. Both go through real tokens.
//
//	Scenarios:
//	  - The ledger is paginated by an opaque cursor with stable ordering
//	  - Pagination stays stable while new entries arrive
//	  - A bad cursor or a bad limit is refused
//	  - A wallet is read with its balance and version
//	  - A wallet that does not exist is reported as not found
//	  - A transaction query exposes its pending state and failure code
//	  - A transaction can be fetched by provider id and external id

type ledgerPage struct {
	Entries []struct {
		ID            string    `json:"id"`
		TransactionID string    `json:"transactionId"`
		Direction     string    `json:"direction"`
		Money         moneyBody `json:"money"`
		BalanceBefore moneyBody `json:"balanceBefore"`
		BalanceAfter  moneyBody `json:"balanceAfter"`
		CreatedAt     string    `json:"createdAt"`
	} `json:"entries"`
	NextCursor string `json:"nextCursor"`
}

type transactionDetail struct {
	TransactionID                  string     `json:"transactionId"`
	ProviderID                     string     `json:"providerId"`
	ExternalTransactionID          string     `json:"externalTransactionId"`
	Kind                           string     `json:"kind"`
	Status                         string     `json:"status"`
	FailureCode                    string     `json:"failureCode"`
	Balance                        *moneyBody `json:"balance"`
	ReferenceExternalTransactionID string     `json:"referenceExternalTransactionId"`
	ReferenceAttempts              int        `json:"referenceAttempts"`
	NextAttemptAt                  string     `json:"nextAttemptAt"`
	ExpiresAt                      string     `json:"expiresAt"`
	CreatedAt                      string     `json:"createdAt"`
}

// readAs calls a GET as the given identity.
func readAs(t *testing.T, identity, path string) *http.Response {
	t.Helper()
	return stack.Request(t, http.MethodGet, path, stack.ClientToken(t, identity), nil)
}

func ledgerPageOf(t *testing.T, walletID, cursor string, limit int) ledgerPage {
	t.Helper()

	query := url.Values{}
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	if limit > 0 {
		query.Set("limit", strconv.Itoa(limit))
	}
	path := "/wallets/" + walletID + "/ledger"
	if len(query) > 0 {
		path += "?" + query.Encode()
	}
	return core.Decode[ledgerPage](t, core.KeepStatus(t, readAs(t, core.InternalService, path), http.StatusOK))
}

// storedSeqOrder is the ids of a wallet's ledger in the order the database assigned.
func storedSeqOrder(t *testing.T, walletID string) []string {
	t.Helper()

	rows, err := stack.DB(t).Query(context.Background(),
		"SELECT id::text FROM wallet_ledger_entries WHERE wallet_id = $1 ORDER BY seq", walletID)
	if err != nil {
		t.Fatalf("read the ledger order: %v", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// walk reads every page of the ledger and returns the ids in the order they were served.
func walk(t *testing.T, walletID string, size int) []string {
	t.Helper()

	var served []string
	cursor := ""
	for page := 0; page < 100; page++ {
		got := ledgerPageOf(t, walletID, cursor, size)
		for _, entry := range got.Entries {
			served = append(served, entry.ID)
		}
		if got.NextCursor == "" {
			return served
		}
		cursor = got.NextCursor
	}
	t.Fatal("the cursor never ended")
	return nil
}

// Scenario: The ledger is paginated by an opaque cursor with stable ordering
//
//	Given a wallet with more entries than one page
//	When the pages are walked with the returned cursor
//	Then every entry appears exactly once in a stable order
//	And the last page carries no cursor
func TestTheLedgerIsPaginatedByAnOpaqueCursorWithStableOrdering(t *testing.T) {
	w := newWallet(t, "1000.00")
	for range 12 {
		applied(t, w.operation("BET", "1.00"))
	}

	served := walk(t, w.ID, 5)

	want := storedSeqOrder(t, w.ID)
	if len(want) != 13 || len(served) != 13 {
		t.Fatalf("served %d entries of %d stored", len(served), len(want))
	}
	for i := range want {
		if served[i] != want[i] {
			t.Fatalf("entry %d is %s, the stored order has %s: the order is not stable", i, served[i], want[i])
		}
	}
	first := ledgerPageOf(t, w.ID, "", 5)
	if first.NextCursor == "" || len(first.Entries) != 5 || first.Entries[0].Direction != "CREDIT" ||
		first.Entries[0].BalanceBefore.Amount != "0.00" || first.Entries[0].BalanceAfter.Amount != "1000.00" {
		t.Fatalf("first page = %+v", first)
	}
	// Opaque: it must not be a position a client can read or build.
	if _, err := strconv.Atoi(first.NextCursor); err == nil {
		t.Fatalf("the cursor %q is a plain number", first.NextCursor)
	}
}

// Scenario: Pagination stays stable while new entries arrive
//
//	Given a page already fetched
//	When new entries are written and the next page is requested
//	Then no entry from the first page is repeated or skipped
func TestPaginationStaysStableWhileNewEntriesArrive(t *testing.T) {
	w := newWallet(t, "1000.00")
	for range 4 {
		applied(t, w.operation("BET", "1.00"))
	}
	first := ledgerPageOf(t, w.ID, "", 3)
	seen := map[string]bool{}
	for _, entry := range first.Entries {
		seen[entry.ID] = true
	}

	for range 2 {
		applied(t, w.operation("BET", "1.00"))
	}
	var rest []string
	for cursor := first.NextCursor; cursor != ""; {
		page := ledgerPageOf(t, w.ID, cursor, 3)
		for _, entry := range page.Entries {
			if seen[entry.ID] {
				t.Fatalf("entry %s was served twice", entry.ID)
			}
			seen[entry.ID] = true
			rest = append(rest, entry.ID)
		}
		cursor = page.NextCursor
	}

	if len(seen) != 7 {
		t.Fatalf("%d distinct entries were served, want the opening and six bets", len(seen))
	}
	all := storedSeqOrder(t, w.ID)
	got := append(idsOf(first), rest...)
	for i := range all {
		if got[i] != all[i] {
			t.Fatalf("position %d: served %s, stored %s", i, got[i], all[i])
		}
	}
}

func idsOf(page ledgerPage) []string {
	ids := make([]string, 0, len(page.Entries))
	for _, entry := range page.Entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

// Scenario: A bad cursor or a bad limit is refused
//
//	When the ledger is requested with a malformed cursor, with a limit of zero and with a limit that
//	is not a number
//	Then each response is 400
//	And a limit above the cap is served, capped
func TestABadCursorOrABadLimitIsRefused(t *testing.T) {
	w := newWallet(t, "1000.00")
	base := "/wallets/" + w.ID + "/ledger"

	for _, query := range []string{
		"?cursor=not-a-cursor", // a cursor this system did not issue
		"?limit=0",             // a limit of zero
		"?limit=-3",            // a negative limit
		"?limit=many",          // a limit that is not a number
	} {
		core.RequireStatus(t, readAs(t, core.InternalService, base+query), http.StatusBadRequest)
	}
	core.RequireStatus(t, readAs(t, core.InternalService, base+"?limit=100000"), http.StatusOK)
}

// Scenario: A wallet is read with its balance and version
//
//	Given a wallet that took a bet
//	When the internal service reads it
//	Then it answers the stored balance and version
func TestAWalletIsReadWithItsBalanceAndVersion(t *testing.T) {
	w := newWallet(t, "1000.00")
	applied(t, w.operation("BET", "25.00"))

	got := core.Decode[walletResponse](t, core.KeepStatus(t, readAs(t, core.InternalService, "/wallets/"+w.ID), http.StatusOK))

	if got.ID != w.ID || got.PlayerID != w.PlayerID || got.Balance.Amount != "975.00" || got.Version != 2 {
		t.Fatalf("wallet = %+v", got)
	}
}

// Scenario: A wallet that does not exist is reported as not found
//
//	When the internal service reads, pages and reconciles a wallet that does not exist
//	Then each answers 404
//	And an id that is not a UUID answers 400
func TestAWalletThatDoesNotExistIsReportedAsNotFound(t *testing.T) {
	missing := uuid.NewString()

	core.RequireStatus(t, readAs(t, core.InternalService, "/wallets/"+missing), http.StatusNotFound)
	core.RequireStatus(t, readAs(t, core.InternalService, "/wallets/"+missing+"/ledger"), http.StatusNotFound)
	core.RequireStatus(t, stack.Request(t, http.MethodPost, "/wallets/"+missing+"/reconciliation",
		stack.ClientToken(t, core.InternalService), nil), http.StatusNotFound)
	core.RequireStatus(t, readAs(t, core.InternalService, "/wallets/not-a-uuid"), http.StatusBadRequest)
}

// Scenario: A transaction query exposes its pending state and failure code
//
//	Given one PENDING_REFERENCE, one REJECTED and one PROCESSED transaction of a provider
//	When each is fetched by id
//	Then the status, the wait of the pending one, the failureCode of the rejected one and the
//	balance of the processed one are visible
func TestATransactionQueryExposesItsPendingStateAndFailureCode(t *testing.T) {
	w := newWallet(t, "10.00")
	pendingBody := w.reversal("REFUND", "5.00", "bet-"+uuid.NewString())
	pending := core.Decode[transactionResponse](t, core.KeepStatus(t, submit(t, pendingBody), http.StatusAccepted))
	refused := rejected(t, w.operation("BET", "25.00"))
	done := applied(t, w.operation("WIN", "1.00"))

	fetch := func(id string) transactionDetail {
		return core.Decode[transactionDetail](t, core.KeepStatus(t,
			readAs(t, core.ProviderA, "/wagering/transactions/"+id), http.StatusOK))
	}

	p := fetch(pending.TransactionID)
	if p.Status != "PENDING_REFERENCE" || p.ReferenceExternalTransactionID != pendingBody.ReferenceExternalTransactionID ||
		p.NextAttemptAt == "" || p.ExpiresAt == "" || p.Balance != nil {
		t.Fatalf("pending = %+v", p)
	}
	r := fetch(refused.TransactionID)
	if r.Status != "REJECTED" || r.FailureCode != "INSUFFICIENT_FUNDS" || r.Balance != nil {
		t.Fatalf("rejected = %+v", r)
	}
	d := fetch(done.TransactionID)
	if d.Status != "PROCESSED" || d.Balance == nil || d.Balance.Amount != "11.00" || d.Kind != "WIN" {
		t.Fatalf("processed = %+v", d)
	}
}

// Scenario: A transaction can be fetched by provider id and external id
//
//	Given a processed operation
//	When it is fetched by provider id and external transaction id
//	Then the same transaction comes back
func TestATransactionCanBeFetchedByProviderIdAndExternalId(t *testing.T) {
	w := newWallet(t, "1000.00")
	body := w.operation("BET", "25.00")
	stored := applied(t, body)

	got := core.Decode[transactionDetail](t, core.KeepStatus(t, readAs(t, core.ProviderA,
		"/providers/"+core.ProviderA+"/wagering/transactions/"+body.ExternalTransactionID), http.StatusOK))

	if got.TransactionID != stored.TransactionID || got.ExternalTransactionID != body.ExternalTransactionID ||
		got.ProviderID != core.ProviderA || got.Status != "PROCESSED" {
		t.Fatalf("transaction = %+v", got)
	}
	core.RequireStatus(t, readAs(t, core.ProviderA,
		"/providers/"+core.ProviderA+"/wagering/transactions/no-such-transaction"), http.StatusNotFound)
}
