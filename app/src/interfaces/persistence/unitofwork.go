// Package persistence is the contract for storage concerns that belong to no domain: the unit
// of work every domain's writes share.
package persistence

import "context"

// UnitOfWork runs a set of writes as one atomic commit.
//
// A service that knows several writes must land together wraps them in Atomic. Every repository
// called with the context fn receives joins that transaction, and a repository called outside
// one talks to the database on its own, so the same repository serves a lone read and a
// transactional write.
type UnitOfWork interface {
	// Atomic runs fn in a single SQL transaction: a nil return commits, an error or a panic
	// rolls back. A call made from inside another Atomic joins the outer transaction instead of
	// opening a second one, so a service that composes another does not split the commit.
	//
	// When the commit itself fails, the outcome is unknown: the database may have committed and
	// the answer been lost. The write is safe to retry because every write is idempotent by
	// constraint.
	Atomic(ctx context.Context, fn func(ctx context.Context) error) error

	// Snapshot runs fn in one read-only transaction that sees a single consistent view of the
	// data. Everything the repositories read inside it comes from the same instant, which is what
	// a comparison between two readings needs: two separate reads can disagree only because
	// something committed between them. Nothing can be written inside it.
	Snapshot(ctx context.Context, fn func(ctx context.Context) error) error
}
