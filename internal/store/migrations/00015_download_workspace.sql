-- +goose Up
-- Any logical save root containing the reserved .cd211 component must be
-- rejected before the migration can create generated workspace paths.
-- RAISE() is legal only in a trigger program, so probe the old tables through
-- no-op updates before adding the new column.
-- +goose StatementBegin
CREATE TRIGGER guard_workspace_migration_collision
BEFORE UPDATE ON downloads
WHEN (
    OLD.destination_name = '.cd211'
    AND (OLD.state != 'DELETED' OR (OLD.content_path IS NOT NULL AND OLD.delete_files_requested = 0))
) OR (
    (OLD.state != 'DELETED' OR (OLD.content_path IS NOT NULL AND OLD.delete_files_requested = 0))
    AND instr('/' || OLD.save_path || '/', '/.cd211/') > 0
)
BEGIN
    SELECT RAISE(ABORT, 'legacy .cd211 destination or save path reserves workspace namespace');
END;
-- +goose StatementEnd
UPDATE downloads SET hash = hash;
DROP TRIGGER guard_workspace_migration_collision;

-- Categories have no deleted state, so probe them separately before changing
-- the schema as well.
-- +goose StatementBegin
CREATE TRIGGER guard_category_migration_reserved_save_path
BEFORE UPDATE ON categories
WHEN instr('/' || OLD.save_path || '/', '/.cd211/') > 0
BEGIN
    SELECT RAISE(ABORT, 'category save path contains reserved .cd211 component');
END;
-- +goose StatementEnd
UPDATE categories SET name = name;
DROP TRIGGER guard_category_migration_reserved_save_path;

ALTER TABLE downloads ADD COLUMN workspace_path TEXT;

DROP INDEX idx_downloads_live_destination;
DROP TRIGGER guard_download_destination_insert;
DROP TRIGGER guard_download_destination_update;
DROP TRIGGER guard_category_save_path_insert;
DROP TRIGGER guard_category_save_path_update;
CREATE UNIQUE INDEX idx_downloads_live_legacy_destination ON downloads (save_path, destination_name)
WHERE destination_name IS NOT NULL
  AND (state != 'DELETED' OR (content_path IS NOT NULL AND delete_files_requested = 0))
  AND workspace_path IS NULL;

CREATE UNIQUE INDEX idx_downloads_workspace_path ON downloads (workspace_path)
WHERE workspace_path IS NOT NULL;

-- +goose StatementBegin
CREATE TRIGGER guard_download_save_path_reserved_insert
BEFORE INSERT ON downloads
WHEN instr('/' || NEW.save_path || '/', '/.cd211/') > 0
BEGIN
    SELECT RAISE(ABORT, 'download save path contains reserved .cd211 component');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_download_save_path_reserved_update
BEFORE UPDATE OF save_path ON downloads
WHEN instr('/' || NEW.save_path || '/', '/.cd211/') > 0
BEGIN
    SELECT RAISE(ABORT, 'download save path contains reserved .cd211 component');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_category_save_path_reserved_insert
BEFORE INSERT ON categories
WHEN instr('/' || NEW.save_path || '/', '/.cd211/') > 0
BEGIN
    SELECT RAISE(ABORT, 'category save path contains reserved .cd211 component');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_category_save_path_reserved_update
BEFORE UPDATE OF save_path ON categories
WHEN instr('/' || NEW.save_path || '/', '/.cd211/') > 0
BEGIN
    SELECT RAISE(ABORT, 'category save path contains reserved .cd211 component');
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_download_destination_insert
BEFORE INSERT ON downloads
WHEN NEW.workspace_path IS NOT NULL
 OR (NEW.destination_name IS NOT NULL
     AND (NEW.state != 'DELETED' OR (NEW.content_path IS NOT NULL AND NEW.delete_files_requested = 0)))
