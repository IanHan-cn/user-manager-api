package gorm

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/memories-cn/user-management-api/internal/domain"
	"github.com/memories-cn/user-management-api/internal/domain/tenant"
	"gorm.io/gorm"
)

// 与 user_repository_test.go 同一手法：不连数据库，
// 只跑"不触碰 *gorm.DB 就能判定"的分支，因此可以直接用零值 &TenantRepository{}。
// 一旦哪天这些分支改成先查库，用例会以 panic 的形式立刻失败，
// 而不是悄悄退化成依赖环境的集成测试。

// newTestTenant 造一个领域层认可的租户，供错误翻译用例取用标识。
func newTestTenant(t *testing.T) *tenant.Tenant {
	t.Helper()
	tn, err := tenant.NewSystemTenant("acme", "Acme")
	if err != nil {
		t.Fatalf("构造租户失败：%v", err)
	}
	return tn
}

// TestNewTenantRepository缺少数据库句柄返回错误
func TestNewTenantRepository缺少数据库句柄返回错误(t *testing.T) {
	if _, err := NewTenantRepository(nil); !errors.Is(err, ErrMissingDB) {
		t.Fatalf("err = %v，期望 ErrMissingDB", err)
	}
}

// Test租户写操作遇到空实体返回ErrNilTenant
//
// 空实体是调用方 bug：必须在触碰数据库之前就被拦下，
// 且不得混入业务分类，否则上层会把它当成一次正常的 4xx。
func Test租户写操作遇到空实体返回ErrNilTenant(t *testing.T) {
	r := &TenantRepository{}
	ctx := context.Background()

	cases := map[string]func() error{
		"Create": func() error { return r.Create(ctx, nil) },
		"Update": func() error { return r.Update(ctx, nil) },
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call()
			if !errors.Is(err, tenant.ErrNilTenant) {
				t.Fatalf("err = %v，期望 ErrNilTenant", err)
			}
			if !errors.Is(err, domain.ErrNilEntity) {
				t.Fatalf("err = %v，应归类到 domain.ErrNilEntity", err)
			}
			for _, base := range []error{
				domain.ErrInvalidKey, domain.ErrConflict,
				domain.ErrNotFound, domain.ErrInvalidState,
			} {
				if errors.Is(err, base) {
					t.Fatalf("err = %v，不应归类到 %v", err, base)
				}
			}
		})
	}
}

// Test租户查询方法遇到空入参不查库
//
// 句柄是 nil：只要真的发起查询就会 panic。
// 空标识意味着"这次查询没有依据"，查库只会白白消耗一次往返，
// 更糟的是会把空字符串打进 UUID 列。
func Test租户查询方法遇到空入参不查库(t *testing.T) {
	r := &TenantRepository{}
	ctx := context.Background()

	cases := map[string]func() (*tenant.Tenant, error){
		"GetByID 空串": func() (*tenant.Tenant, error) {
			return r.GetByID(ctx, "")
		},
		"GetByID 纯空白": func() (*tenant.Tenant, error) {
			return r.GetByID(ctx, "   ")
		},
		"GetBySlug 空串": func() (*tenant.Tenant, error) {
			return r.GetBySlug(ctx, "")
		},
		"GetBySlug 纯空白": func() (*tenant.Tenant, error) {
			return r.GetBySlug(ctx, " \t ")
		},
	}

	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := call()
			if got != nil || err != nil {
				t.Fatalf("空入参应返回 (nil, nil)，实际 (%v, %v)", got, err)
			}
		})
	}
}

// Test唯一约束冲突归入ErrTenantExists
//
// 端口要求"撞上 slug 唯一约束必须返回 ErrTenantExists，不得静默吞掉后返回 nil"：
// 创建租户的流程正是靠它区分"slug 已被占用"与"租户已建好"。
func Test租户唯一约束冲突归入ErrTenantExists(t *testing.T) {
	tn := newTestTenant(t)
	err := fmt.Errorf("duplicate key value violates unique constraint %q: %w",
		"idx_tenants_slug_active", gorm.ErrDuplicatedKey)

	got := translateTenantWriteError(err, tn)
	if !errors.Is(got, tenant.ErrTenantExists) {
		t.Fatalf("err = %v，期望 ErrTenantExists", got)
	}
	if !errors.Is(got, domain.ErrConflict) {
		t.Fatalf("err = %v，应归类到 domain.ErrConflict", got)
	}
	if errors.Is(got, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，不应归类到 domain.ErrInvalidKey", got)
	}
	// 底层错误必须保留：真正被违反的约束名只有驱动知道。
	if !errors.Is(got, gorm.ErrDuplicatedKey) {
		t.Fatalf("err = %v，应保留底层错误以便定位约束名", got)
	}
}

