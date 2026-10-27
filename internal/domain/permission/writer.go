package permission

import (
	"context"
)

// SeedResult 汇报一次目录同步的结果，用于让调用方判断"这次到底改了什么"。
//
// 三个字段互斥且覆盖全部目录项：
// Inserted + Updated + Unchanged == len(entries)。
// 之所以要区分 Updated 与 Unchanged，是因为"什么都没做"必须能被认出来：
// Seeder 会在每次部署时重复执行，只有把它跑成一个可以判定为 no-op 的操作，
// 重复执行才是安全的。
type SeedResult struct {
	// Inserted 是本次新增的权限数：目录里有、库里没有。
	Inserted int
	// Updated 是本次改写了文案的权限数：库里已有该 code，但描述不同。
	Updated int
	// Unchanged 是未发生变动的权限数：库里已有且文案一致，不需要写。
	Unchanged int
}

// Total 返回本次同步覆盖的目录项总数。
func (r SeedResult) Total() int {
	 return r.Inserted + r.Updated + r.Unchanged 
	}

// Changed 报告本次同步是否真的写了任何东西，供调用方决定是否要记日志或触发后续动作。
func (r SeedResult) Changed() bool {
	 return r.Inserted+r.Updated > 0 
	}

// Writer 提供权限目录的写入能力，只允许由系统初始化使用。
//
// 与 Repository 刻意分成两个接口：Repository 面向租户侧的日常用例，只读；
// 权限的增删属于系统维护，租户管理员只能在既有集合内组合角色，
// 不能自己造出新的可执行权限。若把写能力并进 Repository，
// 这条边界就只剩注释约束，任何人拿到 Repository 都能写。
//
// 另一个原因是 caller 极少：全仓只有 Seed 这条路径调用它，
// 单独成端口可以让"谁会改权限"在代码里显式可见。
type Writer interface {
	// EnsureCatalog 把给定目录幂等写入库。
	//
	// 幂等的定义：重复调用既不报错也不产生重复数据，
	// 且第二次调用必须得到 Inserted=0、Updated=0 的结果。
	// 实现方必须依赖 code 的唯一约束来消解并发下的重复插入，
	// 不能只靠"先查再插"——两次执行之间没有锁，
	// 单纯的先行查询挡不住并发竞争。
	//
	// 入参必须是 ValidateCatalog 通过的集合，实现方在写库前会再校验一次；
	// 目录项非法时返回 ErrInvalidCode，不得把 CHECK 约束的原生错误抛给上层。
	//
	// 只做写入、不做清理：库里存在但不在本次目录里的权限一概保留。
	// 它们可能正被既有角色引用着，删除会牵连历史数据，只能由人工决定。
	EnsureCatalog(ctx context.Context, entries []Entry) (SeedResult, error)
}
