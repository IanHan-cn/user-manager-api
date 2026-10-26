// Package user 定义业务用户实体与不变量。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
// 用户不持有认证凭据，身份的权威来源是 ZITADEL，本地只保存业务侧映射与展示资料。
package user

import (
	"fmt"
	"net/mail"
	"net/url"
	"regexp"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

type UserStatus string

const (
	UserStatusActive    UserStatus = "active"    // 用户已激活，允许登录
	UserStatusInactive  UserStatus = "inactive"  // 用户未激活，不允许登录
	UserStatusSuspended UserStatus = "suspended" // 用户被暂停，不允许登录
)

// ErrInvalidZitadelUserID 表示外部身份标识非法。
var ErrInvalidZitadelUserID = fmt.Errorf("%w: zitadel_user_id 不能为空", domain.ErrInvalidKey)

// User 业务用户实体,与 ZITADEL 用户通过 ZitadelUserID 关联。
type User struct {
	ID          string // 用户主键 UUIDv7
	Username    string
	DisplayName string
	Email       string
	Phone       string
	AvatarURL   string
	// ZitadelUserID 是外部身份唯一标识，也是同步的幂等依据。
	ZitadelUserID string

	Status        UserStatus
	EmailVerified bool

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Valid 报告状态取值是否在受控词表内。
//
// 从数据库读出的行也要能走这条判定：未知状态一律判为非法（fail closed），
// 与 membership 状态机的口径一致——脏数据不得被当成合法状态继续流转。
func (s UserStatus) Valid() bool {
	switch s {
	case UserStatusActive, UserStatusInactive, UserStatusSuspended:
		return true
	default:
		return false
	}
}

// 字段长度上限：EmailVerified 之外的每个文本字段都在这里定死上限，
// 迁移文件中的列宽必须与之一一对应（见 migrations/000005_create_users）。
const (
	// maxZitadelUserIDLen 限定外部身份标识长度。
	maxZitadelUserIDLen = 64
	// maxUsernameLen 限定用户名长度。
	maxUsernameLen = 64
	// maxDisplayNameLen 与其它聚合共用同一个口径，引用 domain.MaxNameLen。
	maxDisplayNameLen = domain.MaxNameLen
	// maxEmailLen 取 254：RFC 5321 规定的邮箱地址长度上限。
	maxEmailLen = 254
	// maxPhoneLen 限定手机号长度，含国际区号前缀。
	maxPhoneLen = 32
	// maxAvatarURLLen 限定头像地址长度，取常见 CDN 链接的安全上限。
	maxAvatarURLLen = 2048
)

// 用户名、邮箱与手机号的格式约束，与 migrations/000005_create_users 的 CHECK 保持一致。
//
// 方向是"领域层不比数据库宽"：这些正则保证 Go 放行的值一定能落库，
// 否则领域校验通过、数据库 CHECK 拒绝，只会换来一次无法归类的驱动错误。
// 更细的形态约束（例如邮箱里的连续点）交给库函数继续收紧，
// 数据库侧不重复表达——收紧一侧不会造成写入失败。
var (
	// usernamePattern 限定用户名只含小写字母、数字与 . _ - 三个连接符。
	// 不要求以字母开头：用户名在 ZITADEL 侧已做校验，本地只做兜底。
	usernamePattern = regexp.MustCompile(`^[a-z0-9._-]+$`)

	// emailPattern 限定邮箱为"本地部分@带点域名"的整体形态。
	//
	// 只靠 mail.ParseAddress 不够：它接受 a@localhost 这类不带点的域名，
	// 而数据库的 chk_users_email_format 要求域名含点，
	// 于是领域层放行、数据库拒收，错误既不是 ErrInvalidEmail 也不是 ErrUserExists。
	emailPattern = regexp.MustCompile(`^[^\s@]+@[^\s@]+\.[^\s@]+$`)

	// phonePattern 限定手机号为可选前导 + 加 6~20 位数字。
	phonePattern = regexp.MustCompile(`^\+?[0-9]{6,20}$`)
)

var (
	// ErrInvalidUsername 表示用户名格式不合法或超长。
	ErrInvalidUsername = fmt.Errorf("%w: 用户名", domain.ErrInvalidKey)

	// ErrInvalidEmail 表示邮箱格式不合法或超长。
	ErrInvalidEmail = fmt.Errorf("%w: 邮箱", domain.ErrInvalidKey)

	// ErrInvalidPhone 表示手机号格式不合法或超长。
	ErrInvalidPhone = fmt.Errorf("%w: 手机号", domain.ErrInvalidKey)

	// ErrInvalidAvatarURL 表示头像地址不合法：不是 http/https 链接或超长。
	ErrInvalidAvatarURL = fmt.Errorf("%w: 头像地址", domain.ErrInvalidKey)

	// ErrInvalidUserStatus 表示状态取值不在受控词表内。
	ErrInvalidUserStatus = fmt.Errorf("%w: 用户状态", domain.ErrInvalidKey)

	// ErrInvalidUserRecord 表示写入的值被数据库的 CHECK 约束拒绝。
	//
	// 它是兜底而不是主防线：正常路径上每个 CHECK 在领域层都有对应校验
	// （外部标识、用户名、邮箱、手机号、状态、已验证必须有邮箱），
	// 走到这里说明两侧口径已经漂移，或调用方绕过 New 直接改了字段。
	// 归入 domain.ErrInvalidKey：被约束拒绝的仍是"值不合法"，
	// 上层按参数类错误返回 4xx 即可；具体被违反的约束名由适配层带在错误信息里。
	ErrInvalidUserRecord = fmt.Errorf("%w: 用户记录", domain.ErrInvalidKey)
)

// 写操作的结局错误：与 membership 的口径一致，由适配层按 Repository 端口的约定返回。
var (
	// ErrUserExists 表示 zitadel_user_id、username 或 email 已被占用。
	// 适配层撞上唯一约束时必须返回它，不得静默吞掉后返回 nil。
	ErrUserExists = fmt.Errorf("%w: 用户", domain.ErrAlreadyExists)

	// ErrUserNotFound 表示要改动的用户不存在。
	// Update 写入不存在的行时返回它；查询类方法未命中返回 (nil, nil)，不使用本错误。
	ErrUserNotFound = fmt.Errorf("%w: 用户", domain.ErrNotFound)

	// ErrNilUser 表示在空用户上执行了写操作。
	// 以 domain.ErrNilEntity 为基类：空实体是调用方 bug，不是业务规则拒绝了请求。
	ErrNilUser = fmt.Errorf("%w: 用户", domain.ErrNilEntity)
)

// NormalizeUsername 归一化用户名：去空白并转小写。
func NormalizeUsername(username string) string {
	return strings.ToLower(strings.TrimSpace(username))
}

// NormalizeEmail 归一化邮箱：去空白并转小写。
//
// 邮箱本地部分在协议上区分大小写，但本系统把邮箱当登录标识使用，
// 统一按小写存储与比较：否则同一个邮箱会因大小写差异存成两行，
// 且按邮箱查询时打不中唯一索引。
func NormalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}

// NormalizePhone 归一化手机号：仅去空白，保留国际区号的前导 +。
func NormalizePhone(phone string) string {
	return strings.TrimSpace(phone)
}

// Option 在构造用户时设置可选字段，返回 error 表示选项值不合法。
type Option func(*User) error

// WithUsername 设置用户名。空值表示该用户在 ZITADEL 侧未设置用户名（可以邮箱登录）。
func WithUsername(username string) Option {
	return func(u *User) error {
		username = NormalizeUsername(username)
		if username == "" {
			return nil
		}
		if len(username) > maxUsernameLen || !usernamePattern.MatchString(username) {
			return ErrInvalidUsername
		}
		u.Username = username
		return nil
	}
}

// WithDisplayName 设置展示名称，上限与其它聚合一致，见 domain.MaxNameLen。
func WithDisplayName(name string) Option {
	return func(u *User) error {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil
		}
		if len(name) > maxDisplayNameLen {
			return domain.ErrInvalidName
		}
		u.DisplayName = name
		return nil
	}
}

