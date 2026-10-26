-- 角色表，对应 domain.Role。
-- 列长度与 domain 包中的约束保持一致：key 最长 64、name 最长 255。
CREATE TABLE IF NOT EXISTS roles (
    id          UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id   UUID        NOT NULL,
    key         VARCHAR(64)  NOT NULL,
    name        VARCHAR(255) NOT NULL,
    is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    description TEXT         NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- 角色标识只含小写字母、数字与下划线，且以小写字母开头，
    -- 与 domain.keyPattern 保持一致，防止绕过应用层写入脏数据。
    CONSTRAINT chk_roles_key_format CHECK (key ~ '^[a-z][a-z0-9_]*$')
);

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_roles_set_updated_at
    BEFORE UPDATE ON roles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

CREATE UNIQUE INDEX IF NOT EXISTS idx_roles_tenant_key
    ON roles (tenant_id, key);

COMMENT ON TABLE roles IS '租户角色，key 在租户内唯一';
COMMENT ON COLUMN roles.is_system IS '系统预置角色，不允许删除或修改 key';
