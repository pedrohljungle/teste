//go:build e2e

package e2e

import (
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Authentication and provider isolation
//
//	As the platform, I want identity to decide what a caller may touch,
//	so that a provider can neither move nor read what is not theirs.
//
//	The tokens are real: issued by Keycloak with client_credentials, signed by the realm keys and
//	verified against the JWKS the server loaded at boot. A stubbed principal would prove that a
//	middleware calls a function, not that a token is good.
//
//	Scenarios:
//	  - A request without a token is refused and changes nothing
//	  - A request with a forged token is refused and changes nothing
//	  - A request with an expired token is refused and changes nothing
//	  - A provider cannot open a wallet

// openWalletWith posts a wallet opening under the given bearer.
func openWalletWith(t *testing.T, bearer, playerID string) *http.Response {
	t.Helper()

	return stack.Request(t, http.MethodPost, "/wallets", bearer,
		openBody{PlayerID: playerID, InitialBalance: moneyBody{Amount: "100.00", Currency: "BRL"}})
}

// Scenario: A request without a token is refused and changes nothing
//
//	Given no credentials
//	When a wallet opening is posted
//	Then the response is 401
//	And no wallet, transaction, ledger entry or event exists for the player
func TestARequestWithoutATokenIsRefusedAndChangesNothing(t *testing.T) {
	playerID := uuid.NewString()

	core.RequireStatus(t, openWalletWith(t, "", playerID), http.StatusUnauthorized)

	requireNoTraceOf(t, playerID)
}

// Scenario: A request with a forged token is refused and changes nothing
//
//	Given a genuine token whose signature was tampered with
//	When a wallet opening is posted
//	Then the response is 401
//	And no wallet, transaction, ledger entry or event exists for the player
func TestARequestWithAForgedTokenIsRefusedAndChangesNothing(t *testing.T) {
	playerID := uuid.NewString()
	genuine := stack.ClientToken(t, core.InternalService)
	forged := genuine[:len(genuine)-6] + "AAAAAA"
	if forged == genuine {
		t.Fatal("the forged token is the genuine one")
	}

	core.RequireStatus(t, openWalletWith(t, forged, playerID), http.StatusUnauthorized)

	requireNoTraceOf(t, playerID)
}

// Scenario: A request with an expired token is refused and changes nothing
//
//	Given a genuine token that the realm issued with a one second lifetime
//	And the token was used after it expired
//	When a wallet opening is posted
//	Then the response is 401
//	And no wallet, transaction, ledger entry or event exists for the player
func TestARequestWithAnExpiredTokenIsRefusedAndChangesNothing(t *testing.T) {
	playerID := uuid.NewString()
	token := stack.ClientToken(t, core.ShortLived)
	time.Sleep(2500 * time.Millisecond)

	core.RequireStatus(t, openWalletWith(t, token, playerID), http.StatusUnauthorized)

	requireNoTraceOf(t, playerID)
}

// Scenario: A provider cannot open a wallet
//
//	Given a valid token of a game provider
//	When a wallet opening is posted
//	Then the response is 403
//	And no wallet, transaction, ledger entry or event exists for the player
func TestAProviderCannotOpenAWallet(t *testing.T) {
	playerID := uuid.NewString()

	core.RequireStatus(t, openWalletWith(t, stack.ClientToken(t, core.ProviderA), playerID), http.StatusForbidden)

	requireNoTraceOf(t, playerID)
}

// requireNoTraceOf asserts that nothing of a player reached storage.
func requireNoTraceOf(t *testing.T, playerID string) {
	t.Helper()

	db := stack.DB(t)
	if got := count(t, db, "SELECT count(*) FROM wallets WHERE player_id = $1", playerID); got != 0 {
		t.Errorf("%d wallets exist for the player", got)
	}
	if got := count(t, db, "SELECT count(*) FROM wager_transactions WHERE player_id = $1", playerID); got != 0 {
		t.Errorf("%d transactions exist for the player", got)
	}
	if got := count(t, db, `
		SELECT count(*) FROM wallet_ledger_entries e
		JOIN wallets w ON w.id = e.wallet_id WHERE w.player_id = $1`, playerID); got != 0 {
		t.Errorf("%d ledger entries exist for the player", got)
	}
}
