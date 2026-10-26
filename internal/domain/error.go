package domain

import "errors"

var (
	// ErrInvalidZitadelUserID 表示外部身份标识非法。
	ErrInvalidZitadelUserID = errors.New("zitadel_user_id 不能为空")

	// ErrInvalidCode 表示权限代码格式不合法。
	ErrInvalidCode = errors.New("权限代码格式不合法")

	// ErrInvalidRoleCode 表示角色代码格式不合法。
	ErrInvalidRoleCode = errors.New("角色代码格式不合法")
)
