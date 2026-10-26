-- 回滚租户表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_tenants_set_updated_at ON tenants;
-- 索引名与 000006_create_tenants.up.sql 保持一致：
-- slug 索引是只覆盖未删除行的部分索引，因此后缀为 _active。
DROP INDEX IF EXISTS idx_tenants_slug_active;
DROP TABLE IF EXISTS tenants;
