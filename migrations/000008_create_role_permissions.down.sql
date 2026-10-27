-- 回滚角色权限关联表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_role_permissions_set_updated_at ON role_permissions;
-- 索引名与 000008_create_role_permissions.up.sql 保持一致。
DROP INDEX IF EXISTS idx_role_permissions_permission_role;
DROP INDEX IF EXISTS idx_role_permissions_tenant_role;
DROP INDEX IF EXISTS idx_role_permissions_role_permission;
-- 表删除会连带清掉由 PRIMARY KEY 隐式创建的索引，故不再显式点名。
DROP TABLE IF EXISTS role_permissions;
