-- +goose Up
-- The operator explicitly needs to recover both API credentials from the
-- authenticated Settings page after restarts. Existing rows keep working but
-- have an empty secret because their plaintext cannot be reconstructed from a
-- digest; revoke and generate replaces those legacy rows.
ALTER TABLE api_token ADD COLUMN token_secret TEXT NOT NULL DEFAULT '';
ALTER TABLE qbt_api_key ADD COLUMN key_secret TEXT NOT NULL DEFAULT '';

-- +goose Down
-- SQLite does not support DROP COLUMN on all versions used by CD211. The
-- columns are harmless on rollback and are removed with their parent tables.
