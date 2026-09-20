//go:build e2e

package e2e

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estrategiahq/pedro-test/app/test/e2e/core"
)

// Feature: Ledger integrity and schema guarantees
//
//	As an auditor, I want the database itself to refuse a corrupt write,
//	so that a bug in Go cannot produce an unauditable ledger.
//
//	Scenarios:
//	  - A ledger row cannot be updated
//	  - A ledger row cannot be deleted
//	  - The ledger cannot be truncated
//	  - A balance cannot be driven negative through the schema
//	  - A second ledger entry for the same transaction is refused
//	  - A second opening for the same wallet is refused
//	  - A second successful reversal of one reference is refused
//	  - A second wallet for the same player and currency is refused
//	  - The same provider and external transaction id cannot be stored twice
//	  - The same idempotency key cannot be stored twice for a provider
//	  - The inbox refuses a message it already holds
//	  - Migrations apply and roll back cleanly
//
// These scenarios talk to Postgres directly on purpose: the point is that the constraints and
// triggers refuse the write no matter which code path attempted it, so the application is
// deliberately not in the way.

const (
	pgRaiseException  = "P0001"
	pgUniqueViolation = "23505"
	pgCheckViolation  = "23514"
)

// Scenario: A ledger row cannot be updated
//
//	Given a wallet with a credit entry in its ledger
//	When an UPDATE is issued against that entry directly in SQL
//	Then the database refuses it
//	And the row is unchanged
func TestALedgerRowCannotBeUpdated(t *testing.T) {
	db := stack.DB(t)
	entryID := seedLedgerEntry(t, db, 10000)

	_, err := db.Exec(context.Background(),
		"UPDATE wallet_ledger_entries SET amount_minor = 1 WHERE id = $1", entryID)

	requirePgError(t, err, pgRaiseException, "append-only")
	if got := ledgerAmount(t, db, entryID); got != 10000 {
		t.Fatalf("the ledger row changed: amount is %d, expected 10000", got)
	}
}

// Scenario: A ledger row cannot be deleted
//
//	Given a wallet with a credit entry in its ledger
//	When a DELETE is issued against that entry directly in SQL
//	Then the database refuses it
//	And the row is still there
func TestALedgerRowCannotBeDeleted(t *testing.T) {
	db := stack.DB(t)
	entryID := seedLedgerEntry(t, db, 10000)

	_, err := db.Exec(context.Background(),
		"DELETE FROM wallet_ledger_entries WHERE id = $1", entryID)

	requirePgError(t, err, pgRaiseException, "append-only")
	if got := ledgerAmount(t, db, entryID); got != 10000 {
		t.Fatalf("the ledger row is gone or changed: amount is %d, expected 10000", got)
	}
}

// Scenario: The ledger cannot be truncated
//
//	Given a wallet with a credit entry in its ledger
//	When TRUNCATE is issued against the ledger table directly in SQL
//	Then the database refuses it
//	And the row is still there
func TestTheLedgerCannotBeTruncated(t *testing.T) {
	db := stack.DB(t)
	entryID := seedLedgerEntry(t, db, 10000)

	_, err := db.Exec(context.Background(), "TRUNCATE wallet_ledger_entries")

	requirePgError(t, err, pgRaiseException, "append-only")
	if got := ledgerAmount(t, db, entryID); got != 10000 {
		t.Fatalf("the ledger row is gone or changed: amount is %d, expected 10000", got)
	}
}

// Scenario: A balance cannot be driven negative through the schema
//
//	Given a wallet with a balance of 100.00
//	When an UPDATE sets its balance below zero directly in SQL
//	Then the check constraint refuses it
//	And the balance is unchanged
func TestABalanceCannotBeDrivenNegativeThroughTheSchema(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)

	_, err := db.Exec(context.Background(),
		"UPDATE wallets SET balance_minor = -1 WHERE id = $1", walletID)

	requirePgConstraint(t, err, pgCheckViolation, "ck_wallet_balance_non_negative")
	var balance int64
	if err := db.QueryRow(context.Background(),
		"SELECT balance_minor FROM wallets WHERE id = $1", walletID).Scan(&balance); err != nil {
		t.Fatalf("read the balance: %v", err)
	}
	if balance != 10000 {
		t.Fatalf("the balance changed: %d, expected 10000", balance)
	}
}