BEGIN
    SELECT RAISE(ABORT, 'destination path conflicts with save root')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1
        FROM categories AS category
        WHERE category.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
           OR substr(category.save_path, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
    );

    -- Legacy destinations cannot overlap another retained/live destination or
    -- workspace. Both containment directions are destructive.
    SELECT RAISE(ABORT, 'destination path conflicts with another download')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND other.destination_name IS NOT NULL
          AND other.workspace_path IS NULL
          AND (
              rtrim(other.save_path, '/') || '/' || other.destination_name = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(rtrim(other.save_path, '/') || '/' || other.destination_name, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
              OR substr(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name, 1, length(rtrim(other.save_path, '/') || '/' || other.destination_name) + 1) = rtrim(other.save_path, '/') || '/' || other.destination_name || '/'
          )
    );
    SELECT RAISE(ABORT, 'destination path conflicts with workspace')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND other.workspace_path IS NOT NULL
          AND (
              other.workspace_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(other.workspace_path, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
              OR substr(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name, 1, length(other.workspace_path) + 1) = other.workspace_path || '/'
          )
    );

    SELECT RAISE(ABORT, 'category save root conflicts with destination path')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1 FROM categories AS category
        WHERE category.save_path = NEW.workspace_path
           OR substr(category.save_path, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
    );

    -- A logical save path cannot be equal to or below another workspace, and
    -- a new workspace cannot equal or contain another logical save path.
    SELECT RAISE(ABORT, 'download paths are nested')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (
              (other.workspace_path IS NOT NULL AND (
                  NEW.save_path = other.workspace_path
                  OR substr(NEW.save_path, 1, length(other.workspace_path) + 1) = other.workspace_path || '/'
              ))
              OR NEW.workspace_path = other.save_path
              OR substr(other.save_path, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
          )
    );

    SELECT RAISE(ABORT, 'workspace conflicts with destination path')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND other.workspace_path IS NULL
          AND other.destination_name IS NOT NULL
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND (
              NEW.workspace_path = rtrim(other.save_path, '/') || '/' || other.destination_name
              OR substr(NEW.workspace_path, 1, length(rtrim(other.save_path, '/') || '/' || other.destination_name) + 1) = rtrim(other.save_path, '/') || '/' || other.destination_name || '/'
              OR substr(rtrim(other.save_path, '/') || '/' || other.destination_name, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
          )
    );
END;
-- +goose StatementEnd
-- +goose StatementBegin
CREATE TRIGGER guard_download_destination_update
BEFORE UPDATE OF destination_name, save_path, workspace_path, state, content_path, delete_files_requested ON downloads
WHEN NEW.workspace_path IS NOT NULL
 OR (NEW.destination_name IS NOT NULL
     AND (NEW.state != 'DELETED' OR (NEW.content_path IS NOT NULL AND NEW.delete_files_requested = 0)))
BEGIN
    SELECT RAISE(ABORT, 'destination path conflicts with save root')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1 FROM categories AS category
        WHERE category.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
           OR substr(category.save_path, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
    );
    SELECT RAISE(ABORT, 'destination path conflicts with another download')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND other.destination_name IS NOT NULL
          AND other.workspace_path IS NULL
          AND (
              rtrim(other.save_path, '/') || '/' || other.destination_name = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(rtrim(other.save_path, '/') || '/' || other.destination_name, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
              OR substr(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name, 1, length(rtrim(other.save_path, '/') || '/' || other.destination_name) + 1) = rtrim(other.save_path, '/') || '/' || other.destination_name || '/'
          )
    );
    SELECT RAISE(ABORT, 'destination path conflicts with workspace')
    WHERE NEW.workspace_path IS NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND other.workspace_path IS NOT NULL
          AND (
              other.workspace_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(other.workspace_path, 1, length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
              OR substr(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name, 1, length(other.workspace_path) + 1) = other.workspace_path || '/'
          )
    );
    SELECT RAISE(ABORT, 'category save root conflicts with destination path')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1 FROM categories AS category
        WHERE category.save_path = NEW.workspace_path
           OR substr(category.save_path, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
    );
    SELECT RAISE(ABORT, 'download paths are nested')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (
              (other.workspace_path IS NOT NULL AND (
                  NEW.save_path = other.workspace_path
                  OR substr(NEW.save_path, 1, length(other.workspace_path) + 1) = other.workspace_path || '/'
              ))
              OR NEW.workspace_path = other.save_path
              OR substr(other.save_path, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
          )
    );
    SELECT RAISE(ABORT, 'workspace conflicts with destination path')
    WHERE NEW.workspace_path IS NOT NULL AND EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND other.workspace_path IS NULL
          AND other.destination_name IS NOT NULL
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND (
              NEW.workspace_path = rtrim(other.save_path, '/') || '/' || other.destination_name
              OR substr(NEW.workspace_path, 1, length(rtrim(other.save_path, '/') || '/' || other.destination_name) + 1) = rtrim(other.save_path, '/') || '/' || other.destination_name || '/'
              OR substr(rtrim(other.save_path, '/') || '/' || other.destination_name, 1, length(NEW.workspace_path) + 1) = NEW.workspace_path || '/'
          )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_category_save_path_insert
