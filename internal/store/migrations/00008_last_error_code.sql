-- +goose Up
ALTER TABLE downloads ADD COLUMN last_error_code TEXT;

-- Rows written before problem codes existed keep their English text and are
-- labeled legacy so consumers can distinguish structured problems from the
-- unstructured messages of older versions.
UPDATE downloads
SET last_error_code = 'legacy'
WHERE last_error_code IS NULL
  AND last_error IS NOT NULL
  AND length(trim(last_error)) > 0;

-- +goose Down
ALTER TABLE downloads DROP COLUMN last_error_code;
