// Package config 读取并校验进程配置：环境变量、分段配置与日志级别。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/joho/godotenv"
)

// 允许的环境与日志级别取值。
const (
	AppEnvDev        = "dev"
	AppEnvProduction = "production"
	// 默认值集中定义，避免散落在代码中。
	defaultAppEnv   = AppEnvDev
	defaultLogLevel = "info"

	// 数据库连接池参数。
	defaultDBDSN             = "postgres://postgres:postgres@localhost:5432/postgres?sslmode=disable"
	defaultDBMaxOpenConns    = 25
	defaultDBMaxIdleConns    = 10
	defaultDBConnMaxLifetime = 30 * time.Minute
	defaultDBConnMaxIdleTime = 5 * time.Minute
	defaultDBPingTimeout     = 5 * time.Second

	// HTTP 服务器超时参数，防止慢连接耗尽服务资源。
	defaultHTTPAddr              = ":8080"
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 15 * time.Second
	defaultHTTPWriteTimeout      = 15 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second

	// ZITADEL 会话在 Redis 中的存储参数。
	// 与上面几组不同，这组默认值必须导出：会话存储在配置项为零值时
	// 要能退回同一套值。一旦 TTL 退化为 0，go-redis 会当成永不过期，
	// 会话键将永久驻留。
	DefaultZitadelSessionKeyPrefix      = "zitadel:session:"
	DefaultZitadelSessionFallbackTTL    = 24 * time.Hour
	DefaultZitadelSessionRedisOpTimeout = 2 * time.Second
)

type Config struct {
	// AppEnv 取值为 dev 或 production，决定 Gin 模式与日志格式。
	AppEnv string
	// LogLevel 取值为 debug、info、warn、error，决定日志级别。
	LogLevel string

	Database DatabaseConfig
	Redis    RedisConfig
	HTTP     HTTPConfig
	Zitadel  ZitadelConfig
}
type ZitadelConfig struct {
	Domain string
	Key    string // 任意 32 字节密钥

	ClientID    string
	RedirectURI string

	// 以下是会话在 Redis 中的存储参数。
	// SessionKeyPrefix 会话键前缀，用于与 Redis 中的其它业务数据隔离。
	SessionKeyPrefix string
	// SessionFallbackTTL 是拿不到 ID Token 过期时间时的兜底有效期。
	// 会话没有删除入口，写入时不给 TTL 就会永久驻留，因此不能留空。
	SessionFallbackTTL time.Duration
	// SessionRedisOpTimeout 限制单次 Redis 操作，防止 Redis 抖动拖垮认证链路。
	SessionRedisOpTimeout time.Duration
}
type HTTPConfig struct {
	Addr              string
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
}
type DatabaseConfig struct {
	// DatabaseDSN PostgreSQL 连接字符串，格式为：
	//   postgres://username:password@host:port/dbname?sslmode=disable
	DSN             string
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
	PingTimeout     time.Duration
}

type RedisConfig struct {
	Addr     string
	Password string
	DB       int
}

func Load() (Config, error) {
	envError := godotenv.Load()
	if os.Getenv("AppEnv") != AppEnvProduction {
		slog.Error("failed to load .env file", "error", envError)
	}
	// zitadel
	zitadelCfg := ZitadelConfig{
		Domain:      envOrDefault("ZITADEL_DOMAIN", ""),
		Key:         envOrDefault("ZITADEL_KEY", ""), // 任意 32 字节密钥
		ClientID:    envOrDefault("ZITADEL_CLIENT_ID", ""),
		RedirectURI: envOrDefault("ZITADEL_REDIRECT_URI", ""),

		SessionKeyPrefix:      envOrDefault("ZITADEL_SESSION_KEY_PREFIX", DefaultZitadelSessionKeyPrefix),
		SessionFallbackTTL:    envOrDefaultDuration("ZITADEL_SESSION_FALLBACK_TTL", DefaultZitadelSessionFallbackTTL),
		SessionRedisOpTimeout: envOrDefaultDuration("ZITADEL_SESSION_REDIS_OP_TIMEOUT", DefaultZitadelSessionRedisOpTimeout),
	}
	if zitadelCfg.Domain == "" || zitadelCfg.Key == "" || zitadelCfg.ClientID == "" || zitadelCfg.RedirectURI == "" {
		return Config{}, fmt.Errorf("zitadel config is incomplete")
	}

	// DB
	dbCfg := DatabaseConfig{
		DSN:             envOrDefault("DatabaseDSN", defaultDBDSN),
		MaxOpenConns:    envOrDefaultInt("DatabaseMaxOpenConns", defaultDBMaxOpenConns),
		MaxIdleConns:    envOrDefaultInt("DatabaseMaxIdleConns", defaultDBMaxIdleConns),
		ConnMaxLifetime: envOrDefaultDuration("DatabaseConnMaxLifetime", defaultDBConnMaxLifetime),
		ConnMaxIdleTime: envOrDefaultDuration("DatabaseConnMaxIdleTime", defaultDBConnMaxIdleTime),
		PingTimeout:     envOrDefaultDuration("DatabasePingTimeout", defaultDBPingTimeout),
	}
	// redis
	redisCfg := RedisConfig{
		Addr:     envOrDefault("RedisAddr", "localhost:6379"),
		Password: envOrDefault("RedisPassword", ""),
		DB:       envOrDefaultInt("RedisDB", 0),
	}
	// HTTP
	httpCfg := HTTPConfig{
		Addr:              envOrDefault("HTTPAddr", defaultHTTPAddr),
		ReadHeaderTimeout: envOrDefaultDuration("HTTPReadHeaderTimeout", defaultHTTPReadHeaderTimeout),
		ReadTimeout:       envOrDefaultDuration("HTTPReadTimeout", defaultHTTPReadTimeout),
		WriteTimeout:      envOrDefaultDuration("HTTPWriteTimeout", defaultHTTPWriteTimeout),
		IdleTimeout:       envOrDefaultDuration("HTTPIdleTimeout", defaultHTTPIdleTimeout),
	}
	cfg := Config{
		AppEnv:   envOrDefault("AppEnv", defaultAppEnv),
		LogLevel: envOrDefault("LogLevel", defaultLogLevel),
		Database: dbCfg,
		Redis:    redisCfg,
		HTTP:     httpCfg,
		Zitadel:  zitadelCfg,
	}
	return cfg, nil
}

func (c Config) IsProduction() bool {
	return c.AppEnv == AppEnvProduction
}
func (c Config) LogLevelSlog() slog.Level {
	switch strings.ToLower(c.LogLevel) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func envOrDefault(key, defaultValue string) string {
	if os.Getenv(key) != "" {
		return strings.TrimSpace(os.Getenv(key))
	}
	return defaultValue
}
func envOrDefaultInt(key string, defaultValue int) int {
	if os.Getenv(key) != "" {
		var value int
		_, err := fmt.Sscanf(os.Getenv(key), "%d", &value)
		if err == nil {
			return value
		}
	}
	return defaultValue
}
func envOrDefaultDuration(key string, defaultValue time.Duration) time.Duration {
	if os.Getenv(key) != "" {
		value, err := time.ParseDuration(os.Getenv(key))
		if err == nil {
			return value
		}
	}
	return defaultValue
}
