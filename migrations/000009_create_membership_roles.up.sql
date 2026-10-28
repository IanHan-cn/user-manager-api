-- migrations/000009_create_membership_roles.up.sql
-- 成员角色授权关联表，对应 domain/membershiprole.MembershipRole。
--
-- 一条记录刻画"某成员关系在某租户中被授予某个角色"这一次授权：
-- 与 role_permissions（纯关联、无生命周期）不同，本表带自己的生命周期
-- ——谁授的、何时生效、何时失效、因何变更，因此配一个独立的领域包。
--
-- 生命周期落在四个业务时间戳与一个状态列上：
--   - assigned_at：授权动作发生的时间，恒非空；
--   - effective_at：授权开始生效的时间，恒非空；
--   - expires_at：失效时间，NULL 表示长期有效；
--   - status：pending（待审批）/ active / revoked / expired。
--
-- 刻意不加外键：全仓各表一律不使用 REFERENCES（memberships、role_permissions
-- 都没加），引用完整性由应用层写路径兜底。写明原因是为了让后来者不要再把它"补"上去。
-- 应用层需要保证的三件事：
--   - 成员关系确实属于本租户（tenant_id 与 memberships.tenant_id 一致）；
--   - 角色确实属于本租户（tenant_id 与 roles.tenant_id 一致）；
--   - 分配人是本系统已存在的用户（或 domain.SystemCreator）。
-- 不加外键的实际代价与 role_permissions 同：删除 membership 或 role 不会被引擎阻止，
-- 可能留下孤儿行，因此任何删除路径都必须先经本表清理，并与 Casbin 策略投影同事务提交。
CREATE TABLE IF NOT EXISTS membership_roles (
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 租户归属，冗余自 memberships.tenant_id：
    -- 端口的每个方法都带 tenant_id 条件，冗余后该条件可以直接下推到索引
    -- （见下方三个以 tenant_id 打头的索引），写路径不必再 join memberships 校验归属，
    -- 跨租户写入会被索引条件本身挡住。
    --
    -- 没有外键兜底（见上方"刻意不加外键"小节），
    -- 本列与 memberships.tenant_id 的一致性由"所有写入都必须经过
    -- membershiprole.Repository"保证。
    tenant_id     UUID        NOT NULL,
    membership_id UUID        NOT NULL,
    role_id       UUID        NOT NULL,
    -- 分配人既可能是自然人 UUID 字符串，也可能是 domain.SystemCreator（"system"）
    -- ——系统初始化给 owner 分配角色时没有自然人操作者，
    -- 因此不能收窄为 UUID 类型，与 memberships.invited_by 保持同一口径。
    assigned_by   VARCHAR(64) NOT NULL,
    assigned_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 生效时间。构造函数一律取授权当时作为占位（该列 NOT NULL），
    -- pending 授权在审批通过时改写为审批时刻，因此它真正表达的是"开始生效的时刻"。
    -- 刻意不提供预约未来生效的入口："尚未生效"一律由 pending 表达，
    -- 若再允许 active + effective_at 在将来，
    -- 鉴权侧就要同时判定状态与时间两个维度，口径容易漂移。
    effective_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 失效时间，NULL 表示长期有效。与 memberships 的三个业务时间戳同一口径：
    -- 领域层用 time.Time 零值表达"不失效"，库里用 NULL，
    -- 零值时间（0001-01-01）落到列里既不是"空"也不是一个有意义的时间点。
    expires_at    TIMESTAMPTZ,
    -- 状态取值对应 membershiprole.Status 的四个常量，状态机见该类型的注释。
    status        VARCHAR(16) NOT NULL DEFAULT 'active',
    -- 变更原因：分配理由、撤销理由都落在这一列，由最近一次变更覆写。
    reason        TEXT,

    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    version INT NOT NULL DEFAULT 1,

    CONSTRAINT chk_membership_roles_status CHECK (
        status IN ('pending', 'active', 'revoked', 'expired')
    ),
    -- 空分配人会让"谁授的权"无从追溯，因此连纯空白也不放行。
    CONSTRAINT chk_membership_roles_assigned_by_not_blank CHECK (btrim(assigned_by) <> ''),
    -- 失效时间必须晚于生效时间：等于或早于意味着这条授权从未成立过。
    CONSTRAINT chk_membership_roles_expires_after_effective CHECK (
        expires_at IS NULL OR expires_at > effective_at
    ),
    -- 状态与失效时间的一致性：expired 只能由"到了 expires_at"推进
    -- （见 MembershipRole.Expire），没有到期时间的授权不该被写成 expired。
    -- 少了这一条，一次误调用的定时任务就能把长期有效的授权批量判死。
    CONSTRAINT chk_membership_roles_expired_has_expires_at CHECK (
        status <> 'expired' OR expires_at IS NOT NULL
    ),
    -- reason 是自由文本，但仍拒绝纯空白与超长：
    -- 上限与领域层的 maxReasonLen 一致（512），防止绕过应用层写入脏数据。
    CONSTRAINT chk_membership_roles_reason CHECK (
        reason IS NULL OR (btrim(reason) <> '' AND length(reason) <= 512)
    )
);

