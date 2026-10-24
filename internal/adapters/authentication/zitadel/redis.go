package zitadel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/memories-cn/user-management-api/internal/config"
	"github.com/redis/go-redis/v9"
	"github.com/zitadel/zitadel-go/v3/pkg/authentication"
	openid "github.com/zitadel/zitadel-go/v3/pkg/authentication/oidc"
)

// 静态检查 RedisSessionStore 是否实现了 authentication.Sessions[T] 接口。
//
// Sessions 的类型参数受 authentication.Ctx 约束（要求 IsAuthenticated() bool），
// 因此这里必须用满足该约束的具体上下文类型实例化，本仓库统一使用 *openid.DefaultContext。
var _ authentication.Sessions[*openid.DefaultContext] = (*RedisSessionStore[*openid.DefaultContext])(nil)

// expiredSessionTTL 用于写入时就已经过期的会话：照常写入但立刻失效。
// 这里不返回错误，是为了让过期会话走完上层流程由 IsAuthenticated 判定，
// 而不是让 Set 失败直接打断登录回调。
//
// 该值不作为配置项暴露：它是“已过期会话尽快消失”的实现细节，
// 配成较大值只会让过期会话滞留在 Redis 里。
const expiredSessionTTL = time.Second

// ErrSessionNotFound 表示会话不存在、已过期或被清理。
// Redis 故障不属于该错误，两者要能被区分开。
var ErrSessionNotFound = errors.New("session not found")

// RedisSessionStore 把 ZITADEL 的会话存到 Redis，替代默认的内存实现。
//
// 类型参数必须受 authentication.Ctx 约束，否则无法满足 authentication.Sessions[T]。
type RedisSessionStore[T authentication.Ctx] struct {
	redisClient *redis.Client
	keyPrefix   string
	fallbackTTL time.Duration
	opTimeout   time.Duration
}

// NewRedisSessionStore 使用配置中的会话存储参数构造存储实例。
//
// 配置项缺失（零值）时退回 config 的默认值，这是必须的防御：
// TTL 一旦取到 0，go-redis 会当成永不过期，而会话没有删除入口
// （authentication.Sessions 只有 Set/Get，Logout 仅清理浏览器 Cookie），
// 键会永久驻留并不断膨胀。
func NewRedisSessionStore[T authentication.Ctx](redisClient *redis.Client, cfg config.ZitadelConfig) *RedisSessionStore[T] {
	fallbackTTL := cfg.SessionFallbackTTL
	if fallbackTTL <= 0 {
		fallbackTTL = config.DefaultZitadelSessionFallbackTTL
	}
	opTimeout := cfg.SessionRedisOpTimeout
	if opTimeout <= 0 {
		opTimeout = config.DefaultZitadelSessionRedisOpTimeout
	}
	keyPrefix := cfg.SessionKeyPrefix
	if keyPrefix == "" {
		keyPrefix = config.DefaultZitadelSessionKeyPrefix
	}

	return &RedisSessionStore[T]{
		redisClient: redisClient,
		keyPrefix:   keyPrefix,
		fallbackTTL: fallbackTTL,
		opTimeout:   opTimeout,
	}
}

// Set 写入会话，键的 TTL 与会话自身的有效期保持一致。
//
// Sessions 契约只负责存取，会话是否有效由上层 IsAuthenticated 判断，
// 因此这里不额外做过期校验，也不重复调用 IsAuthenticated。
func (s *RedisSessionStore[T]) Set(id string, session T) error {
	// JSON 是 zitadel 自身采用的编解码方式：它的无状态 Cookie 会话模式
	// 就是把 *openid.DefaultContext 直接 json.Marshal 进 Cookie，
	// UserInfo.Subject 与 ID Token 的 exp 均带 json tag，往返不会丢字段。
	data, err := json.Marshal(session)
	if err != nil {
		return fmt.Errorf("序列化会话失败：%w", err)
	}

	ctx, cancel := s.opContext()
	defer cancel()

	if err := s.redisClient.Set(ctx, s.key(id), data, s.sessionTTL(session)).Err(); err != nil {
		return fmt.Errorf("写入会话到 Redis 失败：%w", err)
	}
	return nil
}

// Get 读取会话。会话不存在返回 ErrSessionNotFound，Redis 故障返回包装错误。
// 上层（Authenticator.IsAuthenticated）对二者一律按未认证处理，天然是 fail closed。
func (s *RedisSessionStore[T]) Get(id string) (T, error) {
	var session T

	ctx, cancel := s.opContext()
	defer cancel()

	data, err := s.redisClient.Get(ctx, s.key(id)).Bytes()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return session, ErrSessionNotFound
		}
		return session, fmt.Errorf("读取会话失败：%w", err)
	}

	// session 是零值（T 为指针类型时即 nil），&session 由 json 负责分配具体值，
	// 与 zitadel 无状态模式下 json.Unmarshal(data, &t) 的处理保持一致。
	if err := json.Unmarshal(data, &session); err != nil {
		return session, fmt.Errorf("反序列化会话失败：%w", err)
	}
	return session, nil
}

func (s *RedisSessionStore[T]) key(id string) string {
	return s.keyPrefix + id
}

func (s *RedisSessionStore[T]) opContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), s.opTimeout)
}

// sessionTTL 推导会话剩余有效期，优先采用会话自身的过期时间。
//
// *openid.DefaultContext 的过期时间来自 ID Token 的 exp，zitadel 计算会话 Cookie 的
// maxAge 用的也是它，两边同源可以保证 Redis 中的会话不会比 Cookie 活得更久。
func (s *RedisSessionStore[T]) sessionTTL(session T) time.Duration {
	// T 的约束 authentication.Ctx 只保证 IsAuthenticated，
	// 过期时间需要与 zitadel 相同的结构化断言来取。
	provider, ok := any(session).(interface{ GetExpiration() time.Time })
	if !ok {
		return s.fallbackTTL
	}

	exp := provider.GetExpiration()
	if exp.IsZero() {
		return s.fallbackTTL
	}
	if ttl := time.Until(exp); ttl > 0 {
		return ttl
	}
	return expiredSessionTTL
}
