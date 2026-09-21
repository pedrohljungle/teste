package wagering

import (
	"context"
	"errors"
	"time"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
	"github.com/estrategiahq/pedro-test/app/src/structs"
)

// The two ways an operation reaches this service. They are a metric dimension, so the same numbers
// can be read for HTTP and for the queue, and compared.
const (
	sourceHTTP = "http"
	sourceSQS  = "sqs"
)

// record notes what one operation came to: how long it took, and what it was, in the metrics and in
// a single log line. It is called once per operation, after the outcome is known.
//
// Only identifiers and bounded labels go out. The amount of an operation is a financial payload, and
// neither the metrics nor the log carry it.
func (s *service) record(ctx context.Context, source string, started time.Time, kind string, outcome structs.WagerOutcome, err error) {
	sourceTag := observability.NewTag("source", source)
	kindTag := observability.NewTag("kind", labelKind(kind))
	s.obs.Measure(ctx, "wager_processing_duration_seconds", time.Since(started), sourceTag, kindTag)

	switch {
	case errors.Is(err, wageringiface.ErrIdempotencyConflict), errors.Is(err, wageringiface.ErrMessageConflict):
		s.obs.Count(ctx, "wager_payload_conflicts_total", sourceTag)
	case errors.Is(err, entities.ErrInvalidTransaction):
		s.obs.Count(ctx, "wager_invalid_operations_total", sourceTag)
	case err != nil:
		if code, unattached := entities.RejectionCode(err); unattached {
			s.obs.Count(ctx, "wager_unattached_rejections_total", sourceTag, observability.NewTag("failure_code", string(code)))
		}
	case outcome.Duplicate:
		s.obs.Count(ctx, "inbox_duplicates_total")
		s.obs.Info(ctx, "message already handled, nothing done")
	case outcome.Replay:
		s.obs.Count(ctx, "wager_idempotent_replays_total", sourceTag)
		s.log(ctx, "wager operation replayed", outcome.Transaction)
	default:
		s.count(ctx, outcome.Transaction, sourceTag)
		s.log(ctx, "wager transaction concluded", outcome.Transaction)
	}
}

// count adds a transaction that reached a state to the result counter, by kind, status and failure
// code: the "results by status" the operators read.
func (s *service) count(ctx context.Context, tx *entities.WagerTransaction, extra ...observability.Tag) {
	tags := append([]observability.Tag{
		observability.NewTag("kind", string(tx.Kind())),
		observability.NewTag("status", string(tx.Status())),
		observability.NewTag("failure_code", string(tx.FailureCode())),
	}, extra...)
	s.obs.Count(ctx, "wager_transactions_total", tags...)
}

// log writes the line that says what happened to a transaction, with the identifiers to find it by.
func (s *service) log(ctx context.Context, message string, tx *entities.WagerTransaction) {
	ctx = observability.WithFields(ctx,
		observability.String("transactionId", tx.ID().String()),
		observability.String("walletId", tx.WalletID().String()),
	)
	s.obs.Info(ctx, message,
		observability.String("kind", string(tx.Kind())),
		observability.String("status", string(tx.Status())),
		observability.String("failureCode", string(tx.FailureCode())),
	)
}

// recordResolution notes what the job that resolves pending references did with a reversal: another
// look, or the outcome that ended the wait.
func (s *service) recordResolution(ctx context.Context, tx *entities.WagerTransaction) {
	if tx.Status() == entities.StatusPendingReference {
		s.obs.Count(ctx, "wager_reference_retries_total")
		return
	}
	s.count(ctx, tx, observability.NewTag("source", "reference-resolver"))
	s.log(ctx, "pending reference concluded", tx)
}

// labelKind keeps the kind label to the values that exist: an operation whose kind is garbage is
// counted as INVALID and does not give the metric a series of its own.
func labelKind(raw string) string {
	kind, err := entities.ParseTransactionKind(raw)
	if err != nil {
		return "INVALID"
	}
	return string(kind)
}
