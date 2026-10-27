package gorm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/memories-cn/user-management-api/internal/domain/tenant"
	"gorm.io/gorm"
)

// TenantRepository 用 GORM 实现 tenant.Repository。
type TenantRepository struct {
	db *gorm.DB
}

var _ tenant.Repository = (*TenantRepository)(nil)

// NewTenantRepository 创建租户仓储。
func NewTenantRepository(db *gorm.DB) (*TenantRepository, error) {
	if db == nil {
		return nil, ErrMissingDB
	}
	return &TenantRepository{db: db}, nil
}

// tenantRecord 是 tenants 表的持久化模型。
//
// deleted_at 用指针类型：库里以 NULL 表示"未删除"
// （见 migrations/000006 的列注释），而领域层用 time.Time 零值表达同一语义。
// 不用指针就会把零值时间写进列里，而该列的非空与否
// 正是 idx_tenants_slug_active 部分索引与 chk_tenants_deleted_consistency
// 的判定依据——写错一个就能让 slug 的唯一性判断整个失效。
//
// settings 走 GORM 内置的 serializer 落 JSONB：
// 领域层持有的是 map[string]any，不引入额外的 JSON 类型依赖。
//
// updated_at 标为只读（<-:false）的理由与 userRecord 一致：
// 该列由数据库触发器维护（见 migrations/000006 的 trg_tenants_set_updated_at），
// 否则 GORM 的自动时间戳会与触发器抢写同一列。
type tenantRecord struct {
	ID        string         `gorm:"column:id;primaryKey"`
	Slug      string         `gorm:"column:slug"`
	Name      string         `gorm:"column:name"`
	Status    string         `gorm:"column:status"`
	PlanCode  string         `gorm:"column:plan_code"`
	Settings  map[string]any `gorm:"column:settings;type:jsonb;serializer:json"`
	CreatedBy string         `gorm:"column:created_by"`
	CreatedAt time.Time      `gorm:"column:created_at"`
	UpdatedAt time.Time      `gorm:"column:updated_at;<-:false"`
	DeletedAt *time.Time     `gorm:"column:deleted_at"`
}

// TableName 固定表名为 tenants，不依赖 GORM 默认的复数转换。
func (tenantRecord) TableName() string { return "tenants" }

// tenantUpdatableColumns 是 Update 允许写入的列，与 toUpdateMap 的键一一对应。
//
// 刻意不含 id、created_at、updated_at：前两者创建后不可变，后者归触发器维护。
// created_by 同样排除：它是"创建租户的人"这一历史事实，
// 换了创建者就等于换了一条记录的来历，不是一次修改。
//
// deleted_at 必须在列内：它与 status 由 chk_tenants_deleted_consistency
// 强制同步（status = 'deleted' 当且仅当 deleted_at 非空），
// 只写 status 会被该 CHECK 直接拒绝，软删除也就无从落地。
var tenantUpdatableColumns = []string{
	"slug",
	"name",
	"status",
	"plan_code",
	"settings",
	"deleted_at",
}

// normalizeSettings 保证 settings 落库前非空。
//
// 领域层由 newTenant 保证该字段非 nil，但 Update 拿到的是可能被外部改过的实体，
// 而 settings 列有 chk_tenants_settings_is_object（jsonb_typeof = 'object'）：
// nil map 会被序列化成 NULL，写库时才被 CHECK 拒绝，
// 错误现场落在 SQL 里、很难反查源头。这里提前兜住。
func normalizeSettings(settings map[string]any) map[string]any {
	if settings == nil {
		return map[string]any{}
	}
	return settings
}

// toTenantRecord 把领域实体转成持久化模型。
func toTenantRecord(t *tenant.Tenant) tenantRecord {
	return tenantRecord{
		ID:        t.ID,
		Slug:      t.Slug,
		Name:      t.Name,
		Status:    string(t.Status),
		PlanCode:  t.PlanCode,
		Settings:  normalizeSettings(t.Settings),
		CreatedBy: t.CreatedBy,
		CreatedAt: t.CreatedAt,
		UpdatedAt: t.UpdatedAt,
		DeletedAt: toNullableTime(t.DeletedAt),
	}
}

// toEntity 把持久化模型还原成领域实体。
//
// Settings 为空时补成空 map：领域层的约定是"Settings 始终为非 nil"
// （见 tenant.newTenant），读回 nil 会让调用方一次写入就 panic。
func (r tenantRecord) toEntity() *tenant.Tenant {
	return &tenant.Tenant{
		ID:        r.ID,
		Slug:      r.Slug,
		Name:      r.Name,
		Status:    tenant.TenantStatus(r.Status),
		PlanCode:  r.PlanCode,
		Settings:  normalizeSettings(r.Settings),
		CreatedBy: r.CreatedBy,
		CreatedAt: r.CreatedAt,
		UpdatedAt: r.UpdatedAt,
		DeletedAt: fromNullableTime(r.DeletedAt),
	}
}