// TestCHECK约束违例归入领域参数类错误
//
// tenants 表上有四条 CHECK（slug 格式、状态取值、套餐格式、
// settings 必须是 JSON 对象）外加一条 deleted 一致性约束，
// 违例必须变成可归类的领域错误，否则只能以驱动原生错误的形式冒到上层，
// 落成一个无从分类的 500。
func Test租户CHECK约束违例归入领域参数类错误(t *testing.T) {
	tn := newTestTenant(t)
	err := fmt.Errorf("new row violates check constraint %q: %w",
		"chk_tenants_deleted_consistency", gorm.ErrCheckConstraintViolated)

	got := translateTenantWriteError(err, tn)
	if !errors.Is(got, tenant.ErrInvalidTenantRecord) {
		t.Fatalf("err = %v，期望 ErrInvalidTenantRecord", got)
	}
	if !errors.Is(got, domain.ErrInvalidKey) {
		t.Fatalf("err = %v，应归类到 domain.ErrInvalidKey", got)
	}
	if errors.Is(got, domain.ErrConflict) || errors.Is(got, domain.ErrNotFound) {
		t.Fatalf("err = %v，不应归类到写操作结局类", got)
	}
}

// Test租户遇到未识别的数据库错误原样上抛
//
// 翻译只覆盖已知的两类：连接失败、超时等必须原样冒泡，
// 不得被误判成"slug 已占用"或"记录不合法"。
func Test租户遇到未识别的数据库错误原样上抛(t *testing.T) {
	boom := errors.New("connection reset")
	got := translateTenantWriteError(boom, newTestTenant(t))
	if !errors.Is(got, boom) {
		t.Fatalf("err = %v，期望原样返回底层错误", got)
	}
}

