package ports

import "context"

// PolicyPermission 是生成 Casbin p 规则所需的权限二元组。
type PolicyPermission struct {
	// Resource 是受控资源，例如 member。
	Resource string
	// Action 是资源上的操作，例如 invite。
	Action string
}

// PolicyRepository 维护 Casbin 策略在数据库中的投影（casbin_rule 表）与全局版本号。
//
// 关键约定：本接口的写入**只能**发生在业务事务内（实现必须走 dbFrom(ctx)），
// 从而保证"业务关系表 + 策略投影 + 版本号"的原子性。
// 绝不能在事务外调用 enforcer 的写入接口，否则事务回滚后策略会残留。
type PolicyRepository interface {
	// ReplaceRoleRules 用给定权限集合整体替换某角色在本租户的 p 规则。
	ReplaceRoleRules(ctx context.Context, tenantID, roleID string, perms []PolicyPermission) error
}
