package db

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
)

// rollbackTimeout bounds the rollback that runs after the caller's context is already gone.
const rollbackTimeout = 5 * time.Second

// Querier is what both a pool and a transaction satisfy, which is what lets a repository run
// the same statement inside a unit of work or on its own.
type Querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

type txKey struct{}

// Accessor hands a repository the way to reach the database for the current context: the open
// transaction when there is one, the pool otherwise.
//
// A repository receives the Accessor and never the pool. That is the point: with the pool out
// of reach, there is no path on which a repository writes outside the transaction by
// forgetting to look for it. The transaction carried in a context is implicit and silent, and
// that is the classic way for this pattern to fail.
type Accessor struct {
	pool *pgxpool.Pool
}

// NewAccessor builds the accessor over the shared pool.
func NewAccessor(pool *pgxpool.Pool) *Accessor {
	return &Accessor{pool: pool}
}

// Q is the querier for this context.
func (a *Accessor) Q(ctx context.Context) Querier {
	if tx, ok := transactionFrom(ctx); ok {
		return tx
	}
	return a.pool
}

// Ping asks the database for a round trip.
func (a *Accessor) Ping(ctx context.Context) error {
	return a.pool.Ping(ctx)
}

// InTransaction reports whether the context carries an open transaction.
func (a *Accessor) InTransaction(ctx context.Context) bool {
	_, ok := transactionFrom(ctx)
	return ok
}

// RequireTransaction fails with persistence.ErrNoTransaction when the context carries no open
// transaction. Writes that must land with others, and row locks, call it first.
func (a *Accessor) RequireTransaction(ctx context.Context) error {
	if !a.InTransaction(ctx) {
		return persistence.ErrNoTransaction
	}
	return nil
}

// Do runs fn inside one transaction. A nil return commits; an error or a panic rolls back, and
// a panic is raised again after the rollback. Called from inside a transaction it joins that
// one instead of opening another.
func (a *Accessor) Do(ctx context.Context, fn func(ctx context.Context) error) error {
	return a.run(ctx, pgx.TxOptions{}, fn)
}

// DoSnapshot runs fn in one read-only transaction that sees a single consistent view of the
// database, however long it takes and whatever commits meanwhile. It is what a reading that has to
// compare two numbers needs: read one after the other outside it, and a write in between makes them
// disagree with each other for no reason but timing. Called from inside a transaction it joins that
// one, and then the view is whatever that transaction has.
func (a *Accessor) DoSnapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	return a.run(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, fn)
}

func (a *Accessor) run(ctx context.Context, options pgx.TxOptions, fn func(ctx context.Context) error) (err error) {
	if a.InTransaction(ctx) {
		return fn(ctx)
	}

	tx, err := a.pool.BeginTx(ctx, options)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", Classify(err))
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			rollback(ctx, tx)
			panic(recovered)
		}
	}()

	if err := fn(context.WithValue(ctx, txKey{}, tx)); err != nil {
		rollback(ctx, tx)
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", Classify(err))
	}
	return nil
}

// rollback runs on a context that no longer carries the caller's cancellation: a request that
// timed out is exactly when the transaction has to be released, not abandoned.
func rollback(ctx context.Context, tx pgx.Tx) {
	rollbackCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), rollbackTimeout)
	defer cancel()
	// The error is dropped on purpose: the caller already has the one that caused the rollback,
	// and a connection that is gone has nothing left to roll back.
	_ = tx.Rollback(rollbackCtx)
}

func transactionFrom(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// Classify marks a storage failure that a retry may cure with persistence.ErrUnavailable and
// leaves every other error as it came. Callers keep matching their own sentinels through the
// wrapping, and a service or handler tells "try again" from "this cannot work" by errors.Is.
func Classify(err error) error {
	if err == nil || !isTransient(err) {
		return err
	}
	return fmt.Errorf("%w: %w", persistence.ErrUnavailable, err)
}

func isTransient(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		if len(pgErr.Code) < 2 {
			return false
		}
		// 08 connection, 40 serialisation and deadlock, 53 resources, 57 operator shutdown.
		switch pgErr.Code[:2] {
		case "08", "40", "53", "57":
			return true
		}
		return false
	}

	var connectErr *pgconn.ConnectError
	var netErr net.Error
	return errors.As(err, &connectErr) ||
		errors.As(err, &netErr) ||
		pgconn.Timeout(err) ||
		errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, io.EOF) ||
		errors.Is(err, io.ErrUnexpectedEOF)
}

// UniqueViolation reports whether err is a unique constraint violation, and which constraint
// or index it was. The name is what lets a repository turn a generic conflict into the specific
// sentinel of its contract.
func UniqueViolation(err error) (constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return pgErr.ConstraintName, true
	}
	return "", false
}

const pgUniqueViolation = "23505"
