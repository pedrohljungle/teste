//go:build e2e

package core

import (
	"context"
	"fmt"
	"net/url"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// DB is a pool on the suite database, for the steps a scenario cannot take through the API:
// seeding rows, and asserting on what the schema itself refuses.
//
// It is one pool for the whole suite. A pool per call, closed when its test ended, looks harmless
// until a scenario reads in a loop: sixty reads open sixty pools, and Postgres runs out of
// connections in the middle of an assertion.
func (s *Stack) DB(t *testing.T) *pgxpool.Pool {
	t.Helper()

	s.poolOnce.Do(func() {
		s.pool, s.poolErr = pgxpool.New(context.Background(), s.infra.databaseURL)
	})
	if s.poolErr != nil {
		t.Fatalf("open the suite database: %v", s.poolErr)
	}
	return s.pool
}

// ScratchDatabase creates an empty database beside the suite one and returns its URL. It exists
// for what has to run against a schema nobody else touched, such as applying the migrations and
// rolling them back. The database is dropped when the test ends.
func (s *Stack) ScratchDatabase(t *testing.T) string {
	t.Helper()

	admin := s.DB(t)
	// The name is generated here and never comes from a caller, so building the statement by
	// concatenation is safe: CREATE DATABASE cannot take a bind parameter anyway.
	name := fmt.Sprintf("scratch_%d", time.Now().UnixNano())
	if _, err := admin.Exec(context.Background(), "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create the scratch database: %v", err)
	}
	t.Cleanup(func() {
		cleanup, err := pgxpool.New(context.Background(), s.infra.databaseURL)
		if err != nil {
			return
		}
		defer cleanup.Close()
		_, _ = cleanup.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})

	parsed, err := url.Parse(s.infra.databaseURL)
	if err != nil {
		t.Fatalf("parse the database url: %v", err)
	}
	parsed.Path = "/" + name
	return parsed.String()
}

// MigrateUp applies every migration to the database at databaseURL.
func (s *Stack) MigrateUp(t *testing.T, databaseURL string) {
	t.Helper()
	if err := migrate(databaseURL); err != nil {
		t.Fatalf("apply the migrations: %v", err)
	}
}

// MigrateDown rolls every migration back on the database at databaseURL.
func (s *Stack) MigrateDown(t *testing.T, databaseURL string) {
	t.Helper()
	if err := migrateDown(databaseURL); err != nil {
		t.Fatalf("roll the migrations back: %v", err)
	}
}

// OpenPool opens a pool on an arbitrary database URL, closed when the test ends.
func OpenPool(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()

	pool, err := pgxpool.New(context.Background(), databaseURL)
	if err != nil {
		t.Fatalf("open a pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}
