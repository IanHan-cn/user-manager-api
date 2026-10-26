-- 数据库层通用函数，供后续业务表复用。
-- 本迁移必须最先执行（版本 1），因为后续建表迁移会依赖这里的触发器函数。

-- set_updated_at 在每行 UPDATE 前把 updated_at 刷新为当前事务时间，
-- 使 updated_at 由数据库强制维护，避免应用层漏写或写错。
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS TRIGGER AS $$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

COMMENT ON FUNCTION set_updated_at() IS
    'BEFORE UPDATE 触发器函数：自动把 updated_at 刷新为当前时间';
