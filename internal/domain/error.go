// Package domain 保存跨聚合共享的领域定义：错误分类、公共常量与公用错误。
//
// 错误分类约定：各聚合的领域错误都必须挂在这棵树上：
//   - ErrInvalidKey：关键字段不合法（为空、格式错误、超长）；
//   - ErrInvalidState：当前状态不允许该操作（例如对终态成员关系再次激活）；
//   - ErrConflict：与已有记录冲突（例如重复创建同一条成员关系）；
//   - ErrNotFound：目标记录不存在（例如更新一条已被删除的成员关系）。
//
// 后两类只在"写操作"上出现：查询未命中按各端口的约定返回空结果，
// 只有"要写却写不了"（重复、目标不存在）才属于这两类。
//
// 另有一类刻意不挂在业务分类树上的错误：
//   - ErrNilEntity：调用方传入了空实体，属于程序 bug，不得混入业务分类。
//
// 分类的意义：上层只需对基类做 errors.Is 判定即可统一归类，
// 无需逐个枚举各聚合的具体错误，也不会因为新增聚合而漏判。
//
// 归属口径：被别的聚合引用、或引用"另一类业务对象"的词汇
// （租户标识、用户标识、操作者、套餐标识）定义在本包；
// 本聚合自己的受控词表（角色标识、权限代码）留在各自的包里，
// 且同样必须以基类包装。
package domain

import (
	"errors"
	"fmt"
)

// 跨聚合共用的字段长度上限。
//
// 名称一类字段的上限在多个聚合里是同一个数，
// 各包一律引用本常量，不要各自写一份字面量：
// 否则调整上限时要改多处，且容易与错误提示对不上。
const MaxNameLen = 255

// 错误分类基类：各聚合的领域错误都必须以其中之一为基类。
var (
	// ErrInvalidKey 是所有"关键字段不合法"错误的基类错误。
	ErrInvalidKey = errors.New("关键字段不合法")

	// ErrInvalidState 是所有"当前状态不允许该操作"错误的基类错误。
	// 与 ErrInvalidKey 的区别：参数本身不合法才叫 ErrInvalidKey；
	// 参数没问题、只是当前状态不允许，属于 ErrInvalidState。
	ErrInvalidState = errors.New("状态不允许该操作")

	// ErrConflict 是所有"与已有记录冲突"错误的基类错误。
	// 典型场景是业务键重复：入参本身合法，只是库里已经有一条同键的记录。
	ErrConflict = errors.New("与已有记录冲突")

	// ErrNotFound 是所有"目标记录不存在"错误的基类错误。
	// 只用于"要写却写不到"的场景（如更新一条已被删除的记录）；
	// 查询未命中不是错误，各端口按约定返回空结果，不走这一类。
	ErrNotFound = errors.New("目标记录不存在")
)

// 跨聚合共用的字段级错误。
//
// 只有"本聚合自己的受控词表"才留在各自的包里；
// 下面这些词汇会被（或规划被）多个聚合引用，因此统一定义在本包，
// 各聚合直接引用，不再各自另写一份。
var (
	// ErrInvalidTenantID 表示租户标识为空或非法。
	ErrInvalidTenantID = fmt.Errorf("%w: 租户标识", ErrInvalidKey)

	// ErrInvalidUserID 表示用户标识为空或非法。
	ErrInvalidUserID = fmt.Errorf("%w: 用户标识", ErrInvalidKey)

	// ErrInvalidPermissionID 表示权限标识为空或非法。
	//
	// 与 ErrInvalidUserID 同理：它是被别的聚合引用的"另一类业务对象"的标识
	// ——角色持有哪几项权限，引用的是 permissions 里的行，
	// 正像成员关系引用 users 里的行那样。因此它留在跨聚合的本包，
	// 而不是留在持有它的 role 包里，将来再有聚合要引用权限时可直接复用。
	ErrInvalidPermissionID = fmt.Errorf("%w: 权限标识", ErrInvalidKey)

	// ErrInvalidRoleID 表示角色标识为空或非法。
	// 与上面几个同属"被别的聚合引用"的词汇：角色权限关联要引用角色，
	// 将来成员关系持有角色也会引用它，因此定义在本包而不是 role 包。
	ErrInvalidRoleID = fmt.Errorf("%w: 角色标识", ErrInvalidKey)

	// ErrInvalidActor 表示操作者标识为空或非法。
	// 操作者指"由谁创建""由谁邀请"这类行为主体，
	// 例如 tenant 的 CreatedBy、membership 的 InvitedBy。
	ErrInvalidActor = fmt.Errorf("%w: 操作者标识", ErrInvalidKey)

	// ErrInvalidPlanCode 表示套餐标识格式不合法。
	// 套餐标识引用的是"套餐"这个业务对象（见 tenant.Tenant.PlanCode 的注释：
	// 未来用于订阅管理），不是 tenant 的自有属性，
	// 因此与其它跨聚合词汇一样放本包：订阅/计费聚合落地时可直接复用，
	// 不必反过来依赖 tenant 包。
	ErrInvalidPlanCode = fmt.Errorf("%w: 套餐标识", ErrInvalidKey)

	// ErrInvalidSlug 表示租户唯一标识不合法。
	ErrInvalidSlug = fmt.Errorf("%w: 租户唯一标识", ErrInvalidKey)

	// ErrInvalidName 表示名称为空或超长，上限见 MaxNameLen。
	// 提示文案与其它字段级错误保持一致，只写字段名：
	// 具体的长度上限由 MaxNameLen 统一给出，不在这里重复写死。
	ErrInvalidName = fmt.Errorf("%w: 名称", ErrInvalidKey)
)

// ErrNilEntity 是所有"调用方传入了空实体"错误的基类错误。
//
// 它刻意不挂在上面任何业务分类上：空实体是调用方 bug
// （该查没查到、查到却没判空），不是业务规则拒绝了请求。
// 若把它归到 ErrInvalidState，上层按状态类返回 409 就会把程序 bug
// 伪装成一次正常的业务冲突，从而掩盖真正的缺陷。
//
// 因此各聚合在以它为基类包装自己的错误后，HTTP 层应映射为 500 而不是 4xx。
var ErrNilEntity = errors.New("实体为空")

// ErrAlreadyExists 表示同类记录已存在，通常由数据库唯一约束兜底发现。
//
// 它描述的是"写操作的结局"而不是某个字段，因此不属于上面的字段级错误。
// 适配层必须把唯一约束冲突翻译成以它为基类的错误：
// 不得直接抛出驱动原生错误，也不得静默吞掉后返回 nil。
// 各聚合在它外面再包一层自己的名字（例如 membership.ErrMembershipExists），
// 调用方既能按聚合精确判定，也能按基类统一归类。
// ErrNotFound 同理，各聚合直接以它为基类包装即可。
var ErrAlreadyExists = fmt.Errorf("%w: 记录已存在", ErrConflict)
