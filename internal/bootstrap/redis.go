package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/memories-cn/user-management-api/internal/config"
	"github.com/redis/go-redis/v9"
)

func OpenRedis(cfg config.Config) (*redis.Client, error) {
	redisClient := redis.NewClient(&redis.Options{
		Addr:     cfg.Redis.Addr,
		Password: cfg.Redis.Password,
		DB:       cfg.Redis.DB,
	})
	if err := pingRedis(context.Background(), redisClient, cfg.Database.PingTimeout); err != nil {
		_ = redisClient.Close()
		return nil, err
	}
	return redisClient, nil
}
func pingRedis(ctx context.Context, redis *redis.Client, timeOut time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeOut)
	defer cancel()

	if err := redis.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("Redis 连通性检查失败：%w", err)
	}
	slog.Info("Redis 连通性检查成功")
	return nil
}
