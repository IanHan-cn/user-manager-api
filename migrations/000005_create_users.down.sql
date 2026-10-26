-- 回滚用户表，索引与触发器必须显式按名删除以便精确回滚。
DROP TRIGGER IF EXISTS trg_users_set_updated_at ON users;
-- 索引名与 000005_create_users.up.sql 保持一致。
DROP INDEX IF EXISTS idx_users_phone;
DROP INDEX IF EXISTS idx_users_email;
DROP INDEX IF EXISTS idx_users_username;
DROP INDEX IF EXISTS idx_users_zitadel_user_id;
DROP TABLE IF EXISTS users;
