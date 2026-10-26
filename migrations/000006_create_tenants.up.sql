-- 租户表，对应 tenant.Tenant。
-- 列长度与 domain 包中的约束保持一致：slug 与 plan_code 最长 64、name 最长 255。
CREATE TABLE IF NOT EXISTS tenants (
    id         UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 租户唯一标识，用于 URL 与外部引用。
    -- 唯一性只在未删除的行之间成立，见下方 idx_tenants_slug_active。
    slug       VARCHAR(64)  NOT NULL,
    name       VARCHAR(255) NOT NULL,
    -- 状态取值对应 tenant.TenantStatus 的三个常量。
    status     VARCHAR(16)  NOT NULL DEFAULT 'active',
    -- 套餐标识：空串表示暂不指定套餐，由默认套餐兜底。
    plan_code  VARCHAR(64)  NOT NULL DEFAULT '',
    -- 少量租户级可选配置，构造时保证非空，落库为 {}。
    settings   JSONB        NOT NULL DEFAULT '{}',
    -- 创建者既可能是自然人 UUID 字符串，也可能是 domain.SystemCreator（"system"），
    -- 因此不能收窄为 UUID 类型。
    created_by VARCHAR(64)  NOT NULL,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT now(),
    -- 软删除时间：未删除时为空，空值比 Go 零值时间（0001-01-01）更贴合语义。
    deleted_at TIMESTAMPTZ,

    -- slug 只允许小写字母、数字与下划线，与 tenant.keyPattern 保持一致。
    CONSTRAINT chk_tenants_slug_format CHECK (slug ~ '^[a-z][a-z0-9_]*$'),
    CONSTRAINT chk_tenants_status CHECK (
        status IN ('active', 'suspended', 'deleted')
    ),
    -- plan_code 允许小写字母、数字、下划线与连字符，与 tenant.planCodePattern 一致；
    -- 空串表示未指定套餐，单独放行。
    CONSTRAINT chk_tenants_plan_code_format CHECK (
        plan_code = '' OR plan_code ~ '^[a-z][a-z0-9_-]*$'
    ),
    -- Settings 是动态配置，必须是 JSON 对象而不是数组或标量。
    CONSTRAINT chk_tenants_settings_is_object CHECK (jsonb_typeof(settings) = 'object'),
    -- "已删除"的两个表示必须同步：status = 'deleted' 当且仅当 deleted_at 非空。
    -- 这条约束是 idx_tenants_slug 部分索引的前提：
    -- 若两者可以漂移，就会出现 status 已是 deleted、deleted_at 却仍为空的行，
    -- 这样的行既不该继续占用 slug，又会被部分索引算作未删除。
    CONSTRAINT chk_tenants_deleted_consistency CHECK (
        (status = 'deleted') = (deleted_at IS NOT NULL)
    )
);

-- slug 在"未删除"的租户间唯一，GetBySlug 依赖该约束做幂等查找。
--
-- 刻意建成部分索引而不是全表唯一：软删除的租户让出自己的 slug，
-- 之后可以用同一个 slug 重新创建租户。
-- 若建成全表唯一，删除就等于永久占用该 slug，租户改一次 slug 就再也回不去，
-- 删除后重建同名租户也会失败——这与表上存在 deleted_at 的初衷相矛盾。
--
-- 条件取 deleted_at IS NULL 而不是 status <> 'deleted'：
-- 前者是软删除的唯一权威判定（见 deleted_at 的列注释），
-- 后者依赖枚举值且被 chk_tenants_deleted_consistency 约束为与前者等价，
-- 但索引只应绑定一个判定口径。
--
-- 配套要求：GetBySlug 必须自带 deleted_at IS NULL 条件。
-- 唯一性只对未删除行成立，若查询不加该条件，
-- 就可能命中已删除的墓碑行，返回本应已被释放的租户。
CREATE UNIQUE INDEX IF NOT EXISTS idx_tenants_slug_active
    ON tenants (slug)
    WHERE deleted_at IS NULL;

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_tenants_set_updated_at
    BEFORE UPDATE ON tenants
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE tenants IS '租户，slug 在未删除的租户间唯一，软删除后 slug 可被复用';
COMMENT ON COLUMN tenants.deleted_at IS '软删除时间，非空表示已删除；该列同时决定 slug 是否仍被占用';
COMMENT ON COLUMN tenants.plan_code IS '套餐标识，空串表示未指定';
