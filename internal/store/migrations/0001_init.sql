-- 0001_init: 个人 AI API 网关初始 schema。
--
-- 约定：
--   * 时间统一存 UTC RFC3339 文本；
--   * 布尔值用 INTEGER 0/1；
--   * 单管理员：admin_credentials 只有 id=1 一行；
--   * 删除渠道时 model_routes 级联删除，历史日志保留 channel_name 快照；
--   * request_logs/request_attempts 不保存 header、prompt 或 body。

CREATE TABLE IF NOT EXISTS schema_migrations (
    version    INTEGER PRIMARY KEY,
    name       TEXT NOT NULL,
    checksum   TEXT NOT NULL,
    applied_at TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_credentials (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL,
    updated_at    TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS admin_sessions (
    id           TEXT PRIMARY KEY,
    token_hash   TEXT NOT NULL UNIQUE,
    created_at   TEXT NOT NULL,
    expires_at   TEXT NOT NULL,
    last_seen_at TEXT,
    revoked_at   TEXT
);

CREATE TABLE IF NOT EXISTS channels (
    id                     INTEGER PRIMARY KEY AUTOINCREMENT,
    name                   TEXT NOT NULL UNIQUE,
    base_url               TEXT NOT NULL,
    protocol               TEXT NOT NULL CHECK (protocol IN ('openai_responses', 'openai_chat', 'anthropic_messages')),
    key_ciphertext         TEXT NOT NULL,
    key_mask               TEXT NOT NULL,
    priority               INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1000),
    max_concurrency        INTEGER NOT NULL DEFAULT 1 CHECK (max_concurrency BETWEEN 1 AND 64),
    enabled                INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    last_used_at           TEXT,
    last_health_status     TEXT,
    last_health_latency_ms INTEGER,
    last_health_at         TEXT,
    last_error             TEXT,
    created_at             TEXT NOT NULL,
    updated_at             TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS model_routes (
    id             INTEGER PRIMARY KEY AUTOINCREMENT,
    client_model   TEXT NOT NULL,
    channel_id     INTEGER NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    upstream_model TEXT NOT NULL,
    priority       INTEGER NOT NULL DEFAULT 0 CHECK (priority BETWEEN 0 AND 1000),
    enabled        INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at     TEXT NOT NULL,
    updated_at     TEXT NOT NULL,
    UNIQUE (client_model, channel_id)
);

CREATE TABLE IF NOT EXISTS request_logs (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id         TEXT NOT NULL UNIQUE,
    protocol           TEXT NOT NULL,
    endpoint           TEXT NOT NULL DEFAULT '',
    model              TEXT NOT NULL DEFAULT '',
    channel_id         INTEGER REFERENCES channels(id) ON DELETE SET NULL,
    channel_name       TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL,
    http_status        INTEGER,
    error_code         TEXT,
    error_message      TEXT,
    stream             INTEGER NOT NULL DEFAULT 0 CHECK (stream IN (0, 1)),
    attempt_count      INTEGER NOT NULL DEFAULT 0,
    retry_count        INTEGER NOT NULL DEFAULT 0,
    ttft_ms            INTEGER,
    latency_ms         INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER,
    output_tokens      INTEGER,
    total_tokens       INTEGER,
    usage_source       TEXT,
    semantic_committed INTEGER NOT NULL DEFAULT 0 CHECK (semantic_committed IN (0, 1)),
    response_bytes     INTEGER,
    started_at         TEXT NOT NULL,
    finished_at        TEXT
);

CREATE TABLE IF NOT EXISTS request_attempts (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    -- request_id 不设外键：T20 约定先逐次写 attempt、最后写 request_log，
    -- 保留这个写入顺序；日志保留期清理时按 request_id 一并删除 attempts。
    request_id         TEXT NOT NULL,
    attempt_index      INTEGER NOT NULL,
    channel_id         INTEGER REFERENCES channels(id) ON DELETE SET NULL,
    channel_name       TEXT NOT NULL DEFAULT '',
    upstream_model     TEXT NOT NULL DEFAULT '',
    status             TEXT NOT NULL,
    http_status        INTEGER,
    error_code         TEXT,
    error_message      TEXT,
    latency_ms         INTEGER NOT NULL DEFAULT 0,
    semantic_committed INTEGER NOT NULL DEFAULT 0 CHECK (semantic_committed IN (0, 1)),
    started_at         TEXT NOT NULL,
    finished_at        TEXT,
    UNIQUE (request_id, attempt_index)
);

-- 按需捕获：默认无行；request/response body 为 AES-GCM 密文，读写都由
-- observability/adminapi 层处理。捕获不引用 request_logs，日志保留期清理
-- 不影响仍在 TTL 内的捕获。
CREATE TABLE IF NOT EXISTS request_captures (
    id                       INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id               TEXT NOT NULL UNIQUE,
    request_body_ciphertext  TEXT,
    response_body_ciphertext TEXT,
    request_truncated        INTEGER NOT NULL DEFAULT 0 CHECK (request_truncated IN (0, 1)),
    response_truncated       INTEGER NOT NULL DEFAULT 0 CHECK (response_truncated IN (0, 1)),
    request_bytes            INTEGER,
    response_bytes           INTEGER,
    created_at               TEXT NOT NULL,
    expires_at               TEXT NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_admin_sessions_expires_at
    ON admin_sessions(expires_at);

CREATE INDEX IF NOT EXISTS idx_channels_enabled_priority
    ON channels(enabled, priority, id);

CREATE INDEX IF NOT EXISTS idx_model_routes_client_model
    ON model_routes(client_model, enabled, priority);

CREATE INDEX IF NOT EXISTS idx_model_routes_channel_id
    ON model_routes(channel_id);

CREATE INDEX IF NOT EXISTS idx_request_logs_started_at
    ON request_logs(started_at DESC);

CREATE INDEX IF NOT EXISTS idx_request_logs_channel_id
    ON request_logs(channel_id, started_at);

CREATE INDEX IF NOT EXISTS idx_request_logs_model
    ON request_logs(model, started_at);

CREATE INDEX IF NOT EXISTS idx_request_logs_status
    ON request_logs(status, started_at);

CREATE INDEX IF NOT EXISTS idx_request_attempts_request_id
    ON request_attempts(request_id, attempt_index);

CREATE INDEX IF NOT EXISTS idx_request_attempts_channel_id
    ON request_attempts(channel_id, started_at);

CREATE INDEX IF NOT EXISTS idx_request_captures_expires_at
    ON request_captures(expires_at);
