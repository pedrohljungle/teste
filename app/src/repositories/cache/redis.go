package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/estrategiahq/pedro-test/app/src/libs/observability"
)

// RedisRepository is a key-value cache over Redis. What to cache and for how long is decided
// by the caller; only the key I/O lives here.
type RedisRepository struct {
	client *redis.Client
	obs    *observability.Observer
}

// NewRedisRepository builds the adapter.
func NewRedisRepository(client *redis.Client, obs *observability.Observer) *RedisRepository {
	return &RedisRepository{client: client, obs: obs}
}

// Get returns the value, whether it was found, and an error. A missing key is not an error:
// it is the normal path of a cache-aside read.
func (r *RedisRepository) Get(ctx context.Context, key string) (value string, found bool, err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "cache.Get",
		observability.String("key", key),
	)
	defer func() { end(err) }()

	val, err := r.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			// Returning nil here is what keeps a cold cache from being reported as a
			// failure.
			return "", false, nil
		}
		return "", false, fmt.Errorf("read cache key: %w", err)
	}
	return val, true, nil
}

// Set writes the value with the TTL chosen by the caller.
func (r *RedisRepository) Set(ctx context.Context, key, value string, ttl time.Duration) (err error) {
	ctx, end := r.obs.Start(ctx, observability.LayerRepository, "cache.Set",
		observability.String("key", key),
	)
	defer func() { end(err) }()

	if err := r.client.Set(ctx, key, value, ttl).Err(); err != nil {
		return fmt.Errorf("write cache key: %w", err)
	}
	return nil
}
