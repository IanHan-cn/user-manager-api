-- 回滚 Casbin 策略表，索引必须显式按名删除以便精确回滚。
DROP INDEX IF EXISTS idx_casbin_rule_unique;
DROP TABLE IF EXISTS casbin_rule;
