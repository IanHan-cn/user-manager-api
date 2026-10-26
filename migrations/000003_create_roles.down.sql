-- 回滚角色表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_roles_set_updated_at ON roles;
DROP INDEX IF EXISTS idx_roles_tenant_key;
DROP TABLE IF EXISTS roles;
