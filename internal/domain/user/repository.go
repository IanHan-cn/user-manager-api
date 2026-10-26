// internal/domain/user/repository.go
package user

import (
	"context"
)

// Repository 是用户聚合的持久化端口，由适配层实现。
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
// # 幂等同步
//
// 本表的每一行都是 ZITADEL 用户的业务侧映射，同步必须幂等：
// 先按 zitadel_user_id 查询，命中则 Update，未命中才 Create。
// 这是 zitadel_user_id 建有全表唯一索引的原因——它是同步的唯一依据，
// 重复创建会撞上约束并返回 ErrUserExists，而不是静默覆盖已有用户。
//
// # 归一化的责任划分
//
// 邮箱、手机号、用户名由领域层归一化后写入（见 NormalizeEmail 等），
// 因此库里存的始终是归一化值，索引也建在这一列上。
// 查询侧传入口参前必须完成同样的归一化，否则大小写不一致会打不中索引。
type Repository interface {
	// FindByID 按主键查询用户，未命中返回 (nil, nil)。
	FindByID(ctx context.Context, id string) (*User, error)

	// FindByZitadelUserID 按外部身份标识查询用户，同步流程的主入口，
	// 未命中返回 (nil, nil)。
	FindByZitadelUserID(ctx context.Context, zitadelUserID string) (*User, error)

	// FindByEmail 按邮箱查询用户，未命中返回 (nil, nil)。
	// 入参应为 NormalizeEmail 的输出：库里存的是归一化小写值。
	FindByEmail(ctx context.Context, email string) (*User, error)

	// Create 写入用户。
	// 主键、状态与时间戳由领域层生成，实现方原样落库，不得改写；
	// 命中 zitadel_user_id、username 或 email 唯一约束时返回 ErrUserExists，
	// 不得静默吞掉后返回 nil——那会让上层误以为同步已经完成。
	Create(ctx context.Context, u *User) error

	// Update 全量更新用户的业务字段，id、created_at 与 updated_at 除外。
	// 记录不存在时返回 ErrUserNotFound：更新不存在的行属于调用方 bug，
	// 静默成功会让上层误以为同步生效。
	Update(ctx context.Context, u *User) error
}
