package role

import (
	"fmt"
	"strings"
	"uuid"

	"github.com/memories-cn/user-management-api/internal/domain"
)

// NormalizePermissionIDs 归一化一组权限标识，产出可直接交给
// Repository.ReplacePermissions 落库的集合。
//
// 本函数是"整体替换"语义的一部分：一旦各种形态的入参都被允许进来，
// 最终写进 role_permissions 的内容就不再可信，因此收敛规则必须固化在领域层，
// 而不是散落在各个调用方里各自实现一份。
//
// 具体规则与各自的取舍：
//
//   - 去两侧空白：调用方的权限集合经常由表单或查询参数拼出，
//     残留空白既不是合法 UUID、也不该成为一次失败——它只是编码噪声；
//
//   - 跳过空串而不是报错：空串等同于"这一项没传"，而不是"传了一个非法标识"。
//     与 application 层处理权限代码的口径一致（platformsetup.normalizeCodes），
//     也避免了上层为了少一个空串就得先自己做一遍过滤；
//
//   - 非 UUID 一律返回 ErrInvalidPermissionID 并带上原始取值，
//     绝不静默丢弃：权限标识引用的是 permissions 表里已存在的行，
//     非法标识应当在进入写路径之前就失败，理由与 membership.New 拒绝
//     uuid.Nil() 一致；静默丢弃会让一次调用悄悄少写一部分权限，
//     而调用方从返回值上完全看不出来发生过丢失；
//
//   - uuid.Nil 同样按非法处理：它虽能通过格式解析，却不可能是任何一条真实权限的标识；
//
//   - 去重，并保留首次出现的顺序：数据库唯一索引会拒绝同一 (role_id, permission_id)
//     出现两次，去重是为了让"语义上相同"的入参不升级成一次写失败；
//     保留首次出现顺序则让同一份入参每次都得到同一份写入序列，便于比对与排障；
//
//   - 返回空切片而非 nil：权限被清空是合法结果，
//     调用方用 len() 判断即可，不必再判 nil。
func NormalizePermissionIDs(ids []string) ([]string, error) {
	seen := make(map[string]struct{}, len(ids))
	result := make([]string, 0, len(ids))

	for _, raw := range ids {
		id := strings.TrimSpace(raw)
		if id == "" {
			continue
		}

		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil() {
			return nil, fmt.Errorf("权限标识 %q 非法：%w", raw, domain.ErrInvalidPermissionID)
		}

		// 一律以解析后的规范形态落库，而不是原样保留入参：
		// uuid.Parse 同时接受大写与花括号写法（RFC 4122），两种写法指向同一条权限，
		// 原样保留会让去重失效——"{x}" 与 "x" 会被当成两个不同的键各留一份，
		// 尽管落到 UUID 列里最终是同一个值。统一形态也便于比对与排障。
		canonical := parsed.String()
		if _, dup := seen[canonical]; dup {
			continue
		}
		seen[canonical] = struct{}{}
		result = append(result, canonical)
	}

	return result, nil
}
