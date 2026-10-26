-- 回滚权限表，索引必须显式按名删除以便精确回滚。
DROP INDEX IF EXISTS idx_permissions_code;
DROP INDEX IF EXISTS idx_permissions_resource_action;
DROP TABLE IF EXISTS permissions;
