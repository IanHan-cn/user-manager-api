package gorm

// 本文件放五个仓储共用的写操作结局判定。
//
// 此前 user / role / tenant / membership 各有一份逐字相同的 switch：
// ErrDuplicatedKey → 聚合自己的"已存在"错误，ErrCheckConstraintViolated →
// 聚合自己的"值不合法"错误，其余原样上抛。四份之间唯一的差异是
// 两个 sentinel 与两句定位文案，把这个差异抽成 writeTargets，
// 结构只在 classifyWriteError 里留一份。
// Update 的 RowsAffected == 0 判定同理，见 classifyUpdateResult。
//
// 后者目前服务 membership、tenant、user 三个仓储：
// 成员角色授权的 Update 额外带版本号前提，命中 0 行时要区分
// "记录不存在"与"已被他人改过"，因此走的是自己的判定。
//
// 除去重之外，顺带把"哪些错误会被翻译、各自归到哪一类"收成一处：
// 以后要认第三种结局，改这里就够了，不必挨个仓储去找。

import (
	"errors"
	"fmt"

	"gorm.io/gorm"
)

// writeTargets 描述某个聚合的写操作可以遇上的结局。
//
// conflict 与 invalid 必须是按 domain 基类包装过的错误（见 internal/domain），
// 这样上层既能按本聚合精确判定，也能按基类统一归类。
// 两个 identity 只用于文案定位，让排障时知道撞的是哪一行；
// 真正被违反的约束名只有驱动知道，因此底层错误仍会挂在错误链上。
type writeTargets struct {
	conflict         error
	conflictIdentity string
	invalid          error
	invalidIdentity  string
}

// updateTargets 在 writeTargets 之上补一个"一行都没改到"的结局。
type updateTargets struct {
	write    writeTargets
	notFound error
	identity string
}

// classifyWriteError 把数据库错误翻译成领域错误，不把驱动原生错误抛给上层。
//
// 只认 TranslateError 已经翻成 GORM 语义错误的那两类（见 bootstrap.OpenDB）：
//   - ErrDuplicatedKey（23505）：撞上唯一约束；
//   - ErrCheckConstraintViolated（23514）：值被 CHECK 约束拒绝。
//
// 其余错误（连接失败、超时等）一律原样上抛：把它们误判成"已占用"或
// "值不合法"，比让它们原样冒上去更糟——前者会让上层做出错误分支。
func classifyWriteError(err error, targets writeTargets) error {
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: %s: %w", targets.conflict, targets.conflictIdentity, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: %s: %w", targets.invalid, targets.invalidIdentity, err)
	default:
		return err
	}
}

// classifyUpdateResult 判定 Update 的执行结果：出错走同一套翻译，
// 一行都没改到返回 notFound。
//
// RowsAffected 是这里唯一的依据：UPDATE 影响 0 行意味着目标行不存在
// （Postgres 即便新旧值完全相同也照样计 1 行），此时静默返回 nil
// 会让上层误以为更新已经生效。
func classifyUpdateResult(result *gorm.DB, targets updateTargets) error {
	if result.Error != nil {
		return classifyWriteError(result.Error, targets.write)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("%w: %s", targets.notFound, targets.identity)
	}
	return nil
}
