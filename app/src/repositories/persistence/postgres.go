package persistence

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

type unitOfWork struct {
	accessor *db.Accessor
	obs      *observability.Observer
}

// NewUnitOfWork builds the unit of work over the shared accessor.
func NewUnitOfWork(accessor *db.Accessor, obs *observability.Observer) persistence.UnitOfWork {
	return &unitOfWork{accessor: accessor, obs: obs}
}

func (u *unitOfWork) Atomic(ctx context.Context, fn func(ctx context.Context) error) error {
	return observability.TraceErr(ctx, u.obs, observability.LayerRepository, "persistence.UnitOfWork.Atomic", func(ctx context.Context) error {
		return u.accessor.Do(ctx, fn)
	})
}

func (u *unitOfWork) Snapshot(ctx context.Context, fn func(ctx context.Context) error) error {
	return observability.TraceErr(ctx, u.obs, observability.LayerRepository, "persistence.UnitOfWork.Snapshot", func(ctx context.Context) error {
		return u.accessor.DoSnapshot(ctx, fn)
	})
}