BEFORE INSERT ON categories
BEGIN
    SELECT RAISE(ABORT, 'category save root conflicts with destination path')
    WHERE EXISTS (
        SELECT 1 FROM downloads AS download
        WHERE download.destination_name IS NOT NULL
          AND (download.state != 'DELETED' OR (download.content_path IS NOT NULL AND download.delete_files_requested = 0))
          AND (NEW.save_path = rtrim(download.save_path, '/') || '/' || download.destination_name
            OR substr(NEW.save_path, 1, length(rtrim(download.save_path, '/') || '/' || download.destination_name) + 1) = rtrim(download.save_path, '/') || '/' || download.destination_name || '/')
    );
    SELECT RAISE(ABORT, 'category save root is inside download workspace')
    WHERE EXISTS (
        SELECT 1 FROM downloads AS download
        WHERE download.workspace_path IS NOT NULL
          AND (NEW.save_path = download.workspace_path OR substr(NEW.save_path, 1, length(download.workspace_path) + 1) = download.workspace_path || '/')
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_category_save_path_update
BEFORE UPDATE OF save_path ON categories
BEGIN
    SELECT RAISE(ABORT, 'category save root conflicts with destination path')
    WHERE EXISTS (
        SELECT 1 FROM downloads AS download
        WHERE download.destination_name IS NOT NULL
          AND (download.state != 'DELETED' OR (download.content_path IS NOT NULL AND download.delete_files_requested = 0))
          AND (NEW.save_path = rtrim(download.save_path, '/') || '/' || download.destination_name
            OR substr(NEW.save_path, 1, length(rtrim(download.save_path, '/') || '/' || download.destination_name) + 1) = rtrim(download.save_path, '/') || '/' || download.destination_name || '/')
    );
    SELECT RAISE(ABORT, 'category save root is inside download workspace')
    WHERE EXISTS (
        SELECT 1 FROM downloads AS download
        WHERE download.workspace_path IS NOT NULL
          AND (NEW.save_path = download.workspace_path OR substr(NEW.save_path, 1, length(download.workspace_path) + 1) = download.workspace_path || '/')
    );
END;
-- +goose StatementEnd


-- Only pristine pre-copy rows are safe to isolate. Historical rows with any
-- copy/content evidence remain in the intentional legacy shared layout.
UPDATE downloads
SET workspace_path = rtrim(save_path, '/') || '/.cd211/' || hash
WHERE workspace_path IS NULL
  AND state IN ('ACCEPTED', 'STOPPED', 'SUBMITTING_OFFLINE', 'WAITING_OFFLINE')
  AND cloud_task_name IS NULL
  AND cloud_result_path IS NULL
  AND copy_source_path IS NULL
  AND content_path IS NULL
  AND length(hash) = 40
  AND lower(hash) = hash
  AND hash NOT GLOB '*[^0-9a-f]*'
  AND save_path != ''
  AND (save_path = '/' OR save_path = rtrim(save_path, '/'))
  AND save_path NOT LIKE '%//%'
  AND save_path NOT LIKE '%/./%'
  AND substr(save_path, 1, 1) = '/'
  AND save_path NOT LIKE '%/../%'
  AND NOT EXISTS (
      SELECT 1
      FROM categories AS category
      WHERE category.save_path = rtrim(downloads.save_path, '/') || '/.cd211/' || downloads.hash
         OR substr(category.save_path, 1, length(rtrim(downloads.save_path, '/') || '/.cd211/' || downloads.hash) + 1) = rtrim(downloads.save_path, '/') || '/.cd211/' || downloads.hash || '/'
  );

-- +goose Down
-- SQLite cannot drop columns on all supported versions; the forward migration
-- is intentionally irreversible, matching the existing migration convention.
