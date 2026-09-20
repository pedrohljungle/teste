package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

// NewPostgres opens the pgx pool and registers its shutdown with fx, so the pool is also
// closed when the boot fails halfway through.
func NewPostgres(lc fx.Lifecycle, cfg config.Config, log *zap.Logger) (*pgxpool.Pool, error) {
	poolCfg, err := pgxpool.ParseConfig(cfg.DatabaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	if cfg.DatabasePassword != "" {
		poolCfg.ConnConfig.Password = cfg.DatabasePassword
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolCfg)
	if err != nil {
		return nil, fmt.Errorf("open database pool: %w", err)
	}

	lc.Append(fx.Hook{
		OnStart: func(ctx context.Context) error {
			if err := pool.Ping(ctx); err != nil {
				return fmt.Errorf("ping database: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			log.Info("closing postgres pool")
			pool.Close()
			return nil
		},
	})
	return pool, nil
}
