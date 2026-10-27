-- 角色权限关联表，落地 role.Repository.ReplacePermissions 所需的存储结构。
--
-- 这是一张纯关联表：它只刻画"某角色持有某权限"这一事实，不携带任何业务属性，
-- 因此也没有自己的生命周期——不存在从一种状态迁移到另一种状态的路径。
-- 正因为如此它不配独立聚合（对比 memberships：那条关系有
-- invited/active/suspended/left 状态机和 Reinvite 复活语义，所以有自己的领域包）；
-- 权限集合被视为 Role 聚合内部的属性，唯一写入口是 ReplacePermissions 的整体替换。
--
-- 关联不能重复：同一个 (role_id, permission_id) 只允许存在一行，
-- 由下方 idx_role_permissions_role_permission 兜底。
CREATE TABLE IF NOT EXISTS role_permissions (
    -- 代理主键而非把业务键当主键：全仓各表（roles、permissions、memberships）
    -- 一律用 UUID 主键 + 唯一索引表达业务唯一性，本表保持一致，
    -- 便于统一的适配层写法与未来的外键/审计接入。
    id            UUID        PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 租户归属。刻意冗余 role 的 tenant_id：role.Repository 的每个方法都带
    -- tenant_id 条件，落在本表后该条件可以直接下推到索引
    -- （见 idx_role_permissions_tenant_role），写路径不必再 join roles 校验归属，
    -- 跨租户写入会被索引条件本身挡住。
    --
    -- 代价是没有外键兜底（见下方"刻意不加外键"小节），
    -- 本列与 roles.tenant_id 的一致性由"所有写入都必须经过 role.Repository"保证。
    tenant_id     UUID        NOT NULL,
    role_id       UUID        NOT NULL,
    permission_id UUID        NOT NULL,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- 由数据库触发器维护，与全仓各表保持一致。
    -- 需要说明的是：本表当前只有"删除不再需要的关联 + 插入新增的关联"两种写入方式
    -- （整体替换被实现为差分写入，见 role.Repository.ReplacePermissions），
    -- 不做行级 UPDATE，因此 updated_at 实际上会一直等于 created_at。
    -- 保留该列与触发器是为了不与"每张表都有 created_at / updated_at"的约定割裂，
    -- 免得将来第一次出现 UPDATE 时才发现要补迁移。
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- 业务主键：一个角色对同一个权限只能有一行。
--
-- 刻意建成唯一索引而不是 (role_id, permission_id) 复合主键：
-- 代理主键与业务唯一性分开表达是全仓既有做法，可以让将来需要引用本行时不必携带两个 UUID 列。
-- 该索引同时支撑 ListPermissionIDs：按 role_id 前缀扫描即可取回一个角色的全部权限，
-- 无需额外再建 role_id 单列索引。
CREATE UNIQUE INDEX IF NOT EXISTS idx_role_permissions_role_permission
    ON role_permissions (role_id, permission_id);

-- 支撑 ReplacePermissions 的删除分支：整体替换要先删掉该角色在本租户中
-- 已不在目标集合里的旧关联，条件为 (tenant_id = ? AND role_id = ? AND permission_id IN (...))，
-- 列顺序与该 SQL 的等值过滤顺序一致，使这次删除直接走索引前缀，不必回表。
CREATE INDEX IF NOT EXISTS idx_role_permissions_tenant_role
    ON role_permissions (tenant_id, role_id);

-- 支撑反查"哪些角色持有某个权限"（role.Repository.ListRoleIDsByPermissionID）：
-- 前缀列是 permission_id，上面两个索引都以 role_id 打头，无法服务于这个方向。
-- 带上 role_id 让这个方向的扫描不必回表就能拿到结果。
--
-- 该方向的查询必定同时带 tenant_id 条件，
-- 但 tenant_id 没有放进本索引：等值过滤后剩下的候选行极少，
-- 回表过滤的成本远低于多维护一列索引，
-- 而这里的查询频率远低于"按角色取权限"那个方向。
--
-- 没有外键约束，因此这条索引也是将来核对孤儿数据的入口。
CREATE INDEX IF NOT EXISTS idx_role_permissions_permission_role
    ON role_permissions (permission_id, role_id);

-- 刻意不加外键：全仓各表一律不使用 REFERENCES（memberships 关联 tenants / users 也没加），
-- 引用完整性由应用层写路径兜底。写明原因是为了让后来者不要再把它"补"上去。
--
-- 应用层的两道防线（见 internal/adapters/persistence/gorm/role_repository.go）：
--   - 角色归属：写路径先确认角色确实属于本租户；
--   - 权限存在性：写路径先确认待写入的权限标识都真实存在于 permissions 表。
-- 缺了任何一半，写路径都可能留下孤儿行——角色"持有"一项并不存在的权限。
--
-- 不加外键的实际代价与替偿措施：
--   - 删除 role 或 permission 不会被引擎阻止，可能留下孤儿行，
--     因此任何删除路径都必须先经 ReplacePermissions 清空本表，
--     并与 Casbin 策略投影在同一次事务内提交
--     （ports.PolicyRepository 的写入也要求同事务，见 internal/ports/policy.go）；
--   - 反过来说，若这里加了 REFERENCES 而不带 ON DELETE CASCADE，
--     将来第一条删除角色的代码就会被引擎直接拒绝；
--     带 CASCADE 则等于把"顺带清掉哪些表"这件事交给引擎，
--     与"删除顺序由应用编排"的整体取向相冲突。
--     两者相权，跟随本仓既有的、应用层负责一致性的口径。

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_role_permissions_set_updated_at
    BEFORE UPDATE ON role_permissions
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE role_permissions IS '角色权限关联，(role_id, permission_id) 唯一，整体替换由领域层 ReplacePermissions 保证';
COMMENT ON COLUMN role_permissions.tenant_id IS '租户归属，与 roles.tenant_id 一致，用于把租户条件下推到本表';
COMMENT ON COLUMN role_permissions.role_id IS '所属角色';
COMMENT ON COLUMN role_permissions.permission_id IS '关联的系统权限';
