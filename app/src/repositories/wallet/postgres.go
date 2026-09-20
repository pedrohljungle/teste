package wallet

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/estrategiahq/pedro-test/app/src/entities"
	walletiface "github.com/estrategiahq/pedro-test/app/src/interfaces/wallet"
	"github.com/estrategiahq/pedro-test/app/src/libs/db"
	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

const walletColumns = "id, player_id, currency, balance_minor, version, created_at, updated_at"

// expected is the error a span should record for a read: a lookup that finds nothing is an
// answer the caller acts on, not a failure of the repository, and logging it as one would bury the
// real ones.
func expected(err error) error {
	if errors.Is(err, walletiface.ErrNotFound) {
		return nil
	}
	return err
}

var _ walletiface.Repository = (*postgresRepository)(nil)

type postgresRepository struct {
	db  *db.Accessor
	obs *observability.Observer
}

// NewPostgresRepository builds the repository. It receives the accessor and not the pool, so a
// write cannot bypass the transaction of the unit of work it runs in.
func NewPostgresRepository(accessor *db.Accessor, obs *observability.Observer) walletiface.Repository {
	return &postgresRepository{db: accessor, obs: obs}
}

func (r *postgresRepository) Insert(ctx context.Context, wallet *entities.Wallet) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wallet.Repository.Insert")
	defer func() { end(err) }()

	s := wallet.Snapshot()
	_, err = r.db.Q(ctx).Exec(ctx,
		`INSERT INTO wallets (`+walletColumns+`) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		s.ID, s.PlayerID, s.Currency, s.BalanceMinor, s.Version, s.CreatedAt, s.UpdatedAt)
	if constraint, ok := db.UniqueViolation(err); ok && constraint == "uk_wallet_player_currency" {
		return walletiface.ErrAlreadyExists
	}
	if err != nil {
		return fmt.Errorf("insert wallet: %w", db.Classify(err))
	}
	return nil
}

func (r *postgresRepository) Get(ctx context.Context, id uuid.UUID) (wallet *entities.Wallet, err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wallet.Repository.Get")
	defer func() { end(expected(err)) }()

	return r.read(ctx, id, "")
}

func (r *postgresRepository) GetForUpdate(ctx context.Context, id uuid.UUID) (wallet *entities.Wallet, err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wallet.Repository.GetForUpdate")
	defer func() { end(expected(err)) }()

	if err := r.db.RequireTransaction(ctx); err != nil {
		return nil, err
	}
	return r.read(ctx, id, " FOR UPDATE")
}

func (r *postgresRepository) read(ctx context.Context, id uuid.UUID, lock string) (*entities.Wallet, error) {
	rows, err := r.db.Q(ctx).Query(ctx, `SELECT `+walletColumns+` FROM wallets WHERE id = $1`+lock, id)
	if err != nil {
		return nil, fmt.Errorf("read wallet: %w", db.Classify(err))
	}
	snapshot, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[entities.WalletSnapshot])
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, walletiface.ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read wallet: %w", db.Classify(err))
	}
	return entities.RehydrateWallet(snapshot)
}

func (r *postgresRepository) Update(ctx context.Context, wallet *entities.Wallet) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wallet.Repository.Update")
	defer func() { end(err) }()

	if err := r.db.RequireTransaction(ctx); err != nil {
		return err
	}
	s := wallet.Snapshot()
	tag, err := r.db.Q(ctx).Exec(ctx,
		`UPDATE wallets SET balance_minor = $2, version = $3, updated_at = $4
		 WHERE id = $1 AND version = $5`,
		s.ID, s.BalanceMinor, s.Version, s.UpdatedAt, wallet.ExpectedVersion())
	if err != nil {
		return fmt.Errorf("update wallet: %w", db.Classify(err))
	}
	if tag.RowsAffected() == 0 {
		return walletiface.ErrConcurrentUpdate
	}
	return nil
}

func (r *postgresRepository) InsertEntry(ctx context.Context, entry entities.LedgerEntry) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "wallet.Repository.InsertEntry")
	defer func() { end(err) }()

	if err := r.db.RequireTransaction(ctx); err != nil {
		return err
	}
	s := entry.Snapshot()
	_, err = r.db.Q(ctx).Exec(ctx,
		`INSERT INTO wallet_ledger_entries
			(id, wallet_id, transaction_id, direction, amount_minor, currency,
			 balance_before_minor, balance_after_minor, created_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		s.ID, s.WalletID, s.TransactionID, s.Direction, s.AmountMinor, s.Currency,
		s.BalanceBeforeMinor, s.BalanceAfterMinor, s.CreatedAt)
	if constraint, ok := db.UniqueViolation(err); ok && constraint == "uk_ledger_wallet_transaction" {
		return walletiface.ErrDuplicateMovement
	}
	if err != nil {
		return fmt.Errorf("insert ledger entry: %w", db.Classify(err))
	}
	return nil
}
