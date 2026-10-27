package membership

import (
	"context"
	"uuid"
)

// Repository 是成员关系聚合的持久化端口，由适配层实现。
//
// # 通用约定
//
// 所有方法都必须满足：
//   - 未命中不是错误：单条查询返回 (nil, nil)，列表查询返回空切片而非 nil；
//   - 只有基础设施故障（连接失败、超时、SQL 执行失败）才返回 error，
//     "查不到数据"必须返回空结果而不是错误；
//   - 成员关系的 Business Key 是 (tenant_id, user_id)，
//     同一对取值在全表只允许存在一行（见下方"重新邀请"小节）；
//     重复插入由数据库唯一约束兜底，实现方必须把约束冲突翻译成
//     ErrMembershipExists 返回，不得静默吞掉后返回 nil（见 Create）；
//   - "找不到记录"只有写操作才报错，且统一返回 ErrMembershipNotFound；
//     查询类方法不适用这一条，它们未命中返回空结果。
//   - created_at 由实现方写入、updated_at 由数据库触发器维护
//     （见 migrations/000001），实现方不得在 SQL 里另行写 updated_at；
//   - 所有写操作必须在事务内执行（ports.Transactor），
//     成员关系与角色/策略投影要同批提交，避免回滚后留下半截状态。
//
// # 租户隔离
//
// Membership 是租户边界内的对象，因此：
//   - 任何写入都必须以 tenant_id 为条件；
//   - 除 GetByUserID 外，所有查询都必须带 tenant_id 条件，不得跨租户返回数据；
//   - GetByUserID 是刻意保留的唯一跨租户查询，
//     用于"该用户加入了哪些租户"这类入口，
//     调用方必须在使用前完成按租户的鉴权，不得把结果整体下发给客户端；
//   - 用 GetByID 取回成员关系后，必须校验返回值的 TenantID 与当前租户一致，
//     或者直接使用 GetByTenantAndUser。
//
// # 重新邀请
//
// 用户离开（left）后想再次加入，不得再插一条新记录：
// (tenant_id, user_id) 全表唯一，再插必然撞约束，而 Create 撞约束是错误而非
// "有则跳过"。正确做法是 GetByTenantAndUser 取回原行，
// 调用 Membership.Reinvite 让它回到 invited，再用 Update 落库——全程只有一行。
// 这也是 left 不是墓碑、而是唯一可经 Reinvite 复活的状态的原因。
//
// # 排序与分页
//
// 列表方法（GetByTenantID、GetByUserID）按 created_at 升序、
// created_at 相同时按 id 升序返回。顺序已固化，
// 后续加入 limit / offset 分页不会改变各页之间的稳定性。
// 当前接口不提供分页参数：成员关系的列表 API 尚未落地，
// 待落地时统一以 ListParams（含 limit、offset、总数）的形态扩展。
type Repository interface {
	// Create 创建成员关系。
	// 主键与初始状态由领域层生成，实现方原样落库，不得改写；
	// 命中 (tenant_id, user_id) 唯一约束时返回 ErrMembershipExists，
	// 不得静默吞掉后返回 nil——那会让上层误以为邀请已经送达。
	// 用户离开后再次加入走"重新邀请"流程（见类型注释），不调用本方法。
	Create(ctx context.Context, m *Membership) error

	// Update 全量更新成员关系的业务字段。
	//
	// 不可写的列为 id、tenant_id、user_id、created_at、updated_at 五项：
	// 前三者是成员关系的业务键，换了其中任何一个就已是另一条关系而不是
	// "改了一下"（见类型注释的"重新邀请"小节）；created_at 不可变，
	// updated_at 归触发器维护。真正会被写入的只有 status 与
	// invited_by、invited_at、joined_at、suspended_at、left_at。
	//
	// 更新必须以 tenant_id 为条件（见类型注释的"租户隔离"小节）：
	// 传入的关系归属与库中不一致时按未命中处理，
	// 返回 ErrMembershipNotFound 而不是去改别的租户的行。
	//
	// 传入空实体返回 ErrNilMembership；
	// id 或 tenant_id 为空同样按"要改的行不存在"处理。
	// 记录不存在时返回 ErrMembershipNotFound：全量更新写入不存在的行属于
	// 调用方 bug，静默成功会让上层误以为更新生效。
	Update(ctx context.Context, m *Membership) error

	// GetByID 按主键查询成员关系，未命中返回 (nil, nil)。
	// 本方法不带 tenant_id 条件，取回后必须校验 TenantID，或直接改用
	// GetByTenantAndUser。
	GetByID(ctx context.Context, id uuid.UUID) (*Membership, error)

	// GetByTenantAndUser 查询指定租户下指定用户的成员关系，
	// 未命中返回 (nil, nil)。租户内的日常鉴权一律使用本方法。
	GetByTenantAndUser(ctx context.Context, tenantID, userID uuid.UUID) (*Membership, error)

	// GetByTenantID 查询指定租户下的全部成员关系，
	// 返回顺序见类型注释的"排序与分页"小节。
	GetByTenantID(ctx context.Context, tenantID uuid.UUID) ([]*Membership, error)

	// GetByUserID 查询指定用户在全部租户下的成员关系（跨租户查询），
	// 返回顺序见类型注释的"排序与分页"小节。
	// 使用前必须完成按租户的鉴权，参见类型注释的"租户隔离"小节。
	GetByUserID(ctx context.Context, userID uuid.UUID) ([]*Membership, error)
}
