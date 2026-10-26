-- 回滚通用函数。
-- 这里不使用 CASCADE：若仍有表上的触发器依赖该函数，回滚会直接报错，
-- 从而提示先回滚业务表迁移，避免误删仍在使用的触发器。
DROP FUNCTION IF EXISTS set_updated_at();
