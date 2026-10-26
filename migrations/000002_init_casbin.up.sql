-- Casbin 策略持久化表。
-- 表名必须为 casbin_rule（单数），与 gorm-adapter v3 的默认表保持一致。
-- 未使用的 v 列由 adapter 写入空字符串，因此统一声明为 NOT NULL DEFAULT ''，
-- 避免 NULL 让唯一索引失效。
CREATE TABLE IF NOT EXISTS casbin_rule (
    id    BIGSERIAL PRIMARY KEY,
    ptype VARCHAR(512) NOT NULL,
    v0    VARCHAR(512) NOT NULL DEFAULT '',
    v1    VARCHAR(512) NOT NULL DEFAULT '',
    v2    VARCHAR(512) NOT NULL DEFAULT '',
    v3    VARCHAR(512) NOT NULL DEFAULT '',
    v4    VARCHAR(512) NOT NULL DEFAULT '',
    v5    VARCHAR(512) NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX IF NOT EXISTS idx_casbin_rule_unique
    ON casbin_rule (ptype, v0, v1, v2, v3, v4, v5);
