-- 权限表，对应 domain.Permission。
-- 权限由系统维护、全租户共享，因此不含 tenant_id；
-- 列长度与 domain 包中的约束保持一致：resource/action 最长 64、code 最长 128。
CREATE TABLE IF NOT EXISTS permissions (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    code        VARCHAR(128) NOT NULL,
    resource    VARCHAR(64)  NOT NULL,
    action      VARCHAR(64)  NOT NULL,
    description TEXT         NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT now(),

    -- 资源与操作标识只含小写字母、数字与下划线，且以小写字母开头，
    -- 与 domain.identPattern 保持一致。
    CONSTRAINT chk_permissions_resource_format CHECK (resource ~ '^[a-z][a-z0-9_]*$'),
    CONSTRAINT chk_permissions_action_format CHECK (action ~ '^[a-z][a-z0-9_]*$'),
    -- code 由 resource 与 action 以 ':' 连接而成，与 domain.BuildCode 保持一致，
    -- 防止绕过应用层写入与 resource/action 不自洽的 code。
    CONSTRAINT chk_permissions_code_format CHECK (code ~ '^[a-z][a-z0-9_]*:[a-z][a-z0-9_]*$'),
    CONSTRAINT chk_permissions_code_consistent CHECK (code = resource || ':' || action)
);

-- code 是稳定的对外标识，全局唯一。
CREATE UNIQUE INDEX IF NOT EXISTS idx_permissions_code
    ON permissions (code);

-- resource 与 action 与 code 一一对应，同样必须唯一；
-- 该索引同时支撑按 resource 检索某个资源的全部操作。
CREATE UNIQUE INDEX IF NOT EXISTS idx_permissions_resource_action
    ON permissions (resource, action);

COMMENT ON TABLE permissions IS '系统权限，由系统维护，租户管理员只能组合不能新建';
COMMENT ON COLUMN permissions.code IS '稳定对外标识，形如 user:read';
