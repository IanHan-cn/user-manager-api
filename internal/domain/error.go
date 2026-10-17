package domain

import "errors"

// ErrInvalidZitadelUserID 表示外部身份标识非法。
var ErrInvalidZitadelUserID = errors.New("zitadel_user_id 不能为空")
