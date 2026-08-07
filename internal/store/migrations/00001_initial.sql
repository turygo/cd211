-- +goose Up
CREATE TABLE categories (
    name TEXT PRIMARY KEY,
    cloud_path TEXT NOT NULL,
    save_path TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);

CREATE TABLE downloads (
    hash TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    source_kind TEXT NOT NULL CHECK (source_kind IN ('magnet', 'torrent')),
    submission_uri TEXT NOT NULL,
    category TEXT NOT NULL,
    cloud_folder TEXT NOT NULL,
    save_path TEXT NOT NULL,
    destination_name TEXT,
    cloud_task_name TEXT,
    cloud_source_path TEXT,
    content_path TEXT,
    is_multi_file INTEGER CHECK (is_multi_file IN (0, 1)),
    total_size INTEGER NOT NULL DEFAULT 0 CHECK (total_size >= 0),
    state TEXT NOT NULL CHECK (state IN (
        'ACCEPTED',
        'STOPPED',
        'SUBMITTING_OFFLINE',
        'WAITING_OFFLINE',
        'SUBMITTING_COPY',
        'WAITING_COPY',
        'VERIFYING_LOCAL',
        'COMPLETED',
        'FAILED',
        'CANCEL_REQUESTED',
        'CANCELLED',
        'DELETE_REQUESTED',
        'DELETED'
    )),
    offline_progress REAL NOT NULL DEFAULT 0 CHECK (offline_progress >= 0 AND offline_progress <= 1),
    copy_progress REAL NOT NULL DEFAULT 0 CHECK (copy_progress >= 0 AND copy_progress <= 1),
    qbit_progress REAL NOT NULL DEFAULT 0 CHECK (qbit_progress >= 0 AND qbit_progress <= 1),
    last_upstream_status TEXT,
    last_error TEXT,
    phase_started_at TIMESTAMP NOT NULL,
    next_run_at TIMESTAMP,
    lease_until TIMESTAMP,
    lease_owner TEXT,
    attempt_count INTEGER NOT NULL DEFAULT 0 CHECK (attempt_count >= 0),
    delete_files_requested INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    removed_at TIMESTAMP,
    row_version INTEGER NOT NULL DEFAULT 0 CHECK (row_version >= 0)
);

CREATE TABLE download_files (
    download_hash TEXT NOT NULL,
    file_index INTEGER NOT NULL,
    relative_path TEXT NOT NULL,
    size INTEGER NOT NULL CHECK (size >= 0),
    PRIMARY KEY (download_hash, file_index),
    FOREIGN KEY (download_hash) REFERENCES downloads(hash) ON DELETE CASCADE
);

CREATE INDEX idx_downloads_visible_category ON downloads (category, removed_at, created_at, hash);
CREATE INDEX idx_downloads_due_claim ON downloads (next_run_at, lease_until, created_at, hash);
CREATE UNIQUE INDEX idx_downloads_live_destination ON downloads (save_path, destination_name)
WHERE destination_name IS NOT NULL AND state != 'DELETED';

-- +goose StatementBegin
CREATE TRIGGER guard_download_destination_insert
BEFORE INSERT ON downloads
WHEN NEW.destination_name IS NOT NULL AND NEW.state != 'DELETED'
BEGIN
    SELECT RAISE(ABORT, 'destination path conflicts with save root')
    WHERE EXISTS (
        SELECT 1
        FROM categories AS category
        WHERE category.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
           OR substr(
                category.save_path,
                1,
                length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1
              ) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
    ) OR EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND (
              other.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(
                    other.save_path,
                    1,
                    length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1
                 ) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
          )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_download_destination_update
BEFORE UPDATE OF destination_name, save_path, state ON downloads
WHEN NEW.destination_name IS NOT NULL AND NEW.state != 'DELETED'
BEGIN
    SELECT RAISE(ABORT, 'destination path conflicts with save root')
    WHERE EXISTS (
        SELECT 1
        FROM categories AS category
        WHERE category.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
           OR substr(
                category.save_path,
                1,
                length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1
              ) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
    ) OR EXISTS (
        SELECT 1
        FROM downloads AS other
        WHERE other.hash != NEW.hash
          AND (other.state != 'DELETED' OR (other.content_path IS NOT NULL AND other.delete_files_requested = 0))
          AND (
              other.save_path = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name
              OR substr(
                    other.save_path,
                    1,
                    length(rtrim(NEW.save_path, '/') || '/' || NEW.destination_name) + 1
                 ) = rtrim(NEW.save_path, '/') || '/' || NEW.destination_name || '/'
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
        SELECT 1
        FROM downloads AS download
        WHERE download.destination_name IS NOT NULL
          AND (
              download.state != 'DELETED'
              OR (download.content_path IS NOT NULL AND download.delete_files_requested = 0)
          )
          AND (
              NEW.save_path = rtrim(download.save_path, '/') || '/' || download.destination_name
              OR substr(
                    NEW.save_path,
                    1,
                    length(rtrim(download.save_path, '/') || '/' || download.destination_name) + 1
                 ) = rtrim(download.save_path, '/') || '/' || download.destination_name || '/'
          )
    );
END;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE TRIGGER guard_category_save_path_update
BEFORE UPDATE OF save_path ON categories
BEGIN
    SELECT RAISE(ABORT, 'category save root conflicts with destination path')
    WHERE EXISTS (
        SELECT 1
        FROM downloads AS download
        WHERE download.destination_name IS NOT NULL
          AND (
              download.state != 'DELETED'
              OR (download.content_path IS NOT NULL AND download.delete_files_requested = 0)
          )
          AND (
              NEW.save_path = rtrim(download.save_path, '/') || '/' || download.destination_name
              OR substr(
                    NEW.save_path,
                    1,
                    length(rtrim(download.save_path, '/') || '/' || download.destination_name) + 1
                 ) = rtrim(download.save_path, '/') || '/' || download.destination_name || '/'
          )
    );
END;
-- +goose StatementEnd

-- +goose Down
DROP TABLE download_files;
DROP TABLE downloads;
DROP TABLE categories;
