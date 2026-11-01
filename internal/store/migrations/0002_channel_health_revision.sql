-- 将探针结果绑定到连接配置代次；旧结果缺少模型及协议证据，升级时失效。
ALTER TABLE channels ADD COLUMN connection_revision INTEGER NOT NULL DEFAULT 1 CHECK (connection_revision >= 1);
ALTER TABLE channels ADD COLUMN last_health_revision INTEGER NOT NULL DEFAULT 0 CHECK (last_health_revision >= 0);
ALTER TABLE channels ADD COLUMN last_health_model TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN last_health_protocol TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN last_health_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE channels ADD COLUMN last_health_duration_ms INTEGER;

UPDATE channels SET last_health_status = NULL, last_health_latency_ms = NULL,
    last_health_at = NULL, last_error = NULL;
