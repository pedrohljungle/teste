package wagering

import (
	"context"

	"github.com/google/uuid"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// Get reads a transaction on behalf of a provider.
//
// Whether a provider may read a transaction depends on the transaction, so the rule is here and not
// in the border. A transaction of another provider is reported as not found, the very same error as
// one that does not exist, so that asking does not tell a provider which ids belong to someone else.
func (s *service) Get(ctx context.Context, providerID string, id uuid.UUID) (*entities.WagerTransaction, error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.Get",
		observability.String("providerId", providerID))
	tx, err := s.get(ctx, providerID, id)
	end(expectedMissing(err))
	return tx, err
}

func (s *service) get(ctx context.Context, providerID string, id uuid.UUID) (*entities.WagerTransaction, error) {
	tx, err := s.wagering.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	// An internal transaction, an opening, has no provider and is nobody's to read here.
	if providerID == "" || tx.ProviderID() != providerID {
		return nil, wageringiface.ErrNotFound
	}
	return tx, nil
}

// GetByExternal reads a provider's own transaction by the id the provider gave it. The lookup is
// scoped by the provider that is asking, so there is no way to name another one's.
func (s *service) GetByExternal(ctx context.Context, providerID, externalTransactionID string) (*entities.WagerTransaction, error) {
	ctx, end := s.obs.Start(ctx, observability.LayerService, "wagering.Service.GetByExternal",
		observability.String("providerId", providerID))
	tx, err := s.getByExternal(ctx, providerID, externalTransactionID)
	end(expectedMissing(err))
	return tx, err
}

func (s *service) getByExternal(ctx context.Context, providerID, externalTransactionID string) (*entities.WagerTransaction, error) {
	if providerID == "" {
		return nil, wageringiface.ErrNotFound
	}
	return s.wagering.FindByExternal(ctx, providerID, externalTransactionID)
}

// expectedMissing is the error a span should record for a read: a lookup that finds nothing is the
// answer, not a failure of the service.
func expectedMissing(err error) error {
	if isNotFound(err) {
		return nil
	}
	return err
}