-- 业务唯一性：同一条成员关系对同一个角色，只能有一行"尚未失效"的授权。
--
-- 刻意建成部分索引而不是全表唯一：revoked / expired 是授权的历史留痕，
-- 撤销之后还要能再次分配同一个角色（重新分配会再插一行，旧行保留以便审计）。
-- 若建成全表唯一，撤销就等于永久占用这对取值，重新授权只能去改旧行，
-- 历史随之被抹掉——这与保留 status 列的初衷相矛盾。
-- 与 idx_tenants_slug_active 是同一个手法，差别只在条件：那里用 deleted_at IS NULL，
-- 这里用 status IN (...)，因为本表没有墓碑列，失效与否由状态本身表达。
--
-- 条件刻意写成 status IN ('pending', 'active') 而不是 status NOT IN (...)：
-- 枚举一旦扩大，前者默认不放行未知状态（失败即关闭），
-- 后者会把未知状态算作"未失效"而继续占用这对取值。
--
-- 重复授权由本约束兜底，适配层把它翻译成 ErrMembershipRoleExists，不得静默吞掉。
-- 配套要求：GetByMembershipAndRole 必须自带同样的状态条件，
-- 否则会命中已失效的历史行，返回本应可以重新分配的授权。
CREATE UNIQUE INDEX IF NOT EXISTS idx_membership_roles_membership_role_active
    ON membership_roles (membership_id, role_id)
    WHERE status IN ('pending', 'active');

-- 支撑 ListByMembershipID / ListActiveByMembershipID / GetByMembershipAndRole：
-- 三者都以 (tenant_id, membership_id) 等值过滤。
--
-- ListByMembershipID 与 ListActiveByMembershipID 的排序为 created_at、id 升序
-- （顺序口径见 Repository 端口注释的"排序与分页"小节），
-- 与本索引的列顺序一致，扫描直接走索引顺序，无需额外排序步骤。
--
-- ListActiveByMembershipID 还要按 status 与到期条件回表过滤，
-- GetByMembershipAndRole 还要按 role_id 回表过滤：
-- 这两列都不在索引前缀里。一个成员持有的角色数是十以内的量级，代价可接受。
--
-- 原设计里那条单独的 membership_id 索引不再单建：
-- 本仓所有查询都带 tenant_id 条件，本索引的前缀已覆盖"按成员关系取授权"，
-- 单建一列只会多一份写放大。
CREATE INDEX IF NOT EXISTS idx_membership_roles_tenant_membership
    ON membership_roles (tenant_id, membership_id, created_at, id);

-- 支撑 ListByRoleID：撤销或调整角色前要评估影响面（"哪些成员持有这个角色"）。
-- 前缀列是 role_id，上一个索引以 membership_id 打头，无法服务于这个方向，
-- 因此必须单独建；带上 tenant_id 让租户条件下推到索引。
CREATE INDEX IF NOT EXISTS idx_membership_roles_tenant_role
    ON membership_roles (tenant_id, role_id);

-- 支撑 ListDueForExpiry：定时任务按 (tenant_id, status, expires_at <= now)
-- 扫描到期未处理的授权，再逐行调用 Expire + Update。
-- 三个列顺序与查询条件一致，扫描走索引前缀。
--
-- 本索引是"expired 由定时任务推进"这一决策的落地：
-- 没有它，定时任务只能全量扫一个租户的授权行再在应用层过滤。
CREATE INDEX IF NOT EXISTS idx_membership_roles_tenant_status_expires
    ON membership_roles (tenant_id, status, expires_at);

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_membership_roles_set_updated_at
    BEFORE UPDATE ON membership_roles
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE membership_roles IS '成员角色授权，(membership_id, role_id) 在未失效的授权间唯一，撤销后可重新分配';
COMMENT ON COLUMN membership_roles.tenant_id IS '租户归属，与 memberships.tenant_id 一致，用于把租户条件下推到本表';
COMMENT ON COLUMN membership_roles.assigned_by IS '分配人用户 ID，系统分配时为 system';
COMMENT ON COLUMN membership_roles.effective_at IS '生效时间，当前恒等于分配时间，不提供预约未来生效';
COMMENT ON COLUMN membership_roles.expires_at IS '失效时间，NULL 表示长期有效；到期由定时任务改写 status 为 expired';
COMMENT ON COLUMN membership_roles.reason IS '变更原因，由最近一次变更覆写';
