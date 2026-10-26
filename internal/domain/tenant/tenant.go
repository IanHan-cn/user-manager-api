package tenant

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

type TenantStatus string

const (
	TenantStatusActive    TenantStatus = "active"    // 激活
	TenantStatusSuspended TenantStatus = "suspended" // 暂停
	TenantStatusDeleted   TenantStatus = "deleted"   // 删除
)

type Tenant struct {
	ID        string         // 租户主键
	Slug      string         // 租户唯一标识，用于 URL、外部引用等
	Name      string         // 租户展示名称
	Status    TenantStatus   // 租户状态
	PlanCode  string         // 套餐标识，未来需要订阅管理时使用
	Settings  map[string]any // 动态 JSONB 字段,少量租户级可选配置，默认空对象
	CreatedBy string         // 创建租户的用户；允许系统初始化创建
	CreatedAt time.Time      // 创建时间
	UpdatedAt time.Time      // 最后更新时间
	DeletedAt time.Time      // 软删除时间
}

const (
	maxSlugLen = 64
	maxNameLen = 255
	// maxPlanCodeLen 限定套餐标识长度。
	maxPlanCodeLen = 64
)

// SystemCreator 是系统初始化（种子数据、迁移脚本）创建租户时使用的创建者标识。
const SystemCreator = "system"

var (
	keyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)
	// planCodePattern 限定套餐标识，允许小写字母、数字、下划线与连字符。
	planCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)
)

var (
	// ErrInvalidPlanCode 表示套餐标识格式不合法。
	ErrInvalidPlanCode = fmt.Errorf("%w: 套餐标识", domain.ErrInvalidKey)

	// ErrInvalidCreatedBy 表示创建租户的用户标识不能为空。
	ErrInvalidCreatedBy = fmt.Errorf("%w: 创建者不能为空", domain.ErrInvalidKey)
)

func NormalizeKey(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

// Option 在构造租户时设置可选字段，返回 error 表示选项值不合法。
type Option func(*Tenant) error

// WithPlanCode 设置租户套餐标识。空值表示暂不指定套餐，由默认套餐兜底。
func WithPlanCode(code string) Option {
	return func(t *Tenant) error {
		code = NormalizeKey(code)
		if code == "" {
			return nil
		}
		if len(code) > maxPlanCodeLen || !planCodePattern.MatchString(code) {
			return ErrInvalidPlanCode
		}
		t.PlanCode = code
		return nil
	}
}

// WithSettings 设置租户级可选配置，nil 与空 map 均视为未配置。
// 内部做浅拷贝，避免调用方在构造完成后仍能改动实体持有的 map。
func WithSettings(settings map[string]any) Option {
	return func(t *Tenant) error {
		if len(settings) == 0 {
			return nil
		}
		copied := make(map[string]any, len(settings))
		for k, v := range settings {
			copied[k] = v
		}
		t.Settings = copied
		return nil
	}
}

// NewTenant 由指定用户创建租户，createdBy 为创建者用户标识，不能为空。
func NewTenant(slug, name, createdBy string, opts ...Option) (*Tenant, error) {
	createdBy = strings.TrimSpace(createdBy)
	if createdBy == "" {
		return nil, ErrInvalidCreatedBy
	}
	return newTenant(slug, name, createdBy, opts...)
}

// NewSystemTenant 在系统初始化场景下创建租户，创建者固定为 SystemCreator。
func NewSystemTenant(slug, name string, opts ...Option) (*Tenant, error) {
	return newTenant(slug, name, SystemCreator, opts...)
}

// newTenant 校验必填字段并填充默认值，再依次应用可选字段。
func newTenant(slug, name, createdBy string, opts ...Option) (*Tenant, error) {
	slug = NormalizeKey(slug)
	if slug == "" || len(slug) > maxSlugLen || !keyPattern.MatchString(slug) {
		return nil, domain.ErrInvalidSlug
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > maxNameLen {
		return nil, domain.ErrInvalidName
	}

	now := time.Now()
	t := &Tenant{
		ID:     uuid.NewV7().String(),
		Slug:   slug,
		Name:   name,
		Status: TenantStatusActive,
		// Settings 始终为非 nil，JSONB 落库为 {}，且避免 nil map 写入 panic。
		Settings:  make(map[string]any),
		CreatedBy: createdBy,

		CreatedAt: now,
		UpdatedAt: now,
	}

	for _, opt := range opts {
		if err := opt(t); err != nil {
			return nil, err
		}
	}
	return t, nil
}
