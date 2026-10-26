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
func ParseCode(code string) (resource, action string, err error) {
	code = strings.ToLower(strings.TrimSpace(code))
	if code == "" || len(code) > maxCodeLen {
		return "", "", ErrInvalidCode
	}
	resource, action, found := strings.Cut(code, CodeSeparator)
	if !found {
		return "", "", ErrInvalidCode
	}
	if !identPattern.MatchString(resource) || !identPattern.MatchString(action) {
		return "", "", ErrInvalidCode
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
