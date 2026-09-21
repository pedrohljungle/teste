package health

import (
	"context"
	"fmt"

	healthiface "github.com/estrategiahq/pedro-test/app/src/interfaces/health"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
)

type postgresChecker struct {
	db *db.Accessor
}

// NewPostgresChecker checks the database with a round trip, and not with the state of the pool: a
// pool that holds a dead connection still looks open.
func NewPostgresChecker(accessor *db.Accessor) healthiface.Checker {
	return &postgresChecker{db: accessor}
}

func (c *postgresChecker) Name() string { return "postgres" }

func (c *postgresChecker) Check(ctx context.Context) error {
	if err := c.db.Ping(ctx); err != nil {
		return fmt.Errorf("ping postgres: %w", err)
	}
	return nil
}
