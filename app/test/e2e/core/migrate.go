//go:build e2e

package core

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
)

// migrate applies the same migrations the goose container applies in compose, through the
// goose library so the suite does not need a second container just to run them.
func migrate(databaseURL string) error {
	dir, err := repoFile("migrations")
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.UpContext(context.Background(), db, dir); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// migrateDown rolls back every migration, in reverse order. It is the counterpart the suite
// uses to prove each migration has a working Down.
func migrateDown(databaseURL string) error {
	dir, err := repoFile("migrations")
	if err != nil {
		return err
	}

	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set dialect: %w", err)
	}
	if err := goose.DownToContext(context.Background(), db, dir, 0); err != nil {
		return fmt.Errorf("roll back migrations: %w", err)
	}
	return nil
}
