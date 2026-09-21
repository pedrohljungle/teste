//go:build e2e

package e2e

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Wallet reconciliation
//
//	As an operator, I want the stored balance checked against the ledger without touching it,
//	so that a divergence is found rather than hidden.
//
//	Scenarios:
//	  - Reconciling a consistent wallet reports no difference
//	  - Reconciliation reports an injected divergence and changes nothing
//	  - Reconciliation is consistent under concurrent movements

type reconciliationResponse struct {
	WalletID          string    `json:"walletId"`
	StoredBalance     moneyBody `json:"storedBalance"`
	CalculatedBalance moneyBody `json:"calculatedBalance"`
	Difference        moneyBody `json:"difference"`
	Consistent        bool      `json:"consistent"`
	CheckedEntries    int       `json:"checkedEntries"`
}

func reconcile(t *testing.T, walletID string) reconciliationResponse {
	t.Helper()

	res := stack.Request(t, http.MethodPost, "/wallets/"+walletID+"/reconciliation", stack.ClientToken(t, core.InternalService), nil)
	return core.Decode[reconciliationResponse](t, core.KeepStatus(t, res, http.StatusOK))
}

// Scenario: Reconciling a consistent wallet reports no difference
//
//	Given a wallet opened with 1000.00 BRL and one bet of 25.00 BRL
//	When reconciliation is requested
//	Then storedBalance and calculatedBalance are 975.00, difference is 0.00
//	And consistent is true and checkedEntries is 2
func TestReconcilingAConsistentWalletReportsNoDifference(t *testing.T) {
	w := newWallet(t, "1000.00")
	applied(t, w.operation("BET", "25.00"))

	got := reconcile(t, w.ID)

	if got.WalletID != w.ID || got.StoredBalance.Amount != "975.00" || got.CalculatedBalance.Amount != "975.00" ||
		got.Difference.Amount != "0.00" || !got.Consistent || got.CheckedEntries != 2 {
		t.Fatalf("reconciliation = %+v", got)
	}
	if got.StoredBalance.Currency != "BRL" || got.Difference.Currency != "BRL" {
		t.Fatalf("currencies = %+v", got)
	}
}

// Scenario: Reconciliation reports an injected divergence and changes nothing
//
//	Given a wallet whose stored balance was tampered with directly in SQL
//	When reconciliation is requested
//	Then consistent is false with the exact difference
//	And the balance is left untouched
func TestReconciliationReportsAnInjectedDivergenceAndChangesNothing(t *testing.T) {
	w := newWallet(t, "1000.00")
	applied(t, w.operation("BET", "25.00"))
	if _, err := stack.DB(t).Exec(context.Background(),
		"UPDATE wallets SET balance_minor = balance_minor + 500 WHERE id = $1", w.ID); err != nil {
		t.Fatalf("tamper with the balance: %v", err)
	}
	before := walletState(t, w.ID)

	got := reconcile(t, w.ID)

	if got.Consistent || got.StoredBalance.Amount != "980.00" || got.CalculatedBalance.Amount != "975.00" ||
		got.Difference.Amount != "5.00" {
		t.Fatalf("reconciliation = %+v", got)
	}
	if after := walletState(t, w.ID); after != before {
		t.Fatalf("the reconciliation changed the wallet: before %+v, after %+v", before, after)
	}
	// A second run finds the same thing: it corrected nothing.
	if again := reconcile(t, w.ID); again.Consistent || again.Difference.Amount != "5.00" {
		t.Fatalf("the divergence disappeared: %+v", again)
	}
}

// Scenario: Reconciliation is consistent under concurrent movements
//
//	Given bets running against the wallet
//	When reconciliation is requested again and again meanwhile
//	Then every answer is consistent, because the two balances come from the same snapshot
func TestReconciliationIsConsistentUnderConcurrentMovements(t *testing.T) {
	w := newWallet(t, "100000.00")

	var betting sync.WaitGroup
	betting.Add(1)
	go func() {
		defer betting.Done()
		for i := 0; i < 60; i++ {
			body := w.operation("BET", "1.00")
			res := submit(t, body)
			if res.StatusCode != http.StatusOK {
				t.Errorf("bet %d answered %d", i, res.StatusCode)
			}
			_ = res.Body.Close()
		}
	}()

	checks := 0
	done := make(chan struct{})
	go func() { betting.Wait(); close(done) }()
loop:
	for {
		select {
		case <-done:
			break loop
		default:
			if got := reconcile(t, w.ID); !got.Consistent {
				t.Fatalf("a healthy wallet reconciled as divergent under load: %+v", got)
			}
			checks++
		}
	}

	if checks < 5 {
		t.Fatalf("only %d reconciliations ran while the bets were going", checks)
	}
	if final := reconcile(t, w.ID); !final.Consistent || final.CheckedEntries != 61 {
		t.Fatalf("final = %+v", final)
	}
}