// Scenario: A second ledger entry for the same transaction is refused
//
//	Given a wallet with a credit entry for a transaction
//	When a second entry is inserted for the same wallet and transaction
//	Then the unique constraint refuses it
func TestASecondLedgerEntryForTheSameTransactionIsRefused(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)
	transactionID := seedOpening(t, db, walletID, 10000)
	insertLedgerEntry(t, db, walletID, transactionID, 10000)

	err := tryInsertLedgerEntry(db, walletID, transactionID, 10000)

	requirePgConstraint(t, err, pgUniqueViolation, "uk_ledger_wallet_transaction")
}

// Scenario: A second opening for the same wallet is refused
//
//	Given a wallet with an OPENING transaction
//	When a second OPENING is inserted for the same wallet
//	Then the partial unique index refuses it
func TestASecondOpeningForTheSameWalletIsRefused(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)
	seedOpening(t, db, walletID, 10000)

	err := tryInsertOpening(db, walletID, 10000)

	requirePgConstraint(t, err, pgUniqueViolation, "uk_wager_single_opening")
}

// Scenario: A second successful reversal of one reference is refused
//
//	Given a processed BET
//	And a processed REFUND referencing it
//	When a second processed reversal referencing the same bet is inserted
//	Then the partial unique index refuses it
//	And a reversal that was rejected is still accepted, because it moved nothing
func TestASecondSuccessfulReversalOfOneReferenceIsRefused(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)
	bet := seedExternal(t, db, walletID, "BET", "PROCESSED", "")
	seedExternal(t, db, walletID, "REFUND", "PROCESSED", bet)

	err := tryInsertExternal(db, walletID, "ROLLBACK", "PROCESSED", bet)
	requirePgConstraint(t, err, pgUniqueViolation, "uk_wager_single_reversal")

	if err := tryInsertExternal(db, walletID, "ROLLBACK", "REJECTED", bet); err != nil {
		t.Fatalf("a rejected reversal must be storable, it moved nothing: %v", err)
	}
}

// Scenario: A second wallet for the same player and currency is refused
//
//	Given a wallet for a player in BRL
//	When a second wallet is inserted for the same player in BRL
//	Then the unique constraint refuses it
//	And a wallet for the same player in USD is accepted
func TestASecondWalletForTheSamePlayerAndCurrencyIsRefused(t *testing.T) {
	db := stack.DB(t)
	playerID := uuid.New()
	insertWallet := func(currency string) error {
		_, err := db.Exec(context.Background(),
			"INSERT INTO wallets (id, player_id, currency) VALUES ($1, $2, $3)",
			uuid.New(), playerID, currency)
		return err
	}
	if err := insertWallet("BRL"); err != nil {
		t.Fatalf("first wallet: %v", err)
	}

	requirePgConstraint(t, insertWallet("BRL"), pgUniqueViolation, "uk_wallet_player_currency")
	if err := insertWallet("USD"); err != nil {
		t.Fatalf("a wallet in another currency must be accepted: %v", err)
	}
}

// Scenario: The same provider and external transaction id cannot be stored twice
//
//	Given a stored external transaction
//	When another one with the same provider and external id but another idempotency key is inserted
//	Then the unique index refuses it
func TestTheSameProviderAndExternalTransactionIdCannotBeStoredTwice(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)
	external := seedExternal(t, db, walletID, "BET", "PROCESSED", "")

	err := tryInsertExternalWith(db, walletID, "BET", "PROCESSED", "", external, "another-key-"+uuid.NewString())

	requirePgConstraint(t, err, pgUniqueViolation, "uk_wager_provider_external")
}

// Scenario: The same idempotency key cannot be stored twice for a provider
//
//	Given a stored external transaction
//	When another one with the same idempotency key but another external id is inserted
//	Then the unique index refuses it
func TestTheSameIdempotencyKeyCannotBeStoredTwiceForAProvider(t *testing.T) {
	db := stack.DB(t)
	walletID := seedWallet(t, db, 10000)
	external := seedExternal(t, db, walletID, "BET", "PROCESSED", "")

	err := tryInsertExternalWith(db, walletID, "BET", "PROCESSED", "", "another-id-"+uuid.NewString(), "provider-a:"+external)

	requirePgConstraint(t, err, pgUniqueViolation, "uk_wager_idempotency_key")
}

