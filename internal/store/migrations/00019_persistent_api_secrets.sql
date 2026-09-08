-- +goose Up
-- 旧凭据保留摘要认证，无法恢复的明文保持为空；新凭据保存明文供设置页读取。
ALTER TABLE api_token ADD COLUMN token_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE qbt_api_key ADD COLUMN key_secret TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE api_token DROP COLUMN token_secret;
ALTER TABLE qbt_api_key DROP COLUMN key_secret;
