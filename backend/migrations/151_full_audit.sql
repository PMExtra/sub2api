-- Full JSON user input audit v1.

CREATE TABLE IF NOT EXISTS audit_message_kv (
    message_hash VARCHAR(64) PRIMARY KEY,
    protocol VARCHAR(64) NOT NULL DEFAULT '',
    role VARCHAR(32) NOT NULL DEFAULT 'user',
    raw_message TEXT NOT NULL,
    raw_message_size INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE TABLE IF NOT EXISTS audit_request_logs (
    id BIGSERIAL PRIMARY KEY,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    user_id BIGINT,
    user_email VARCHAR(255) NOT NULL DEFAULT '',
    api_key_id BIGINT,
    api_key_name VARCHAR(255) NOT NULL DEFAULT '',
    group_id BIGINT,
    group_name VARCHAR(255) NOT NULL DEFAULT '',
    endpoint VARCHAR(255) NOT NULL DEFAULT '',
    provider VARCHAR(64) NOT NULL DEFAULT '',
    model VARCHAR(255) NOT NULL DEFAULT '',
    protocol VARCHAR(64) NOT NULL DEFAULT '',
    body_hash VARCHAR(64) NOT NULL DEFAULT '',
    message_hashes JSONB NOT NULL DEFAULT '[]'::jsonb,
    message_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_created_at
    ON audit_request_logs(created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_request_id
    ON audit_request_logs(request_id);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_user_created
    ON audit_request_logs(user_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_api_key_created
    ON audit_request_logs(api_key_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_group_created
    ON audit_request_logs(group_id, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_audit_request_logs_endpoint_created
    ON audit_request_logs(endpoint, created_at DESC);