// Scenario: The inbox refuses a message it already holds
//
//	Given a message stored in the inbox for a consumer
//	When the same message id is inserted for the same consumer
//	Then the primary key refuses it
//	And the same message id for another consumer is accepted
func TestTheInboxRefusesAMessageItAlreadyHolds(t *testing.T) {
	db := stack.DB(t)
	messageID := "msg-" + uuid.NewString()
	insert := func(consumer string) error {
		_, err := db.Exec(context.Background(),
			"INSERT INTO inbox_messages (consumer_name, message_id, payload_hash) VALUES ($1, $2, $3)",
			consumer, messageID, []byte{1})
		return err
	}
	if err := insert("wager-consumer"); err != nil {
		t.Fatalf("first message: %v", err)
	}

	requirePgConstraint(t, insert("wager-consumer"), pgUniqueViolation, "pk_inbox")
	if err := insert("another-consumer"); err != nil {
		t.Fatalf("the same id for another consumer must be accepted: %v", err)
	}
}

// Scenario: Migrations apply and roll back cleanly
//
//	Given an empty database
//	When every migration is applied
//	Then the domain tables exist
//	When every migration is rolled back
//	Then no domain table and no trigger function remain
//	And the migrations can be applied again
func TestMigrationsApplyAndRollBackCleanly(t *testing.T) {
	databaseURL := stack.ScratchDatabase(t)
	pool := core.OpenPool(t, databaseURL)
	domain := []string{"inbox_messages", "outbox_events", "wager_transactions", "wallet_ledger_entries", "wallets"}

	stack.MigrateUp(t, databaseURL)
	if got := domainTables(t, pool); !equalStrings(got, domain) {
		t.Fatalf("after Up the tables are %v, expected %v", got, domain)
	}

	stack.MigrateDown(t, databaseURL)
	if got := domainTables(t, pool); len(got) != 0 {
		t.Fatalf("after Down these tables remain: %v", got)
	}
	var functions int
	if err := pool.QueryRow(context.Background(),
		"SELECT count(*) FROM pg_proc WHERE proname = 'ledger_is_append_only'").Scan(&functions); err != nil {
		t.Fatalf("count the trigger functions: %v", err)
	}
	if functions != 0 {
		t.Fatalf("the ledger trigger function survived the rollback")
	}

	stack.MigrateUp(t, databaseURL)
	if got := domainTables(t, pool); !equalStrings(got, domain) {
		t.Fatalf("after applying again the tables are %v, expected %v", got, domain)
	}
}

// --- seeding: rows inserted directly, because the schema is what is under test -------------

func seedWallet(t *testing.T, db *pgxpool.Pool, balanceMinor int64) uuid.UUID {
	t.Helper()

	id := uuid.New()
	if _, err := db.Exec(context.Background(),
		"INSERT INTO wallets (id, player_id, currency, balance_minor) VALUES ($1, $2, 'BRL', $3)",
		id, uuid.New(), balanceMinor); err != nil {
		t.Fatalf("seed a wallet: %v", err)
	}
	return id
}

func seedOpening(t *testing.T, db *pgxpool.Pool, walletID uuid.UUID, amountMinor int64) uuid.UUID {
	t.Helper()

	id, err := insertOpening(db, walletID, amountMinor)
	if err != nil {
		t.Fatalf("seed an opening: %v", err)
	}
	return id
}

func tryInsertOpening(db *pgxpool.Pool, walletID uuid.UUID, amountMinor int64) error {
	_, err := insertOpening(db, walletID, amountMinor)
	return err
}

func insertOpening(db *pgxpool.Pool, walletID uuid.UUID, amountMinor int64) (uuid.UUID, error) {
	id := uuid.New()
	_, err := db.Exec(context.Background(), `
		INSERT INTO wager_transactions (id, origin, kind, status, wallet_id, player_id, amount_minor, currency)
		VALUES ($1, 'INTERNAL', 'OPENING', 'PROCESSED', $2, $3, $4, 'BRL')`,
		id, walletID, uuid.New(), amountMinor)
	return id, err
}

// seedLedgerEntry builds a wallet, its opening and one credit entry, and returns the entry id.
func seedLedgerEntry(t *testing.T, db *pgxpool.Pool, amountMinor int64) uuid.UUID {
	t.Helper()

	walletID := seedWallet(t, db, amountMinor)
	transactionID := seedOpening(t, db, walletID, amountMinor)
	return insertLedgerEntry(t, db, walletID, transactionID, amountMinor)
}

