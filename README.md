# 项目说明

这是一个遵循六边形架构的以 GIN+GORM+CASBIN+ZITADEL 为基础的用户管理系统

- 项目依赖ZITADEL为用户基础
- 业务事实的唯一真实持久化是业务表：`memberships`（成员关系）、`roles`、`role_permissions`、`permissions`
- `casbin_rule` 只是 Casbin 策略的投影，可由业务表重建；删库重建不会丢失业务事实
