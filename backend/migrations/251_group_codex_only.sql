ALTER TABLE groups ADD COLUMN IF NOT EXISTS codex_only BOOLEAN NOT NULL DEFAULT FALSE;
COMMENT ON COLUMN groups.codex_only IS '是否仅允许 Codex 官方客户端（OpenAI 分组）';
COMMENT ON COLUMN groups.fallback_group_id IS '非官方客户端请求降级使用的分组 ID';
