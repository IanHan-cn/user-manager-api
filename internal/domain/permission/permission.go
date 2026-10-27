// Package permission 定义系统权限目录实体与权限代码的解析规则。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
// 权限由系统维护，不允许租户管理员任意创建新的可执行权限代码。
// 租户管理员可以在已有权限集合内组合自定义角色。
package permission

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/memories-cn/user-management-api/internal/domain"
)

const (
	maxResourceLen = 64
	maxActionLen   = 64
	maxCodeLen     = 128
)

// ErrInvalidCode 表示权限代码格式不合法。
var ErrInvalidCode = fmt.Errorf("%w: 权限对外标识", domain.ErrInvalidKey)

// 标识格式约束：只允许小写字母、数字与下划线，且以小写字母开头。
//
// identPattern 限定资源与操作标识的字符集。
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// ValidateResource 校验资源标识是否合法：非空、长度不超限、字符集合法。
//
// 单列校验而不仅是"拼成 code 再查一次"的原因：
// permissions 表对 resource 与 action 各有一列 VARCHAR(64)（见 migrations/000004），
// 而 code 的上限是 VARCHAR(128)——只校验 code 总长会放过
// "resource 70 字符 + action 短"这种 code 总长合格、单列却超限的取值，
// 写库时才会被列长度拒绝。
//
// 它是目录校验与写库前校验共用的同一把尺子，
// 保证"目录里的项"与"能写进库的项"永远是同一个范围。
func ValidateResource(resource string) error {
	if resource == "" || len(resource) > maxResourceLen || !identPattern.MatchString(resource) {
		return fmt.Errorf("%w: 资源标识", ErrInvalidCode)
	}
	return nil
}

// ValidateAction 校验操作标识是否合法：非空、长度不超限、字符集合法。
//
// 与 ValidateResource 同理，边界取自 permissions 表的 action 列（VARCHAR(64)）。
func ValidateAction(action string) error {
	if action == "" || len(action) > maxActionLen || !identPattern.MatchString(action) {
		return fmt.Errorf("%w: 操作标识", ErrInvalidCode)
	}
	return nil
}

type Permission struct {
	ID string
	// Code 是稳定的对外标识，形如 user:read
	Code string
	// Resource 是受控资源，形如 user
	Resource string
	// Action 是资源上的操作，形如 read
	Action string
	// Description 是展示用的中文说明
	Description string

	CreatedAt time.Time
}

// CodeSeparator 分隔资源与操作，构成权限代码。
const CodeSeparator = ":"

// BuildCode 由资源与操作构造权限代码。
func BuildCode(resource, action string) string {
	return resource + CodeSeparator + action
}

// ParseCode 解析权限代码，非法格式返回 ErrInvalidCode。
//
// 校验分三层：code 总长、resource/action 各自的单列长度与字符集。
// 单列长度之所以要单独校验，是因为 permissions 表给 resource 与 action
// 各留了 VARCHAR(64)，而 code 自身的上限是 VARCHAR(128)——
// 只看总长会放过"某一列超限"的取值，写库时才被列长度拒绝。
func ParseCode(code string) (resource, action string, err error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > maxCodeLen {
		return "", "", ErrInvalidCode
	}
	resource, action, found := strings.Cut(code, CodeSeparator)
	if !found {
		return "", "", ErrInvalidCode
	}
	if err := ValidateResource(resource); err != nil {
		return "", "", err
	}
	if err := ValidateAction(action); err != nil {
		return "", "", err
	}
	return resource, action, nil
}

// ValidateCodes 校验一组权限代码是否全部合法，返回首个非法代码。
func ValidateCodes(codes []string) error {
	for _, code := range codes {
		if _, _, err := ParseCode(code); err != nil {
			return fmt.Errorf("权限代码 %q 非法：%w", code, ErrInvalidCode)
		}
	}
	return nil
}
