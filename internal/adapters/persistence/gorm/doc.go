// Package gorm 用 GORM 实现各聚合的持久化端口。
//
// 分层约束：本包只负责把领域实体翻译成数据库行；业务规则一律由领域层守护。
// 适配层里也有少量判定逻辑，它们都是端口契约的落地，
// 最显眼的一处是成员角色授权的到期推进（advanceExpiredDueGrants）：
// 部分唯一索引的条件无法引用 now()，这一步只能在应用层完成，
// 判定口径与领域层的 Expire 对齐。
//
// 包内组织：
//   - 每个聚合一个仓储文件，文件里只放该聚合的持久化模型与转换；
//     权限聚合另有 permission_writer.go 负责目录的批量写入；
//   - 跨仓储共用的小工具按职责分三处收口：
//     convert.go（NULL 互转与通用小工具）、errors.go（写操作的结局翻译）、
//     tx.go（事务边界实现与句柄传递）。
//
// 依赖：gorm.io/gorm、github.com/google/uuid、internal/ports（事务边界）
// 与 internal/domain（基类错误及各聚合包）。
package gorm
