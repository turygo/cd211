// Package store owns the SQLite schema, migrations, and durable download and
// category repository.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/pressly/goose/v3"
	"github.com/turygo/cd211/internal/store/sqlc"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

type Store struct {
	db      *sql.DB
	queries *storedb.Queries
}

func Open(ctx context.Context, databasePath string) (*Store, error) {
	if err := ensureDatabaseParent(databasePath); err != nil {
		return nil, err
	}
	if err := restrictDatabaseFiles(databasePath); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	fail := func(cause error) (*Store, error) {
		_ = db.Close()
		return nil, cause
	}

	if err := db.PingContext(ctx); err != nil {
		return fail(fmt.Errorf("ping database: %w", err))
	}
	if err := configureDatabase(ctx, db); err != nil {
		return fail(err)
	}
	if err := applyMigrations(ctx, db); err != nil {
		return fail(err)
	}

	return &Store{
		db:      db,
		queries: storedb.New(db),
	}, nil
}

func (s *Store) PingContext(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) Ready(ctx context.Context, localRoot string) error {
	if err := s.PingContext(ctx); err != nil {
		return errors.New("store readiness: database unavailable")
	}

	info, err := os.Stat(localRoot)
	if err != nil || !info.IsDir() {
		return errors.New("store readiness: local root unavailable")
	}

	probe, err := os.CreateTemp(localRoot, ".cd211-ready-*")
	if err != nil {
		return errors.New("store readiness: local root is not writable")
	}
	probePath := probe.Name()
	defer os.Remove(probePath)

	if err := probe.Close(); err != nil {
		return errors.New("store readiness: local root is not writable")
	}
	if err := os.Remove(probePath); err != nil {
		return errors.New("store readiness: local root is not writable")
	}

	return nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func restrictDatabaseFiles(databasePath string) error {
	for index, path := range []string{databasePath, databasePath + "-wal", databasePath + "-shm"} {
		pathInfo, err := os.Lstat(path)
		missing := errors.Is(err, os.ErrNotExist)
		switch {
		case err == nil && !pathInfo.Mode().IsRegular():
			return errors.New("restrict database file permissions: database file is not a regular file")
		case err != nil && !missing:
			return fmt.Errorf("inspect database file path: %w", err)
		case missing && index != 0:
			continue
		}

		flags := os.O_RDWR
		if missing {
			flags |= os.O_CREATE | os.O_EXCL
		}
		file, err := os.OpenFile(path, flags, 0o600)
		if err != nil {
			return fmt.Errorf("open database file for permission check: %w", err)
		}
		info, statErr := file.Stat()
		currentPathInfo, lstatErr := os.Lstat(path)
		modeErr := error(nil)
		if statErr == nil && lstatErr == nil {
			if !info.Mode().IsRegular() || !currentPathInfo.Mode().IsRegular() || !os.SameFile(info, currentPathInfo) {
				modeErr = errors.New("database file is not a regular file")
			} else {
				modeErr = file.Chmod(0o600)
			}
		}
		closeErr := file.Close()
		switch {
		case statErr != nil:
			return fmt.Errorf("stat database file: %w", statErr)
		case lstatErr != nil:
			return fmt.Errorf("inspect database file path: %w", lstatErr)
		case modeErr != nil:
			return fmt.Errorf("restrict database file permissions: %w", modeErr)
		case closeErr != nil:
			return fmt.Errorf("close database file permission check: %w", closeErr)
		}
	}
	return nil
}

func ensureDatabaseParent(databasePath string) error {
	parent := filepath.Dir(databasePath)
	info, err := os.Stat(parent)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("database parent is not a directory: %s", parent)
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("stat database parent: %w", err)
	}

	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create database parent: %w", err)
	}
	if err := os.Chmod(parent, 0o700); err != nil {
		return fmt.Errorf("restrict database parent permissions: %w", err)
	}
	return nil
}

func configureDatabase(ctx context.Context, db *sql.DB) error {
	var journalMode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode = WAL").Scan(&journalMode); err != nil {
		return fmt.Errorf("enable WAL journal mode: %w", err)
	}
	if !strings.EqualFold(journalMode, "wal") {
		return fmt.Errorf("verify WAL journal mode: got %q", journalMode)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA synchronous = FULL"); err != nil {
		return fmt.Errorf("set synchronous mode: %w", err)
	}
	var synchronous int
	if err := db.QueryRowContext(ctx, "PRAGMA synchronous").Scan(&synchronous); err != nil {
		return fmt.Errorf("read synchronous mode: %w", err)
	}
	if synchronous != 2 {
		return fmt.Errorf("verify synchronous mode: got %d", synchronous)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA foreign_keys = ON"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}
	var foreignKeys int
	if err := db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign keys setting: %w", err)
	}
	if foreignKeys != 1 {
		return fmt.Errorf("verify foreign keys setting: got %d", foreignKeys)
	}

	if _, err := db.ExecContext(ctx, "PRAGMA busy_timeout = 5000"); err != nil {
		return fmt.Errorf("set busy timeout: %w", err)
	}
	var busyTimeout int
	if err := db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busyTimeout); err != nil {
		return fmt.Errorf("read busy timeout: %w", err)
	}
	if busyTimeout != 5000 {
		return fmt.Errorf("verify busy timeout: got %d", busyTimeout)
	}

	return nil
}

func applyMigrations(ctx context.Context, db *sql.DB) error {
	migrations, err := fs.Sub(embeddedMigrations, "migrations")
	if err != nil {
		return fmt.Errorf("load embedded migrations: %w", err)
	}

	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrations)
	if err != nil {
		return fmt.Errorf("create migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}
