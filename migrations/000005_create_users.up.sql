-- 用户表，对应 domain/user.User。
--
-- 本表是 ZITADEL 用户的业务侧映射：不保存任何认证凭据，
-- 身份的权威来源是 ZITADEL，zitadel_user_id 是同步的幂等依据。
--
-- 列长度与 domain/user 包中的约束保持一致：
-- zitadel_user_id 与 username 最长 64、display_name 最长 255（domain.MaxNameLen）、
-- email 最长 254、phone 最长 32、avatar_url 最长 2048。
CREATE TABLE IF NOT EXISTS users (
    id              UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    -- 用户名：ZITADEL 允许以邮箱登录，用户名可以缺省，因此允许空串。
    username        VARCHAR(64)  NOT NULL DEFAULT '',
    display_name    VARCHAR(255) NOT NULL DEFAULT '',
    -- 邮箱统一存 NormalizeEmail 的输出（去空白、转小写），
    -- 唯一索引建立在同一口径上，见下方 idx_users_email。
    email           VARCHAR(254) NOT NULL DEFAULT '',
    phone           VARCHAR(32)  NOT NULL DEFAULT '',
    avatar_url      VARCHAR(2048) NOT NULL DEFAULT '',
    -- 外部身份唯一标识，也是同步的幂等依据，见 idx_users_zitadel_user_id。
    zitadel_user_id VARCHAR(64)  NOT NULL,
    -- 状态取值对应 user.UserStatus 的三个常量。
    status          VARCHAR(16)  NOT NULL DEFAULT 'active',
    email_verified  BOOLEAN      NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ  NOT NULL DEFAULT now(),

    CONSTRAINT chk_users_status CHECK (
        status IN ('active', 'inactive', 'suspended')
    ),
    -- 空标识会让"按 zitadel_user_id 同步"失去依据，因此连纯空白也不放行。
    CONSTRAINT chk_users_zitadel_user_id_not_blank CHECK (btrim(zitadel_user_id) <> ''),
    -- 三个格式约束与 domain/user 的正则保持一致，防止绕过应用层写入脏数据。
    -- 空串表示"该用户未提供"，单独放行。
    CONSTRAINT chk_users_username_format CHECK (
        username = '' OR username ~ '^[a-z0-9._-]+$'
    ),
    CONSTRAINT chk_users_email_format CHECK (
        email = '' OR email ~ '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$'
    ),
    CONSTRAINT chk_users_phone_format CHECK (
        phone = '' OR phone ~ '^\+?[0-9]{6,20}$'
    ),
    -- 已验证必须有邮箱：不允许出现 email 为空却标记为已验证的行，
    -- 否则"按邮箱通知"这类流程会把已验证用户当成没有收件地址。
    CONSTRAINT chk_users_email_verified_requires_email CHECK (
        NOT email_verified OR email <> ''
    )
);

-- zitadel_user_id 全表唯一：它是同步的唯一依据，
-- 重复创建必须由数据库兜底发现（适配层把它翻译成 user.ErrUserExists）。
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_zitadel_user_id
    ON users (zitadel_user_id);

-- username 与 email 只在非空值之间唯一。
--
-- 刻意建成部分索引而不是全表唯一：空串表示"该用户没有设置这一项"，
-- 若建成全表唯一，第二个没有邮箱（或没有用户名）的用户就会插入失败——
-- 而这两项在 ZITADEL 侧本来就是可选项。
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_username
    ON users (username)
    WHERE username <> '';

CREATE UNIQUE INDEX IF NOT EXISTS idx_users_email
    ON users (email)
    WHERE email <> '';

-- phone 只建普通索引，不做唯一约束：
-- 手机号会被运营商回收后再分配给他人，建成唯一会让新用户抢不到归属过他人的号码。
-- 待"手机号登录"落地、且明确号码在本系统的归属规则后，再评估是否收紧为唯一。
CREATE INDEX IF NOT EXISTS idx_users_phone
    ON users (phone)
    WHERE phone <> '';

-- 由数据库强制维护 updated_at，函数来自 000001_init_functions。
CREATE TRIGGER trg_users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW
    EXECUTE FUNCTION set_updated_at();

COMMENT ON TABLE users IS '业务用户，ZITADEL 用户的本地映射，不保存认证凭据';
COMMENT ON COLUMN users.zitadel_user_id IS '外部身份唯一标识，也是同步的幂等依据';
COMMENT ON COLUMN users.email IS '归一化后的小写邮箱，空串表示未设置';
COMMENT ON COLUMN users.status IS '用户状态，取值见 user.UserStatus';
