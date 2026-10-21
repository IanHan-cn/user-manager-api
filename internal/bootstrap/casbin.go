package bootstrap

import (
	"embed"
	"fmt"

	"github.com/casbin/casbin/v3"
	"github.com/casbin/casbin/v3/model"
	gormadapter "github.com/casbin/gorm-adapter/v3"
	"gorm.io/gorm"
)

//go:embed rbac_with_domains_model.conf
var configFS embed.FS

func InitializeCasbin(db *gorm.DB) (*casbin.Enforcer, error) {
	// 创建 Casbin GORM adapter，使用 PostgreSQL 作为策略存储。
	adapter, err := gormadapter.NewAdapterByDB(db)
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin GORM adapter 失败：%w", err)
	}

	// 加载嵌入式 Casbin 模型配置文件。
	modelContent, err := configFS.ReadFile("rbac_with_domains_model.conf")
	if err != nil {
		return nil, fmt.Errorf("读取嵌入式模型配置失败：%w", err)
	}
	model, err := model.NewModelFromString(string(modelContent))
	if err != nil {
		return nil, fmt.Errorf("创建Casbin模型失败：%w", err)
	}

	// 创建 Casbin enforcer 并加载策略。
	enforcer, err := casbin.NewEnforcer(model, adapter)
	if err != nil {
		return nil, fmt.Errorf("创建 Casbin enforcer 失败：%w", err)
	}
	err = enforcer.LoadPolicy()
	if err != nil {
		return nil, fmt.Errorf("加载策略失败：%w", err)
	}
	return enforcer, nil
}
