package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/casbin/casbin/v3"
	"github.com/memories-cn/user-management-api/internal/adapters/inbound/http/gin/handlers"
	"gorm.io/gorm"
)

var _ handlers.ReadinessProbe = (*ReadinessChecker)(nil)

// readinessTimeout 限制单次就绪检查的耗时，避免探针被慢依赖拖住。
const readinessTimeout = 3 * time.Second

// ReadinessChecker 校验服务是否具备处理请求的条件。
// 单独成类型是为了解开 Bootstrap 的自引用（探针不再依赖尚未构造完的 app），
// 也便于在单元测试中直接构造并检查失败路径。
type ReadinessChecker struct {
	db     *gorm.DB
	casbin *casbin.Enforcer
}

var _ handlers.ReadinessProbe = (*ReadinessChecker)(nil)

// NewReadinessChecker 使用已装配的基础设施构造就绪检查器。
func NewReadinessChecker(db *gorm.DB, enforcer *casbin.Enforcer) (*ReadinessChecker, error) {
	if db == nil {
		return nil, errors.New("database is required")
	}
	if enforcer == nil {
		return nil, errors.New("casbin enforcer is required")
	}
	return &ReadinessChecker{db: db, casbin: enforcer}, nil
}

// Check 依次校验数据库连通性与策略可用性，任一失败即视为未就绪（fail closed）。
func (r *ReadinessChecker) Check(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, readinessTimeout)
	defer cancel()

	sqlDB, err := r.db.DB()
	if err != nil {
		return fmt.Errorf("获取底层 sql.DB 失败：%w", err)
	}
	if err := sqlDB.PingContext(ctx); err != nil {
		return fmt.Errorf("数据库连通性检查失败：%w", err)
	}

	// 策略已在启动阶段由 NewCasbin 加载，这里再做一次轻量读取确认其可用。
	if _, err := r.casbin.GetPolicy(); err != nil {
		return fmt.Errorf("casbin 策略不可用：%w", err)
	}

	// TODO(migrations): 待引入迁移运行器后，在此校验已执行版本与 migrations/ 中最新版本一致。
	return nil
}
