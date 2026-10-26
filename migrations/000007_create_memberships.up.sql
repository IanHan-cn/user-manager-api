-- 成员关系表，对应 domain/membership.Membership。
--
-- 一条记录只刻画"某用户在某租户中的成员资格"，因此 (tenant_id, user_id)
-- 全表唯一（见下方 idx_memberships_tenant_user）：成员离开后处于 left 状态的行
-- 必须留着复用（Reinvite），不能删除后重建。
--
-- 本表只保存成员资格本身，不保存任何认证凭据：身份的权威来源仍是 ZITADEL。
-- 列长度与 domain/membership 包中的约束保持一致：
-- 状态取值最长 9（suspended）、邀请人标识最长 64（UUID 字符串或 domain.SystemCreator）。
CREATE TABLE IF NOT EXISTS memberships (
    id           UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    tenant_id    UUID        NOT NULL,
    user_id      UUID        NOT NULL,
    -- 状态取值对应 membership.Status 的四个常量，状态机见该类型的注释。
    status       VARCHAR(16) NOT NULL DEFAULT 'invited',
    -- 邀请人既可能是自然人 UUID 字符串，也可能是 domain.SystemCreator（"system"），
    -- 因此不能收窄为 UUID 类型，与 tenants.created_by 保持同一口径。
    invited_by   VARCHAR(64) NOT NULL,
    -- 邀请时间：新建与重新邀请都会写入，因此恒非空。
    invited_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 正式加入时间：仅在 active / suspended 下有值，恢复成员身份时不覆盖首次加入时间。
    joined_at    TIMESTAMPTZ,
    -- 暂停时间：仅在 suspended 下有值，恢复时清空。
    suspended_at TIMESTAMPTZ,
    -- 离开时间：仅在 left 下有值，重新邀请时清空。
    --
    -- 三个业务时间戳统一用 NULL 表示"尚未发生"：
    -- 领域层用 time.Time 零值表达同一语义，而零值时间（0001-01-01）落到库里
    -- 既不是"空"也不是一个有意义的时间点，NULL 更贴合语义。
    left_at      TIMESTAMPTZ,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),

    CONSTRAINT chk_memberships_status CHECK (
        status IN ('invited', 'active', 'suspended', 'left')
    ),
    -- 空邀请人会让"谁邀请的"无从追溯，因此连纯空白也不放行。
    CONSTRAINT chk_memberships_invited_by_not_blank CHECK (btrim(invited_by) <> ''),

    -- 状态与业务时间戳的一致性：二者描述同一件事的两种视角，
    -- 领域层由 Activate / Suspend / Leave / Reinvite 四个方法守护，
    -- 这里再由数据库兜底，防止绕过应用层写入漂移的数据。
    --
    -- 四种状态各自的取值要求（取自状态机注释）：
    --   - invited：尚未加入、未被暂停、未离开，三个时间戳全空；
    --   - active：已加入，且不在暂停中、未离开；
    --   - suspended：已加入且处于暂停中，未离开；
    --   - left：必然有离开时间。
    --
    -- left 只约束 left_at，是有意为之：从 invited 离开时 joined_at 仍为空，
    -- 从 suspended 离开时 suspended_at 仍保留当次暂停的时间，
    -- Leave 并不清理这两个时间戳。若把 left 也要求"joined_at 非空"，
    -- 就会拒绝"邀请了但从未加入就离开"这一合法路径。
    --
    -- ELSE FALSE 是失败即关闭：状态取值已由 chk_memberships_status 收窄，
    -- 这里再兜一层，万一枚举扩大而本约束未同步，未知状态一律拒绝写入。
    CONSTRAINT chk_memberships_status_timestamps CHECK (
        CASE status
            WHEN 'invited' THEN
                joined_at IS NULL AND suspended_at IS NULL AND left_at IS NULL
            WHEN 'active' THEN
                joined_at IS NOT NULL AND suspended_at IS NULL AND left_at IS NULL
            WHEN 'suspended' THEN
                joined_at IS NOT NULL AND suspended_at IS NOT NULL AND left_at IS NULL
            WHEN 'left' THEN
                left_at IS NOT NULL
            ELSE FALSE
        END
    )
);

-- (tenant_id, user_id) 全表唯一，是成员关系的业务主键。
--
-- 刻意建成全表唯一而不是"只覆盖未离开成员"的部分索引：
-- left 不是墓碑，而是唯一可经 Reinvite 复活的状态（见 domain/membership 的状态机）。
-- 用户离开后再次加入必须复用同一行，若唯一性只对未离开的行成立，
-- 同一个用户就能被反复插入多条成员关系，GetByTenantAndUser 的"取一条"语义随之失效。
--
-- 重复插入由本约束兜底，适配层把它翻译成 ErrMembershipExists，不得静默吞掉。
CREATE UNIQUE INDEX IF NOT EXISTS idx_memberships_tenant_user
    ON memberships (tenant_id, user_id);

-- 支撑 GetByTenantID：按 created_at 升序、created_at 相同时按 id 升序返回
-- （顺序口径见 Repository 端口注释的"排序与分页"小节）。
--
-- 列顺序刻意与排序键一致（tenant_id 等值过滤 + created_at, id 排序），
-- 使该扫描直接走索引顺序，无需额外排序步骤；
-- 后续加入 limit / offset 分页时也仍然稳定。
-- 前缀列 tenant_id 已能覆盖"按租户过滤"的查询，故不再单建 tenant_id 索引。
CREATE INDEX IF NOT EXISTS idx_memberships_tenant_created_at
    ON memberships (tenant_id, created_at, id);

-- 支撑 GetByUserID：该方法是刻意保留的唯一跨租户查询，
-- 用于"该用户加入了哪些租户"这类入口，排序口径同上。
CREATE INDEX IF NOT EXISTS idx_memberships_user_created_at
    ON memberships (user_id, created_at, id);

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_memberships_set_updated_at
    BEFORE UPDATE ON memberships
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE memberships IS '租户成员关系，(tenant_id, user_id) 全表唯一，离开后复用同一行重新邀请';
COMMENT ON COLUMN memberships.invited_by IS '邀请人用户 ID，系统邀请时为 system';
COMMENT ON COLUMN memberships.joined_at IS '首次正式加入时间，恢复成员身份时不覆盖，未加入为空';
COMMENT ON COLUMN memberships.suspended_at IS '本次暂停的时间，恢复时清空';
COMMENT ON COLUMN memberships.left_at IS '离开时间，非空表示该成员已离开，仅能经重新邀请回到 invited';
