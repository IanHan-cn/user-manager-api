// Package user 定义业务用户实体与不变量。
//
// 分层约束：本包属于 Domain，不得依赖 Gin、GORM、Redis 或 Casbin。
// 用户不持有认证凭据，身份的权威来源是 ZITADEL，本地只保存业务侧映射与展示资料。
package user

import (
	"errors"
	"time"
)

type UserStatus string

const (
	UserStatusActive    UserStatus = "active"    // 用户已激活，允许登录
	UserStatusInactive  UserStatus = "inactive"  // 用户未激活，不允许登录
	UserStatusSuspended UserStatus = "suspended" // 用户被暂停，不允许登录
)

// ErrInvalidZitadelUserID 表示外部身份标识非法。
var ErrInvalidZitadelUserID = errors.New("zitadel_user_id 不能为空")

// User 业务用户实体,与 ZITADEL 用户通过 ZitadelUserID 关联。
type User struct {
	ID          string
	Username    string
	DisplayName string
	Email       string
	Phone       string
	AvatarURL   string
	// ZitadelUserID 是外部身份唯一标识，也是同步的幂等依据。
	ZitadelUserID string

	Status        UserStatus
	EmailVerified bool

	CreatedAt time.Time
	UpdatedAt time.Time
}
