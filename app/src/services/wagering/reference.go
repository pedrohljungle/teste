package wagering

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// ResolvePending looks again for the reference of the reversals that are due, one unit of work each.
func (s *service) ResolvePending(ctx context.Context) (int, error) {
	return observability.Trace(ctx, s.obs, observability.LayerService, "wagering.Service.ResolvePending", func(ctx context.Context) (int, error) {
		found := 0
		for found < s.cfg.BatchSize {
			resolved, err := s.resolveNext(ctx)
			if err != nil {
				return found, err
			}
			if !resolved {
				break
			}
			found++
		}
		return found, nil
	})
}

// resolveNext claims one due reversal and concludes it, in one unit of work. The row lock that
// claims it is what keeps two workers from resolving the same reversal, and it is released by the
// commit or by the death of the worker, so no lease is needed.
func (s *service) resolveNext(ctx context.Context) (bool, error) {
	var claimed *entities.WagerTransaction
	resolved := false
	err := s.uow.Atomic(ctx, func(ctx context.Context) error {
		tx, err := s.wagering.ClaimDueReference(ctx, s.now())
		if isNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		resolved, claimed = true, tx
		return s.resolveReference(ctx, tx)
	})
	if err == nil && claimed != nil {
		// Counted after the commit, so a resolution that was rolled back is never reported.
		s.recordResolution(ctx, claimed)
	}
	return resolved, err
}

// resolveReference decides a reversal that was waiting. The wallet is locked first, as on every
// write path, and the reference is read after it, so a reference that is being processed on the
// same wallet is either fully there or not there at all.
func (s *service) resolveReference(ctx context.Context, tx *entities.WagerTransaction) error {
	wallet, err := s.wallets.GetForUpdate(ctx, tx.WalletID())
	if err != nil {
		return err
	}
	found, err := s.resolve(ctx, tx)
	if err != nil {
		return err
	}

	now := s.now()
	correlationID := tx.ID().String()
	decision, err := s.decide(wallet, tx, found, correlationID, now)
	if errors.Is(err, errWaitingForReference) {
		return s.keepWaiting(ctx, wallet, tx, found, correlationID, now)
	}
	if err != nil {
		return err
	}
	return s.store(ctx, wallet, tx, decision, update)
}

// keepWaiting either gives up on a reversal whose wait is over, or schedules the next look.
//
// When the wait ends the reversal is rejected, and the code says why: REFERENCE_NOT_FOUND when the
// reference never arrived, REFERENCE_NOT_PROCESSED when it arrived and is still not finished. A
// reference that finished without success does not wait at all; it is refused the moment it is seen.
func (s *service) keepWaiting(ctx context.Context, wallet *entities.Wallet, tx *entities.WagerTransaction, found resolution, correlationID string, now time.Time) error {
	if tx.ReferenceExhausted(now, s.cfg.MaxAttempts) {
		code := entities.FailureReferenceNotFound
		if found.reference != nil {
			code = entities.FailureReferenceNotProcessed
		}
		decision, err := s.rejected(tx, code, correlationID, now)
		if err != nil {
			return err
		}
		return s.store(ctx, wallet, tx, decision, update)
	}

	if err := tx.RetryReference(now.Add(s.referenceBackoff(tx.ReferenceAttempts()+1)), now); err != nil {
		return err
	}
	return s.wagering.Update(ctx, tx)
}

// referenceBackoff is how long to wait after the given attempt: the base doubled on every attempt,
// up to the maximum, spread so instances that looked together do not look together again.
func (s *service) referenceBackoff(attempts int) time.Duration {
	wait := s.cfg.BackoffBase
	for i := 1; i < attempts && wait < s.cfg.BackoffMax; i++ {
		wait *= 2
	}
	if wait > s.cfg.BackoffMax {
		wait = s.cfg.BackoffMax
	}
	return s.jitter(wait)
}

// spread returns a duration within a fifth either side of d.
func spread(d time.Duration) time.Duration {
	window := int64(d) / 5
	if window <= 0 {
		return d
	}
	return d + time.Duration(rand.Int64N(2*window+1)-window)
}

func isNotFound(err error) bool {
	return errors.Is(err, wageringiface.ErrNotFound)
}
