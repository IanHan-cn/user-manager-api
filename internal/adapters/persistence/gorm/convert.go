package gorm

// 本文件放同包内多个仓储共用的小工具。
//
// 它们此前各自寄生在一个具体的仓储文件里，却被另外一到三个仓储跨文件引用：
// toAnys 寄生在 role_repository.go、toNullableTime/fromNullableTime 寄生在
// membership_repository.go、isRecordNotFound 寄生在 permission_repository.go。
// 编译一直合法，代价是"这个 helper 在哪"没有确定答案，新增第 N 个仓储时
// 又得重新挑一个宿主文件。收口到这里，让答案只有一个。

import (
	"errors"
	"time"

	"gorm.io/gorm"
)

// isRecordNotFound 报告错误是否为"未命中"。
//
// 读方法都要判它，单独成函数是为了让 gorm.ErrRecordNotFound 的引用只有一处：
// 未命中不是错误，漏判一次就会把一次正常的空结果变成 500。
// 这个承诺约束的是"不要绕过它直接写 errors.Is"——绕过一次保证就失效。
func isRecordNotFound(err error) bool {
	return errors.Is(err, gorm.ErrRecordNotFound)
}

// toAnys 把字符串参数转成 GORM 占位符所需的 []any。
func toAnys(args []string) []any {
	out := make([]any, len(args))
	for i, a := range args {
		out[i] = a
	}
	return out
}

// toNullableTime 把领域层的零值时间转成库里的 NULL。
func toNullableTime(t time.Time) *time.Time {
	if t.IsZero() {
		return nil
	}
	value := t
	return &value
}

// fromNullableTime 把库里的 NULL 还原成领域层的零值时间。
func fromNullableTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// toNullableString 把领域层的空串转成库里的 NULL。
//
// 与时间那一对同一口径：可空文本列以 NULL 表示"没填"，
// 空串写进去会撞上 btrim(col) <> ” 一类的 CHECK
// （见 membership_roles.reason 与 chk_membership_roles_reason）。
func toNullableString(s string) *string {
	if s == "" {
		return nil
	}
	value := s
	return &value
}

// fromNullableString 把库里的 NULL 还原成领域层的空串。
func fromNullableString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