// toUpdateMap 给出 Update 的列值对，键集合必须与 tenantUpdatableColumns 一致。
//
// 用 map 而不是结构体：按结构体更新时零值字段会被 GORM 跳过，
// 而"清空 settings"（改回空对象）与"清空 deleted_at"（撤销软删除）
// 都是需要真正落库的取值。
func (r tenantRecord) toUpdateMap() map[string]any {
	return map[string]any{
		"slug":       r.Slug,
		"name":       r.Name,
		"status":     r.Status,
		"plan_code":  r.PlanCode,
		"settings":   r.Settings,
		"deleted_at": r.DeletedAt,
	}
}

// translateTenantWriteError 把数据库错误翻译成领域错误，不把驱动原生错误抛给上层。
//
// 只认 TranslateError 已经翻成 GORM 语义错误的那两类（见 bootstrap.OpenDB）：
//   - ErrDuplicatedKey（23505）：撞上 idx_tenants_slug_active，归入 ErrTenantExists；
//   - ErrCheckConstraintViolated（23514）：slug 格式、套餐格式、
//     settings 不是 JSON 对象、或 status 与 deleted_at 不同步，归入 ErrInvalidTenantRecord。
//
// 单独成函数是为了让这两条判定能脱离数据库被测试：它们只看错误类型，不需要真的连一次库。
func translateTenantWriteError(err error, t *tenant.Tenant) error {
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: slug=%s: %w", tenant.ErrTenantExists, t.Slug, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: id=%s: %w", tenant.ErrInvalidTenantRecord, t.ID, err)
	default:
		return err
	}
}

// translateTenantUpdateResult 判定 Update 的执行结果：出错翻译成领域错误，
// 没改到任何行返回 ErrTenantNotFound。
//
// 单独成函数是为了让这两条判定能脱离数据库被测试——
// 它们只看 Error 与 RowsAffected，不需要真的连一次库。
func translateTenantUpdateResult(result *gorm.DB, t *tenant.Tenant) error {
	if result.Error != nil {
		return translateTenantWriteError(result.Error, t)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: id=%s", tenant.ErrTenantNotFound, t.ID)
	}
	return nil
}

// Create 写入租户，撞上 slug 唯一约束时返回 ErrTenantExists。
//
// 错误怎么翻译见 translateTenantWriteError。
func (r *TenantRepository) Create(ctx context.Context, t *tenant.Tenant) error {
	if t == nil {
		return tenant.ErrNilTenant
	}

	rec := toTenantRecord(t)
	db := dBFromContext(ctx, r.db)
	if err := db.WithContext(ctx).Create(&rec).Error; err != nil {
		return translateTenantWriteError(err, t)
	}
	return nil
}

// Update 全量更新租户的业务字段，记录不存在时返回 ErrTenantNotFound。
//
// 只写 tenantUpdatableColumns 列出的列；
// status 与 deleted_at 成对写入，updated_at 由触发器维护，本方法不写。
func (r *TenantRepository) Update(ctx context.Context, t *tenant.Tenant) error {
	if t == nil {
		return tenant.ErrNilTenant
	}

	rec := toTenantRecord(t)
	db := dBFromContext(ctx, r.db)
	result := db.WithContext(ctx).
		Model(&tenantRecord{}).
		Where("id = ?", t.ID).
		Select(tenantUpdatableColumns).
		Updates(rec.toUpdateMap())
	return translateTenantUpdateResult(result, t)
}

// GetByID 按主键查询租户，未命中返回 (nil, nil)。
//
// 刻意不带软删除条件：按主键取回的行即使已删除也要返回，
// 否则"删除后回看这条租户"无从实现。只想要未删除的租户时，
// 由调用方自行判定 Status 或 DeletedAt。
//
// 空标识表示"没有依据可查"，直接短路：把它打进 UUID 列只会换来一次
// 不知所云的类型转换错误，而约定的答案是 (nil, nil)。
func (r *TenantRepository) GetByID(ctx context.Context, id string) (*tenant.Tenant, error) {
	normalized := strings.TrimSpace(id)
	if normalized == "" {
		return nil, nil
	}
	return r.findOne(ctx, "id = ?", normalized)
}

// GetBySlug 按唯一标识查询未删除的租户，未命中返回 (nil, nil)。
//
// deleted_at IS NULL 是必须带上的条件而不是优化：
// idx_tenants_slug_active 是建在该条件上的部分索引，
// slug 的唯一性只对未删除的租户成立（见端口注释的"软删除"小节）。
// 少了这一条件就可能命中墓碑行，返回一个本应已被释放的租户。
//
// 入参在查询前先经 NormalizeKey 归一化：库里存的是归一化小写值。
func (r *TenantRepository) GetBySlug(ctx context.Context, slug string) (*tenant.Tenant, error) {
	normalized := tenant.NormalizeKey(slug)
	if normalized == "" {
		return nil, nil
	}
	return r.findOne(ctx, "deleted_at IS NULL AND slug = ?", normalized)
}

// findOne 按条件取一条租户，未命中返回 (nil, nil)。
func (r *TenantRepository) findOne(ctx context.Context, cond string, args ...string) (*tenant.Tenant, error) {
	for _, arg := range args {
		if arg == "" {
			return nil, nil
		}
	}

	var rec tenantRecord
	db := dBFromContext(ctx, r.db)
	err := db.WithContext(ctx).Where(cond, toAnys(args)...).Take(&rec).Error
	if err != nil {
		if isRecordNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("查询租户失败：%w", err)
	}
	return rec.toEntity(), nil
}
