// Package permissionseed 把代码里定义的权限目录幂等同步进数据库。
//
// 职责边界：目录"有哪些权限"定义在 domain/permission（见 permission.All），
// 表结构由 migrations 管理，本包只负责把前两者对上。
// 它刻意不与迁移混在一起——迁移是不可逆的版本序列，
// 每次部署重跑一次的数据同步放进去会让版本号的含义变得模糊。
package permissionseed

import (
	"context"
	"errors"

	"github.com/memories-cn/user-management-api/internal/domain/permission"
)

// seeder 执行权限目录的同步。
//
// 只依赖 permission.Writer 端口，不认识 GORM，也不知道 SQL 长什么样：
// 换一种持久化实现时本包不需要改动。
type seeder struct {
	writer permission.Writer
}

// NewSeeder 创建权限目录同步器。
func NewSeeder(w permission.Writer) (*seeder, error) {
	if w == nil {
		return nil, errors.New("权限目录写入端口不能为空")
	}
	return &seeder{writer: w}, nil
}

// EnsureCatalog 同步全量权限目录。
//
// 幂等：重复执行既不报错也不产生重复数据，第二次起 Inserted 与 Updated 恒为 0，
// 因此可以在每次部署后放心地跑，不必记录"这次部署要不要同步"。
//
// 刻意不包事务：单次写入的幂等由 UPSERT 依赖 code 唯一约束保证，
// 而不是靠"先查再插"——读已提交隔离级别下，包了事务也挡不住并发行插入。
// 把一次 upsert 拆成事务里的两条语句只会多一次往返。
//
// 不删除：库里存在但不在目录里的权限会被原样留下，原因见 permission.Writer 的注释。
func (s *seeder) EnsureCatalog(ctx context.Context) (permission.SeedResult, error) {
	return s.writer.EnsureCatalog(ctx, permission.All())
}