func insertLedgerEntry(t *testing.T, db *pgxpool.Pool, walletID, transactionID uuid.UUID, amountMinor int64) uuid.UUID {
	t.Helper()

	id := uuid.New()
	if err := tryInsertLedgerEntryWithID(db, id, walletID, transactionID, amountMinor); err != nil {
		t.Fatalf("seed a ledger entry: %v", err)
	}
	return id
}

func tryInsertLedgerEntry(db *pgxpool.Pool, walletID, transactionID uuid.UUID, amountMinor int64) error {
	return tryInsertLedgerEntryWithID(db, uuid.New(), walletID, transactionID, amountMinor)
}

func tryInsertLedgerEntryWithID(db *pgxpool.Pool, id, walletID, transactionID uuid.UUID, amountMinor int64) error {
	_, err := db.Exec(context.Background(), `
		INSERT INTO wallet_ledger_entries
			(id, wallet_id, transaction_id, direction, amount_minor, currency, balance_before_minor, balance_after_minor)
		VALUES ($1, $2, $3, 'CREDIT', $4, 'BRL', 0, $4)`,
		id, walletID, transactionID, amountMinor)
	return err
}

// seedExternal stores an external transaction and returns its external id, which is what a
// reversal references.
func seedExternal(t *testing.T, db *pgxpool.Pool, walletID uuid.UUID, kind, status, reference string) string {
	t.Helper()

	external := "ext-" + uuid.NewString()
	if err := tryInsertExternalWith(db, walletID, kind, status, reference, external, "provider-a:"+external); err != nil {
		t.Fatalf("seed an external transaction: %v", err)
	}
	return external
}

func tryInsertExternal(db *pgxpool.Pool, walletID uuid.UUID, kind, status, reference string) error {
	external := "ext-" + uuid.NewString()
	return tryInsertExternalWith(db, walletID, kind, status, reference, external, "provider-a:"+external)
}

func tryInsertExternalWith(db *pgxpool.Pool, walletID uuid.UUID, kind, status, reference, external, key string) error {
	var referenceArg any
	if reference != "" {
		referenceArg = reference
	}
	_, err := db.Exec(context.Background(), `
		INSERT INTO wager_transactions
			(id, origin, kind, status, wallet_id, player_id, amount_minor, currency,
			 provider_id, external_transaction_id, idempotency_key, payload_hash, round_id, game_id,
			 reference_external_transaction_id)
		VALUES ($1, 'EXTERNAL', $2, $3, $4, $5, 1000, 'BRL',
			'provider-a', $6, $7, $8, 'round-1', 'game-1', $9)`,
		uuid.New(), kind, status, walletID, uuid.New(), external, key, []byte{1, 2, 3}, referenceArg)
	return err
}

func ledgerAmount(t *testing.T, db *pgxpool.Pool, entryID uuid.UUID) int64 {
	t.Helper()

	var amount int64
	if err := db.QueryRow(context.Background(),
		"SELECT amount_minor FROM wallet_ledger_entries WHERE id = $1", entryID).Scan(&amount); err != nil {
		t.Fatalf("read the ledger entry: %v", err)
	}
	return amount
}

func domainTables(t *testing.T, db *pgxpool.Pool) []string {
	t.Helper()

	rows, err := db.Query(context.Background(), `
		SELECT table_name FROM information_schema.tables
		WHERE table_schema = 'public' AND table_name <> 'goose_db_version'
		ORDER BY table_name`)
	if err != nil {
		t.Fatalf("list the tables: %v", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan a table name: %v", err)
		}
		tables = append(tables, name)
	}
	return tables
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// requirePgError asserts the database refused the statement with the given SQLSTATE and a
// message that contains the fragment.
func requirePgError(t *testing.T, err error, code, fragment string) {
	t.Helper()

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected the database to refuse the statement, got %v", err)
	}
	if pgErr.Code != code {
		t.Fatalf("expected SQLSTATE %s, got %s: %s", code, pgErr.Code, pgErr.Message)
	}
	if !strings.Contains(pgErr.Message, fragment) {
		t.Fatalf("expected the message to mention %q, got %q", fragment, pgErr.Message)
	}
}

// requirePgConstraint asserts the database refused the statement because of a named constraint.
func requirePgConstraint(t *testing.T, err error, code, constraint string) {
	t.Helper()

	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Fatalf("expected the database to refuse the statement, got %v", err)
	}
	if pgErr.Code != code || pgErr.ConstraintName != constraint {
		t.Fatalf("expected %s on %s, got %s on %q: %s", code, constraint, pgErr.Code, pgErr.ConstraintName, pgErr.Message)
	}
}