// Test租户Update结果判定的三条分支
func Test租户Update结果判定的三条分支(t *testing.T) {
	tn := newTestTenant(t)

	if err := translateTenantUpdateResult(&gorm.DB{RowsAffected: 1}, tn); err != nil {
		t.Fatalf("改到一行时应返回 nil，实际 %v", err)
	}

	// 没改到任何行即目标不存在：静默成功会让上层误以为更新生效。
	err := translateTenantUpdateResult(&gorm.DB{RowsAffected: 0}, tn)
	if !errors.Is(err, tenant.ErrTenantNotFound) {
		t.Fatalf("err = %v，期望 ErrTenantNotFound", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("err = %v，应归类到 domain.ErrNotFound", err)
	}

	// 出错时走同一套翻译，不得绕过。
	err = translateTenantUpdateResult(&gorm.DB{Error: gorm.ErrDuplicatedKey}, tn)
	if !errors.Is(err, tenant.ErrTenantExists) {
		t.Fatalf("err = %v，期望 ErrTenantExists", err)
	}
}

// Test可写列白名单排除不可变列
//
// id 与 created_at 创建后不可变、updated_at 归触发器维护，
// created_by 是"谁创建的"这一历史事实，换了它等于换了记录的来历。
func Test租户可写列白名单排除不可变列(t *testing.T) {
	for _, forbidden := range []string{"id", "created_at", "updated_at", "created_by"} {
		for _, allowed := range tenantUpdatableColumns {
			if forbidden == allowed {
				t.Fatalf("可写列白名单不应包含 %q", forbidden)
			}
		}
	}
}

// Test状态与软删除时间必须成对写入
//
// 这一条是 tenants 表独有的陷阱：chk_tenants_deleted_consistency 强制
// (status = 'deleted') 恒等于 (deleted_at IS NOT NULL)。
// 白名单里少了 deleted_at，软删除就只能改 status 然后被 CHECK 拒绝；
// 少了 status，删除时间也无处安放。两者必须同时在列内。
func Test状态与软删除时间必须成对写入(t *testing.T) {
	var hasStatus, hasDeletedAt bool
	for _, col := range tenantUpdatableColumns {
		switch col {
		case "status":
			hasStatus = true
		case "deleted_at":
			hasDeletedAt = true
		}
	}
	if !hasStatus {
		t.Fatal("可写列白名单必须包含 status")
	}
	if !hasDeletedAt {
		t.Fatal("可写列白名单必须包含 deleted_at，否则软删除会撞上 deleted 一致性约束")
	}
}

// Test租户更新列值对的键与白名单一致
//
// 两处一旦漂移，GORM 的 Select 会静默忽略白名单之外的键，
// 于是"改了却没生效"，而且没有任何报错。
func Test租户更新列值对的键与白名单一致(t *testing.T) {
	values := toTenantRecord(newTestTenant(t)).toUpdateMap()

	if len(values) != len(tenantUpdatableColumns) {
		t.Fatalf("列值对有 %d 项，白名单有 %d 项", len(values), len(tenantUpdatableColumns))
	}
	for _, col := range tenantUpdatableColumns {
		if _, ok := values[col]; !ok {
			t.Fatalf("列值对缺少白名单中的列 %q", col)
		}
	}
}

// Test软删除时间写入NULL
//
// 未删除的租户 deleted_at 必须是 NULL 而不是零值时间：
// 该列的非空与否既是 idx_tenants_slug_active 部分索引的条件，
// 也是 deleted 一致性 CHECK 的判定依据。
func Test软删除时间写入NULL(t *testing.T) {
	rec := toTenantRecord(newTestTenant(t))
	if rec.DeletedAt != nil {
		t.Fatalf("未删除时 deleted_at 应为 NULL，实际 %v", *rec.DeletedAt)
	}

	deletedAt := time.Now()
	tn := newTestTenant(t)
	tn.Status = tenant.TenantStatusDeleted
	tn.DeletedAt = deletedAt

	rec = toTenantRecord(tn)
	if rec.DeletedAt == nil || !rec.DeletedAt.Equal(deletedAt) {
		t.Fatalf("已删除时 deleted_at 应落库为 %v，实际 %v", deletedAt, rec.DeletedAt)
	}
	if rec.toUpdateMap()["status"] != string(tenant.TenantStatusDeleted) {
		t.Fatal("status 必须与 deleted_at 一起写入")
	}
}

// Test空配置补成空对象
//
// settings 列有 chk_tenants_settings_is_object：nil map 会被序列化成 NULL，
// 写库时才被拒绝，错误现场落在 SQL 里很难反查。
// 写侧与读侧都在这里兜住，保证领域层拿到的永远是非 nil 的 map。
func Test空配置补成空对象(t *testing.T) {
	if got := normalizeSettings(nil); got == nil {
		t.Fatal("nil 配置应补成空 map")
	}

	tn := newTestTenant(t)
	tn.Settings = nil
	if got := toTenantRecord(tn).Settings; got == nil {
		t.Fatal("落库前 nil 配置应补成空 map")
	}

	entity := tenantRecord{Settings: nil}.toEntity()
	if entity.Settings == nil {
		t.Fatal("读回的 nil 配置应补成空 map，否则调用方一次写入就 panic")
	}
}

// Test租户往返转换保留动态配置
//
// Settings 是唯一的 JSONB 列，也是唯一需要浅拷贝语义的字段：
// 转换不得让它变成 nil，也不得丢掉已配置的键值。
func Test租户往返转换保留动态配置(t *testing.T) {
	tn, err := tenant.NewSystemTenant("acme", "Acme",
		tenant.WithSettings(map[string]any{"timezone": "Asia/Shanghai"}))
	if err != nil {
		t.Fatalf("构造租户失败：%v", err)
	}

	got := toTenantRecord(tn).toEntity()
	if got.Settings["timezone"] != "Asia/Shanghai" {
		t.Fatalf("配置丢失：%+v", got.Settings)
	}
	if got.Slug != "acme" || got.Status != tenant.TenantStatusActive {
		t.Fatalf("往返转换丢失字段：%+v", got)
	}
	if !got.DeletedAt.IsZero() {
		t.Fatalf("未删除的租户 DeletedAt 应为零值，实际 %v", got.DeletedAt)
	}
}
