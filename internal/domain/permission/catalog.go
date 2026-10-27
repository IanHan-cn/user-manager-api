package permission

import (
	"fmt"
	"strings"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// Entry 描述权限目录里的一项，是"系统要维护哪些权限"的权威定义。
//
// 与 Permission 的分工：Permission 是 permissions 表里的一行（有 ID、有落库时间），
// Entry 则是代码里的定义本身，不含任何存储属性。
// 二者通过 Code 对应——这里刻意不复用 Permission，是为了让目录
// 能在没有任何数据库往返的情况下被校验、被遍历、被单测，
// Seeder 拿着它就足以决定要写什么。
type Entry struct {
	// Resource 是受控资源，形如 member。
	Resource string
	// Action 是资源上的操作，形如 invite。
	Action string
	// Code 是稳定的对外标识，恒等于 BuildCode(Resource, Action)。
	Code string
	// Description 是展示用的中文说明，会随目录同步进库。
	Description string
}

// ErrUnknownCode 表示权限代码格式合法，但不在系统权限目录内。
//
// 与 ErrInvalidCode 分开：前者是格式错了（少个冒号、混进大写），
// 后者是格式没问题、只是这个词不在受控词表里。
// 排障方向完全不同——格式错要去查调用的构造过程，
// 不在目录里则要去看是不是目录该扩充、还是调用方写了自己想象出来的权限。
var ErrUnknownCode = fmt.Errorf("%w: 权限代码不在系统目录中", domain.ErrInvalidKey)

// newEntry 构造目录项，Code 恒由 resource 与 action 拼出。
//
// 不把 Code 作为入参是为了消除写错的可能：permissions 表上有
// chk_permissions_code_consistent（code = resource || ':' || action），
// 手写 code 一旦与实际列值不一致，写入会被 CHECK 约束拒绝，
// 而错误要等到 SQL 执行时才暴露。由构造函数拼就不需要这个担心。
func newEntry(resource, action, description string) Entry {
	return Entry{
		Resource:    resource,
		Action:      action,
		Code:        BuildCode(resource, action),
		Description: description,
	}
}

// catalog 是系统权限目录的全量定义。
//
// 为什么写死在代码里而不是放进迁移：它是一份受控词表，
// 角色预置、守卫校验都要引用它；散在 SQL 里就只能靠运行时查库比对，
// 而删除一条目录项会牵连已被引用的历史数据，本就不是一个可以自动化的变更。
// 表结构由迁移管理（见 migrations/000004），这里只管"有哪些权限"。
//
// 变更目录的方式是改这里再跑 Seeder：Seeder 幂等，重复执行不会产生重复数据，
// 也不会删除库里已经存在但不在目录里的行（它们可能被既有的角色引用着）。
//
// 它刻意保持私有：目录是"系统要维护哪些权限"的权威定义，
// 一旦导出，外部包就能在运行时改写词表，而这种破坏不会立刻暴露，
// 只会在 Seeder 写库时被 CHECK 约束拒绝，错误现场落在 SQL 里、很难反查源头。
// 对外的读取入口只有 All（全量副本）与 Lookup（单项查询）两个。
var catalog = []Entry{
	// 租户自身
	newEntry("tenant", "read", "查看租户信息"),
	newEntry("tenant", "update", "修改租户信息"),
	newEntry("tenant", "delete", "删除租户"),

	// 租户成员
	newEntry("member", "read", "查看租户成员"),
	newEntry("member", "invite", "邀请成员加入租户"),
	newEntry("member", "update", "修改租户成员"),
	newEntry("member", "remove", "移除租户成员"),

	// 租户角色
	newEntry("role", "read", "查看角色"),
	newEntry("role", "create", "创建角色"),
	newEntry("role", "update", "修改角色"),
	newEntry("role", "delete", "删除角色"),
	newEntry("role", "assign", "为成员分配角色"),

	// 权限自身
	newEntry("permission", "read", "查看权限"),
	newEntry("permission", "assign", "为角色分配权限"),

	// 审计
	newEntry("audit", "read", "查看审计日志"),
}

// All 返回目录全量的副本。
//
// 返回副本而不是直接给出切片：catalog 是包级变量，
// 调用方一次 append 就会改写后续所有人看到的目录。
// Entry 的字段全是字符串，浅拷贝之后彼此不再共享任何可变状态。
func All() []Entry {
	out := make([]Entry, len(catalog))
	copy(out, catalog)
	return out
}

// Lookup 按权限代码查询目录项，未命中返回零值与 false。
//
// 线性查找而不是预先建一份 index：目录目前只有十余项，
// 且只有在 Seeder 与校验路径上被调用，为它维护一份与 catalog
// 必须同步更新的派生状态反而是负担。
func Lookup(code string) (Entry, bool) {
	normalized := strings.ToLower(strings.TrimSpace(code))
	for _, e := range catalog {
		if e.Code == normalized {
			return e, true
		}
	}
	return Entry{}, false
}

// ValidateCatalog 校验一组目录项自身是否成立，返回首个非法项。
//
// 检查三件事：
//   - 每项的 resource 与 action 各自合法（非空、不超长、字符集合法），
//     边界与 permissions 表的单列长度一致（见 migrations/000004）；
//   - 每项的代码能通过 ParseCode，且 Code 与 Resource/Action 自洽；
//   - 目录内没有重复代码。
//
// 第一条看似由 newEntry 与 ParseCode 分别保证，但 Entry 是导出结构体，
// 调用方完全可以手工构造——一旦有人那么写，不一致会在写入时才炸。
// 重复项同样在这里拒绝：两条相同代码会让 Seeder 的幂等判断失去依据。
func ValidateCatalog(entries []Entry) error {
	seen := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		if err := ValidateResource(e.Resource); err != nil {
			return err
		}
		if err := ValidateAction(e.Action); err != nil {
			return err
		}
		if e.Code != BuildCode(e.Resource, e.Action) {
			return fmt.Errorf("权限代码 %q 与 resource/action 不自洽：%w", e.Code, ErrInvalidCode)
		}
		resource, action, err := ParseCode(e.Code)
		if err != nil {
			return err
		}
		if resource != e.Resource || action != e.Action {
			return fmt.Errorf("权限代码 %q 非法：%w", e.Code, ErrInvalidCode)
		}
		if _, dup := seen[e.Code]; dup {
			return fmt.Errorf("权限代码 %q 在目录中重复：%w", e.Code, ErrInvalidCode)
		}
		seen[e.Code] = struct{}{}
	}
	return nil
}