// WithEmail 设置邮箱，内部归一化后校验。
//
// 只接受纯地址形态（"a@example.com"），不接受带显示名的写法
// （"张三 <a@example.com>"）：邮箱是本系统用于登录与查重的标识，
// 收下显示名会让同一地址存出多行。
func WithEmail(email string) Option {
	return func(u *User) error {
		email = NormalizeEmail(email)
		if email == "" {
			return nil
		}
		if len(email) > maxEmailLen {
			return ErrInvalidEmail
		}
		// 先用与数据库 CHECK 同口径的正则卡掉整体形态，再交给 ParseAddress 收紧细节。
		if !emailPattern.MatchString(email) {
			return ErrInvalidEmail
		}
		addr, err := mail.ParseAddress(email)
		if err != nil || addr.Address != email {
			return ErrInvalidEmail
		}
		u.Email = email
		return nil
	}
}

// WithPhone 设置手机号，空值表示未提供。
func WithPhone(phone string) Option {
	return func(u *User) error {
		phone = NormalizePhone(phone)
		if phone == "" {
			return nil
		}
		if len(phone) > maxPhoneLen || !phonePattern.MatchString(phone) {
			return ErrInvalidPhone
		}
		u.Phone = phone
		return nil
	}
}

// WithAvatarURL 设置头像地址，只接受 http/https 链接，空值表示未设置。
func WithAvatarURL(rawURL string) Option {
	return func(u *User) error {
		rawURL = strings.TrimSpace(rawURL)
		if rawURL == "" {
			return nil
		}
		if len(rawURL) > maxAvatarURLLen {
			return ErrInvalidAvatarURL
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || parsed.Host == "" {
			return ErrInvalidAvatarURL
		}
		// 只允许 http/https：javascript: 之类的伪协议会变成 XSS 载体。
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return ErrInvalidAvatarURL
		}
		u.AvatarURL = rawURL
		return nil
	}
}

