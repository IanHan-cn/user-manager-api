package role

import (
	"context"
)

// Repository 提供租户角色与角色权限的持久化能力，由适配层实现。
//
// 所有方法都必须带 tenant_id 条件：角色不得跨租户使用。
type Repository interface {
	// Create 创建角色。
	// 主键与时间戳由领域层生成，实现方原样落库，不得改写；
	// 命中 (tenant_id, key) 唯一约束时返回 ErrRoleExists，
	// 不得静默吞掉后返回 nil——那会让上层误以为角色已经建好。
	// 传入空实体返回 ErrNilRole。
	Create(ctx context.Context, r *Role) error
	// FindByID 查询指定租户下的角色，未命中返回 (nil, nil)。
	//
	// 标识为空表示"没有依据可查"，返回 (nil, nil)；
	// 标识非空但非法（不是合法 UUID）返回以 ErrInvalidTenantID / ErrInvalidRoleID
	// 为基类的错误——那是调用方 bug，静默当成"查不到"会把程序缺陷伪装成正常的业务结果。
	FindByID(ctx context.Context, tenantID, roleID string) (*Role, error)
	// FindByKey 查询指定租户下指定标识的角色，未命中返回 (nil, nil)。
	//
	// 租户标识与 FindByID 同口径；角色标识是业务 key 而不是 UUID，
	// 取值非法不构成错误——一个不存在的 key 本来就是"未命中"。
	FindByKey(ctx context.Context, tenantID, key string) (*Role, error)
	// ReplacePermissions 用给定权限集合整体替换角色的权限关联。
	//
	// 语义是"传进来的集合就是最终结果"，而不是追加：不在集合里的旧关联必须全部消失，
	// 否则管理员撤销权限的结果是权限越攒越多。实现方允许做增量差分以稳定 created_at，
	// 但对外效果必须与"全删再全插"一致。
	//
	// 入参先经 NormalizePermissionIDs 收敛：去空白、去空串、去重、校验 UUID，
	// 任一项非法即返回错误且不得写入任何数据。
	//
	// 原子性由实现方保证，不转嫁给调用方：替换要么整体生效、要么完全不生效，
	// 一次失败的替换不能让角色半途失去部分权限。
	// 实现方因此必须把删除与插入放进同一个事务。
	//
	// 但"本方法内部原子"不等于"跨表原子"：若调用方还需要让这次替换与其它写入
	// （最典型的是 Casbin 策略投影，见 internal/ports/policy.go）保持原子，
	// 必须由调用方开启事务并经 context 传入，实现方会自动嵌套进该事务
	// （此时删除与插入与外层事务同生共死）。
	// 反之，只调用本方法时，实现方自建的事务已足以保证"整体替换"成立。
	//
	// 角色不属于本租户时返回 ErrRoleNotFound：本表冗余了 tenant_id，
	// 但没有外键兜底，这个校验是跨租户写入的唯一防线。
	//
	// 待写入的权限标识在系统权限目录中不存在时返回以 ErrInvalidPermissionID
	// 为基类的错误，同样不得写入任何数据：permission_id 列同样没有外键兜底，
	// 写入不存在的权限会留下孤儿行，让角色"持有"一项无法鉴权的权限。
	ReplacePermissions(ctx context.Context, tenantID, roleID string, permissionIDs []string) error

	// ListPermissionIDs 返回指定租户下角色当前关联的权限标识集合。
	//
	// 它与 ReplacePermissions 必须成对存在：整体替换是写口，写入的结果若读不回来，
	// 调用方既无法校验本次替换是否生效，也无法判断一次失败的替换留下了什么中间状态。
	// 两者的取值口径刻意一致（都是 permissions.id 的 UUID 字符串），
	// 使"读出来再写回去"是一次幂等的往返。
	//
	// 未命中不是错误：
	//   - 角色还没有任何权限，返回空切片；
	//   - 角色不存在，同样返回空切片。
	//
	// 刻意不去区分上面两种情形：判断角色是否存在是 FindByID 的职责，
	// 本方法兼任会让每个只想看权限的调用方都得多做一次判空语义的处理，
	// 而 ErrNotFound 按全仓约定只用于"要写却写不到"的场景（见 domain/error.go）。
	//
	// 但"没有依据可查"与"查错了"是两回事，实现方必须区分：
	//   - 标识为空（例如调用方忘了传租户 ID），返回空切片——没有可查的东西不是错误；
	//   - 标识非空但非法（例如两个标识传反了，或 UUID 拼错了），
	//     返回以 ErrInvalidTenantID / ErrInvalidRoleID 为基类的错误——
	//     那是调用方 bug，静默当成"查不到"会把程序缺陷伪装成正常的业务结果。
	//
	// 顺序不定，可能与写进去的顺序不同：实现方不做排序，也不依赖索引顺序返回，
	// 过滤条件 (tenant_id, role_id) 走 idx_role_permissions_tenant_role 前缀。
	// 需要按某种次序展示时，由调用方自行排序。
	ListPermissionIDs(ctx context.Context, tenantID, roleID string) ([]string, error)

	// ListRoleIDsByPermissionID 返回指定租户下持有某项权限的全部角色。
	//
	// 与 ListPermissionIDs 是同一个多对多关联的两个方向：
	// 前者回答"这个角色能做什么"，后者回答"哪些角色能做这件事"。
	// 缺少后者时，"撤销某项权限会波及哪些角色"就只能全表扫描，
	// 而这类判断往往发生在权限变更之前的确认环节。
	//
	// 必须带 tenant_id 条件：本关联表的 tenant_id 是冗余列、没有外键兜底，
	// 少了这一条件就会把所有租户的角色一并返回。
	//
	// 未命中返回空切片而非 nil，顺序按 role_id 升序。
	// 标识为空与标识非法时的语义同 ListPermissionIDs，不再重复。
	ListRoleIDsByPermissionID(ctx context.Context, tenantID, permissionID string) ([]string, error)
}
