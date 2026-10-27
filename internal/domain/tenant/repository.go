// internal/domain/tenant/repository.go
package tenant

import (
	"context"
)

// Repository 是租户聚合的持久化端口，由适配层实现。
//
// # 通用约定
//
//   - 未命中不是错误：单条查询返回 (nil, nil)；
//   - 只有基础设施故障（连接失败、超时、SQL 执行失败）才返回 error，
//     "查不到数据"必须返回空结果而不是错误；
//   - created_at 由实现方写入、updated_at 由数据库触发器维护
//     （见 migrations/000001），实现方不得在 SQL 里另行写 updated_at；
//   - 所有写操作必须在事务内执行（ports.Transactor）。
//
// # 软删除
//
// 租户不做物理删除：deleted_at 非空即为已删除，
// 且 chk_tenants_deleted_consistency 强制它恒等于 (status = 'deleted')。
// 两个表示必须同步翻转，实现方在写路径上不得只改其中一个——
// 只改一个会被该 CHECK 直接拒绝，而不是留下一条半删除的行。
//
// 由此也决定了 slug 的唯一性口径：idx_tenants_slug_active 是
// deleted_at IS NULL 上的部分索引，唯一性只在未删除的租户间成立，
// 因此 GetBySlug 必须自带 deleted_at IS NULL 条件，
// 否则会命中已删除的墓碑行，返回本应已被释放的租户。
type Repository interface {
	// Create 创建租户。
	// 主键与 created_at 由领域层生成，实现方原样落库，不得改写；
	// updated_at 是唯一的例外——它由触发器写入（见类型注释的"通用约定"小节），
	// 实现方既不要给它赋值，也不要把它放进 INSERT。
	// 命中 slug 唯一约束时返回 ErrTenantExists，
	// 不得静默吞掉后返回 nil——那会让上层误以为租户已经建好。
	// 传入空实体返回 ErrNilTenant。
	Create(ctx context.Context, t *Tenant) error

	// GetByID 按主键查询租户，未命中返回 (nil, nil)。
	//
	// 本方法不带软删除条件：按主键取回的行即使已删除也要返回，
	// 否则"删除后回看这条租户"这类场景无从实现。
	// 只想要未删除的租户时，调用方自行判定 Status 或 DeletedAt。
	//
	// 空标识表示"没有依据可查"，返回 (nil, nil)：把它打进 UUID 主键列
	// 只会换来一次不知所云的类型转换错误。
	GetByID(ctx context.Context, id string) (*Tenant, error)

	// GetBySlug 按唯一标识查询租户，未命中返回 (nil, nil)。
	//
	// 必须自带 deleted_at IS NULL：slug 的唯一性只对未删除的租户成立
	// （见类型注释的"软删除"小节），少了这一条件就会返回已被释放的 slug
	// 对应的墓碑行。
	//
	// 空 slug 表示"没有依据可查"，返回 (nil, nil)；
	// 入参在查询前须经 NormalizeKey 归一化，库里存的是归一化小写值。
	GetBySlug(ctx context.Context, slug string) (*Tenant, error)

	// Update 全量更新租户的业务字段。
	//
	// 不可写的列为 id、created_at、created_by、updated_at 四项：
	// id 与 created_at 创建后不可变；created_by 是"谁创建的"这一历史事实，
	// 换了它等于换了一条记录的来历，不是一次修改；updated_at 由触发器维护。
	// 其余列（含 slug、status、deleted_at、settings、plan_code）均可写。
	//
	// status 与 deleted_at 必须成对写入：二者由 CHECK 约束强制同步
	// （见类型注释的"软删除"小节），只写其一会被数据库拒绝。
	//
	// 传入空实体返回 ErrNilTenant；主键为空同样按"要改的行不存在"处理，
	// 不得把它打进 UUID 列去换一次类型错误。
	// 记录不存在时返回 ErrTenantNotFound：更新不存在的行属于调用方 bug，
	// 静默成功会让上层误以为更新生效。
	Update(ctx context.Context, t *Tenant) error
}
