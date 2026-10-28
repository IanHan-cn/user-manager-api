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
// 结局判定有两条路径：
//   - 先按被违反的约束名分流（见 writeTargets.byConstraint）：最精确，
//     因为同一类数据库错误可能来自不同约束，领域语义并不相同；
//   - 再按错误类别分流：约束名取不到时的兜底，
//     此时只能区分"唯一冲突"与"CHECK 违反"两类。
//
// 注意：本文件产出的错误链里含驱动原生错误（*pgconn.PgError），
// 它带有表名、约束名与 Detail 里的实际键值。
// 该错误链只可用于 errors.Is / errors.As 判定与内部日志，
// 禁止整体序列化或 JSON 编码后出仓库边界。
//
// 除去重之外，顺带把"哪些错误会被翻译、各自归到哪一类"收成一处：
// 以后要认第三种结局，改这里就够了，不必挨个仓储去找。

import (
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5/pgconn"
	"gorm.io/gorm"
)

// constraintOutcome 是某个约束被违反时的结局：领域错误与定位文案。
type constraintOutcome struct {
	err      error
	identity string
}

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
	// byConstraint 按被违反的约束名给出更精确的结局。
	//
	// 键是数据库里的约束名（唯一索引与 CHECK 约束），值是对应的领域错误与定位文案。
	// 同一类数据库错误可能来自不同约束，领域语义并不相同：
	// 撞主键是写入本身的缺陷，撞业务唯一索引是"已存在"，
	// 违反字段级 CHECK 是"取值不合法"。
	//
	// 不按约束名分流时，它们一律落进同一类错误：主键冲突被报成
	// "该成员已持有该角色"，字段取值非法又被报成 409 而不是 400——
	// 同一个错误在两条路径上给出不同的 HTTP 状态码，排障方向也被带偏。
	//
	// 表里没有登记的约束走按错误类别的默认分流，因此新增约束时
	// 应当一并登记（登记的名字必须与迁移脚本里的字面量一致）。
	byConstraint map[string]constraintOutcome
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
	// 先按约束名分流：能取到具体约束名时，领域语义最精确。
	//
	// 这一层必须在按类别分流之前：唯一冲突（23505）与 CHECK 违反（23514）
	// 各自都可能是好几条约束触发的，只有约束名能区分它们的领域含义。
	if name := violatedConstraint(err); name != "" {
		if outcome, ok := targets.byConstraint[name]; ok {
			// 文案只放领域语义与定位信息，不放约束名：
			// 领域错误的文案可能被直接用作 HTTP 响应体，
			// 约束名属于内部结构，不该出现在对外文案里。
			// 它仍在错误链上（驱动错误里带着），日志与排障取得到。
			return fmt.Errorf("%w: %s: %w", outcome.err, outcome.identity, err)
		}
	}
	switch {
	case errors.Is(err, gorm.ErrDuplicatedKey):
		return fmt.Errorf("%w: %s: %w", targets.conflict, targets.conflictIdentity, err)
	case errors.Is(err, gorm.ErrCheckConstraintViolated):
		return fmt.Errorf("%w: %s: %w", targets.invalid, targets.invalidIdentity, err)
	default:
		return err
	}
}

// violatedConstraint 返回被违反的约束名；取不到时返回空串。
//
// 取法必须用 errors.As 而不是类型断言：GORM 的错误翻译把驱动错误包成
// fmt.Errorf("%w: %w", gorm.ErrDuplicatedKey, pgErr)，
// 驱动错误在错误链的第二个位置上，直接断言会落空。
//
// 取不到的情况是有的：某些驱动路径只保留错误码与文案，
// 不带约束名。此时按类别分流仍然可用，只是结局粗一些。
func violatedConstraint(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.ConstraintName
	}
	return ""
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
