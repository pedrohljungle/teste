package persistence

import (
	"context"

	"github.com/estrategiahq/pedro-test/app/src/interfaces/persistence"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

var _ persistence.UnitOfWork = (*unitOfWork)(nil)

type unitOfWork struct {
	accessor *db.Accessor
	obs      *observability.Observer
}

// NewUnitOfWork builds the unit of work over the shared accessor.
func NewUnitOfWork(accessor *db.Accessor, obs *observability.Observer) persistence.UnitOfWork {
	return &unitOfWork{accessor: accessor, obs: obs}
}

func (u *unitOfWork) Atomic(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	ctx, end := u.obs.Start(ctx, observability.LayerRepository, "persistence.UnitOfWork.Atomic")
	defer func() { end(err) }()

	return u.accessor.Do(ctx, fn)
}

func (u *unitOfWork) Snapshot(ctx context.Context, fn func(ctx context.Context) error) (err error) {
	ctx, end := u.obs.Start(ctx, observability.LayerRepository, "persistence.UnitOfWork.Snapshot")
	defer func() { end(err) }()

	return u.accessor.DoSnapshot(ctx, fn)
}