// WithStatus 设置用户状态，取值必须在 UserStatus 受控词表内。
//
// 初始状态默认是 active（ZITADEL 已确认该身份存在），
// 本选项用于同步 ZITADEL 侧被停用/未激活的用户。
func WithStatus(status UserStatus) Option {
	return func(u *User) error {
		if !status.Valid() {
			return ErrInvalidUserStatus
		}
		u.Status = status
		return nil
	}
}

// New 由 ZITADEL 身份映射构造用户。
//
// zitadelUserID 是唯一必填项：本地用户是 ZITADEL 用户的业务侧映射，
// 身份的权威来源在外部，username、email、phone 在 ZITADEL 侧都可以缺省，
// 缺任何一个都不影响本地行成立，因此一律走 Option 而不是位置参数。
//
// 这里刻意不提供 NewSystem：用户只能由已存在的外部身份映射而来，
// 不存在"由系统凭空创建的用户"这一语义（对比 tenant/membership，
// 它们有 created_by、invited_by 这类需要系统代填的操作者字段）。
//
//	u, err := user.New("zitadel-uid-1",
//		user.WithUsername("alice"),
//		user.WithEmail("Alice@Example.com"),
//		user.WithDisplayName("Alice"),
//	)
func New(zitadelUserID string, opts ...Option) (*User, error) {
	zitadelUserID = strings.TrimSpace(zitadelUserID)
	if zitadelUserID == "" || len(zitadelUserID) > maxZitadelUserIDLen {
		return nil, ErrInvalidZitadelUserID
	}

	now := time.Now()
	u := &User{
		ID:            uuid.NewV7().String(),
		ZitadelUserID: zitadelUserID,
		// 默认 active：外部身份已存在即视为可登录；
		// suspended 与 inactive 由 WithStatus 或后续的状态转换方法写入。
		Status:    UserStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, opt := range opts {
		if err := opt(u); err != nil {
			return nil, err
		}
	}
	return u, nil
}
