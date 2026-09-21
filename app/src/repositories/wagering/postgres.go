package wagering

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	wageringiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wagering"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// transactionColumns lists every column of the snapshot, in the order the insert binds them.
// RowToStructByName needs the select to name exactly the fields of the snapshot.
var transactionColumns = []string{
	"id", "origin", "kind", "status", "wallet_id", "player_id", "amount_minor", "currency",
	"provider_id", "external_transaction_id", "idempotency_key", "payload_hash", "round_id", "game_id",
	"reference_external_transaction_id", "reference_transaction_id",
	"failure_code", "result_balance_minor", "result_wallet_version",
	"reference_attempts", "reference_next_attempt_at", "reference_expires_at",
	"created_at", "updated_at", "settled_at",
}

var (
	selectTransaction = "SELECT " + strings.Join(transactionColumns, ", ") + " FROM wager_transactions "
	insertTransaction = "INSERT INTO wager_transactions (" + strings.Join(transactionColumns, ", ") +
		") VALUES (" + placeholders(len(transactionColumns)) + ")"
)

// The unique indexes of the schema that Insert and Update translate into the contract's errors.
const (
	indexProviderExternal = "uk_wager_provider_external"
	indexIdempotencyKey   = "uk_wager_idempotency_key"
	indexSingleOpening    = "uk_wager_single_opening"
	indexSingleReversal   = "uk_wager_single_reversal"
)

// expected is the error a span should record for a read: a lookup that finds nothing is an
// answer the caller acts on, not a failure of the repository, and logging it as one would bury the
// real ones.
func expected(err error) error {
	if errors.Is(err, wageringiface.ErrNotFound) {
		return nil
	}
	return err
}

type postgresRepository struct {
	db  *db.Accessor
	obs *observability.Observer
}

// NewPostgresRepository builds the repository over the shared accessor.
func NewPostgresRepository(accessor *db.Accessor, obs *observability.Observer) wageringiface.Repository {
	return &postgresRepository{db: accessor, obs: obs}
}

func placeholders(n int) string {
	marks := make([]string, n)
	for i := range marks {
		marks[i] = fmt.Sprintf("$%d", i+1)
	}
	return strings.Join(marks, ", ")
}

func (r *postgresRepository) Insert(ctx context.Context, transaction *entities.WagerTransaction) error {
	return observability.TraceErr(ctx, r.obs, observability.LayerRepository, "wagering.Repository.Insert", func(ctx context.Context) error {
		s := transaction.Snapshot()
		_, err := r.db.Q(ctx).Exec(ctx, insertTransaction,
			s.ID, s.Origin, s.Kind, s.Status, s.WalletID, s.PlayerID, s.AmountMinor, s.Currency,
			s.ProviderID, s.ExternalTransactionID, s.IdempotencyKey, s.PayloadHash, s.RoundID, s.GameID,
			s.ReferenceExternalTransactionID, s.ReferenceTransactionID,
			s.FailureCode, s.ResultBalanceMinor, s.ResultWalletVersion,
			s.ReferenceAttempts, s.ReferenceNextAttemptAt, s.ReferenceExpiresAt,
			s.CreatedAt, s.UpdatedAt, s.SettledAt)
		if constraint, ok := db.UniqueViolation(err); ok {
			switch constraint {
			case indexProviderExternal, indexIdempotencyKey, indexSingleOpening:
				return wageringiface.ErrDuplicate
			case indexSingleReversal:
				return wageringiface.ErrAlreadyReversed
			}
		}
		if err != nil {
			return fmt.Errorf("insert transaction: %w", db.Classify(err))
		}
		return nil
	})
}

