-- 回滚成员关系表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_memberships_set_updated_at ON memberships;
-- 索引名与 000007_create_memberships.up.sql 保持一致。
DROP INDEX IF EXISTS idx_memberships_user_created_at;
DROP INDEX IF EXISTS idx_memberships_tenant_created_at;
DROP INDEX IF EXISTS idx_memberships_tenant_user;
DROP TABLE IF EXISTS memberships;
