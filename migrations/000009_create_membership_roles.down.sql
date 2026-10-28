-- 回滚成员角色授权关联表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_membership_roles_set_updated_at ON membership_roles;
-- 索引名与 000009_create_membership_roles.up.sql 保持一致。
DROP INDEX IF EXISTS idx_membership_roles_due;
DROP INDEX IF EXISTS idx_membership_roles_tenant_role;
DROP INDEX IF EXISTS idx_membership_roles_tenant_membership;
DROP INDEX IF EXISTS idx_membership_roles_membership_role_active;
-- 表删除会连带清掉由 PRIMARY KEY 隐式创建的索引，故不再显式点名。
DROP TABLE IF EXISTS membership_roles;