func (r *postgresRepository) Update(ctx context.Context, transaction *entities.WagerTransaction) error {
	return observability.TraceErr(ctx, r.obs, observability.LayerRepository, "wagering.Repository.Update", func(ctx context.Context) error {
		s := transaction.Snapshot()
		// A settled row is never rewritten, whatever the caller believes: the state machine in the
		// domain already refuses the transition, and this refuses it again where it cannot be
		// bypassed.
		tag, err := r.db.Q(ctx).Exec(ctx,
			`UPDATE wager_transactions SET
			status = $2, reference_transaction_id = $3, failure_code = $4,
			result_balance_minor = $5, result_wallet_version = $6,
			reference_attempts = $7, reference_next_attempt_at = $8, reference_expires_at = $9,
			updated_at = $10, settled_at = $11
		 WHERE id = $1 AND status IN ('PENDING', 'PENDING_REFERENCE')`,
			s.ID, s.Status, s.ReferenceTransactionID, s.FailureCode,
			s.ResultBalanceMinor, s.ResultWalletVersion,
			s.ReferenceAttempts, s.ReferenceNextAttemptAt, s.ReferenceExpiresAt,
			s.UpdatedAt, s.SettledAt)
		if constraint, ok := db.UniqueViolation(err); ok && constraint == indexSingleReversal {
			return wageringiface.ErrAlreadyReversed
		}
		if err != nil {
			return fmt.Errorf("update transaction: %w", db.Classify(err))
		}
		if tag.RowsAffected() == 0 {
			return wageringiface.ErrStale
		}
		return nil
	})
}

func (r *postgresRepository) Get(ctx context.Context, id uuid.UUID) (*entities.WagerTransaction, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wagering.Repository.Get")
	transaction, err := r.one(ctx, selectTransaction+"WHERE id = $1", id)
	end(expected(err))
	return transaction, err
}

func (r *postgresRepository) FindByExternal(ctx context.Context, providerID, externalTransactionID string) (*entities.WagerTransaction, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wagering.Repository.FindByExternal")
	transaction, err := r.one(ctx, selectTransaction+"WHERE provider_id = $1 AND external_transaction_id = $2 AND origin = 'EXTERNAL'",
		providerID, externalTransactionID)
	end(expected(err))
	return transaction, err
}

func (r *postgresRepository) FindByKey(ctx context.Context, providerID, idempotencyKey string) (*entities.WagerTransaction, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wagering.Repository.FindByKey")
	transaction, err := r.one(ctx, selectTransaction+"WHERE provider_id = $1 AND idempotency_key = $2 AND origin = 'EXTERNAL'",
		providerID, idempotencyKey)
	end(expected(err))
	return transaction, err
}

func (r *postgresRepository) FindReversalOf(ctx context.Context, providerID, referenceExternalTransactionID string) (*entities.WagerTransaction, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wagering.Repository.FindReversalOf")
	transaction, err := r.one(ctx, selectTransaction+`WHERE provider_id = $1 AND reference_external_transaction_id = $2
		AND status = 'PROCESSED' AND kind IN ('REFUND', 'ROLLBACK')`,
		providerID, referenceExternalTransactionID)
	end(expected(err))
	return transaction, err
}

// ClaimDueReference picks the reversal with SKIP LOCKED for the same reason the outbox does: any
// number of workers can ask at once and none waits for another. The lock is the transaction's, so
// unlike the outbox there is no lease to expire: a worker that dies releases it by dying, and the
// reversal is due again for the next one.
func (r *postgresRepository) ClaimDueReference(ctx context.Context, now time.Time) (*entities.WagerTransaction, error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wagering.Repository.ClaimDueReference")
	transaction, err := r.claimDueReference(ctx, now)
	end(expected(err))
	return transaction, err
}

func (r *postgresRepository) claimDueReference(ctx context.Context, now time.Time) (*entities.WagerTransaction, error) {
	if err := r.db.RequireTransaction(ctx); err != nil {
		return nil, err
	}
	return r.one(ctx, selectTransaction+`WHERE status = 'PENDING_REFERENCE' AND reference_next_attempt_at <= $1
		ORDER BY reference_next_attempt_at FOR UPDATE SKIP LOCKED LIMIT 1`, now)
}

func (r *postgresRepository) one(ctx context.Context, query string, args ...any) (*entities.WagerTransaction, error) {
	rows, err := r.db.Q(ctx).Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("read transaction: %w", db.Classify(err))
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[entities.WagerTransactionSnapshot])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, wageringiface.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read transaction: %w", db.Classify(err))
	}
	return entities.RehydrateWagerTransaction(snapshot)
}
