package db

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
	"go.uber.org/fx"
	"go.uber.org/zap"

	"github.com/estrategiahq/pedro-test/app/src/libs/config"
)

// NewRedis opens the client used by both the cache and the queue adapters.
func NewRedis(lc fx.Lifecycle, cfg config.Config, log *zap.Logger) (*redis.Client, error) {
	opts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return nil, fmt.Errorf("invalid REDIS_URL: %w", err)
	}

	client := redis.NewClient(opts)

	lc.Append(fx.Hook{
		// The ping runs on start, not in the constructor, so a failure is reported as a
		// component that did not start rather than as a bare construction error.
		OnStart: func(ctx context.Context) error {
			if err := client.Ping(ctx).Err(); err != nil {
				return fmt.Errorf("ping redis: %w", err)
			}
			return nil
		},
		OnStop: func(context.Context) error {
			log.Info("closing redis connection")
			return client.Close()
		},
	})
	return client, nil
}
