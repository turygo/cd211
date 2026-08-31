package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
)

// ListTags returns globally-created tags together with tags assigned to visible downloads.
func (s *Store) ListTags(ctx context.Context) ([]string, error) {
	rows, err := s.queries.ListQbtTags(ctx)
	if err != nil {
		return nil, fmt.Errorf("list tags: %w", err)
	}
	seen := make(map[string]struct{}, len(rows))
	for _, tag := range rows {
		seen[tag] = struct{}{}
	}
	downloads, err := s.ListDownloads(ctx, nil)
	if err != nil {
		return nil, err
	}
	for _, download := range downloads {
		for _, tag := range strings.Split(download.Tags, ",") {
			tag = strings.TrimSpace(tag)
			if tag != "" {
				seen[tag] = struct{}{}
			}
		}
	}
	result := make([]string, 0, len(seen))
	for tag := range seen {
		result = append(result, tag)
	}
	sort.Strings(result)
	return result, nil
}

// CreateTags adds each canonical tag to the global registry. Existing tags are
// intentionally accepted so qB callers can repeat the operation safely.
func (s *Store) CreateTags(ctx context.Context, tags string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTransition
	}
	list, err := canonicalQBTTagList(tags, false)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin create tags: %w", err)
	}
	queries := s.queries.WithTx(tx)
	changed := false
	for _, tag := range list {
		inserted, err := queries.InsertQbtTag(ctx, tag)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("create tag: %w", err)
		}
		changed = changed || inserted > 0
	}
	return s.commitMutationTx(tx, changed)
}

// DeleteTags removes global tags and all assignments in one transaction.
func (s *Store) DeleteTags(ctx context.Context, tags string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTransition
	}
	list, err := canonicalQBTTagList(tags, false)
	if err != nil {
		return err
	}
	if len(list) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete tags: %w", err)
	}
	queries := s.queries.WithTx(tx)
	changed := false
	for _, tag := range list {
		removed, err := queries.DeleteQbtTag(ctx, tag)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("delete tag: %w", err)
		}
		changed = changed || removed > 0
	}
	downloads, err := queries.ListAllVisibleDownloads(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("list downloads for tag deletion: %w", err)
	}
	remove := make(map[string]struct{}, len(list))
	for _, tag := range list {
		remove[tag] = struct{}{}
	}
	for _, row := range downloads {
		current := normalizeStoredTags(row.Tags)
		parts := strings.Split(current, ",")
		kept := parts[:0]
		for _, tag := range parts {
			if _, drop := remove[tag]; drop {
				changed = true
				continue
			}
			kept = append(kept, tag)
		}
		next := strings.Join(kept, ",")
		if next == current {
			continue
		}
		if _, err := queries.UpdateDownloadTags(ctx, storedb.UpdateDownloadTagsParams{Tags: next, UpdatedAt: now, Hash: row.Hash}); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("clear deleted tag: %w", err)
		}
	}
	return s.commitMutationTx(tx, changed)
}

// RemoveTags removes selected tags from selected non-deleted downloads. An
// empty tags string removes every assigned tag.
func (s *Store) RemoveTags(ctx context.Context, hashes []string, tags string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTransition
	}
	list, err := canonicalQBTTagList(tags, false)
	if err != nil {
		return err
	}
	selected := uniqueHashes(hashes)
	if len(selected) == 0 {
		return nil
	}
	removeAll := len(list) == 0
	remove := make(map[string]struct{}, len(list))
	for _, tag := range list {
		remove[tag] = struct{}{}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remove tags: %w", err)
	}
	queries := s.queries.WithTx(tx)
	changed := false
	for _, hash := range selected {
		row, getErr := queries.GetDownload(ctx, hash)
		if errors.Is(getErr, sql.ErrNoRows) {
			continue
		}
		if getErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read tags download: %w", getErr)
		}
		if row.State == string(domain.StateDeleted) {
			continue
		}
		current := normalizeStoredTags(row.Tags)
		if current == "" {
			continue
		}
		kept := make([]string, 0)
		for _, tag := range strings.Split(current, ",") {
			if removeAll {
				changed = true
				continue
			}
			if _, drop := remove[tag]; drop {
				changed = true
				continue
			}
			kept = append(kept, tag)
		}
		next := strings.Join(kept, ",")
		if next != current {
			if _, updateErr := queries.UpdateDownloadTags(ctx, storedb.UpdateDownloadTagsParams{Tags: next, UpdatedAt: now, Hash: hash}); updateErr != nil {
				_ = tx.Rollback()
				return fmt.Errorf("remove tags: %w", updateErr)
			}
		}
	}
	return s.commitMutationTx(tx, changed)
}

// RemoveCategories deletes categories and clears their labels from live
// downloads without changing any frozen physical paths.
func (s *Store) RemoveCategories(ctx context.Context, categories []string, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTransition
	}
	names := uniqueNonEmpty(categories)
	if len(names) == 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin remove categories: %w", err)
	}
	queries := s.queries.WithTx(tx)
	rows, err := queries.ListAllVisibleDownloads(ctx)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("list category downloads: %w", err)
	}
	if _, err := queries.DeleteQbtCategories(ctx, names); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("delete categories: %w", err)
	}
	if _, err := queries.ClearDownloadCategories(ctx, storedb.ClearDownloadCategoriesParams{UpdatedAt: now, Names: names}); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("clear categories: %w", err)
	}
	changed := false
	removed := make(map[string]struct{}, len(names))
	for _, name := range names {
		removed[name] = struct{}{}
	}
	for _, row := range rows {
		if _, ok := removed[row.Category]; !ok {
			continue
		}
		changed = true
		afterRow, getErr := queries.GetDownload(ctx, row.Hash)
		if getErr != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read cleared category: %w", getErr)
		}
		after, convErr := downloadFromDB(afterRow)
		if convErr != nil {
			_ = tx.Rollback()
			return convErr
		}
		if err := emitDownloadEvent(ctx, queries, outbox.EventTypeCategoryChanged, domain.State(row.State), after); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	return s.commitEventTx(tx, changed)
}

// RenameDownload updates only the qB-visible display name and override bit.
func (s *Store) RenameDownload(ctx context.Context, hash, name string, now time.Time) error {
	if hash == "" || !safeQBTName(name) || now.IsZero() {
		return ErrInvalidTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rename: %w", err)
	}
	queries := s.queries.WithTx(tx)
	before, err := queries.GetDownload(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read rename download: %w", err)
	}
	if before.State == string(domain.StateDeleted) {
		_ = tx.Rollback()
		return ErrInvalidTransition
	}
	if before.Name == name && before.NameOverridden == 1 {
		return tx.Commit()
	}
	updated, err := queries.SetDownloadName(ctx, storedb.SetDownloadNameParams{Name: name, UpdatedAt: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("rename download: %w", err)
	}
	if updated != 1 {
		_ = tx.Rollback()
		return ErrInvalidTransition
	}
	return s.commitMutationTx(tx, true)
}

// RenameFolder rewrites only matching file overrides for one pre-start torrent.
func (s *Store) RenameFolder(ctx context.Context, hash, oldPath, newPath string, now time.Time) error {
	if hash == "" || !safeRelativePrefix(oldPath) || !safeRelativePrefix(newPath) || now.IsZero() {
		return ErrInvalidTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin folder rename: %w", err)
	}
	queries := s.queries.WithTx(tx)
	row, err := queries.GetDownload(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read folder rename download: %w", err)
	}
	if row.State != string(domain.StateStopped) && row.State != string(domain.StateAccepted) || row.LeaseOwner.Valid || row.ContentPath.Valid || row.CopySourcePath.Valid || row.CloudResultPath.Valid {
		_ = tx.Rollback()
		return ErrInvalidTransition
	}
	manifest, err := queries.ListDownloadFiles(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	overrides, err := queries.ListDownloadFileOverrides(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	type effectiveFile struct {
		index int64
		path  string
		prio  int64
	}
	effective := make([]effectiveFile, 0, len(manifest))
	for _, file := range manifest {
		entry := effectiveFile{index: file.FileIndex, path: file.RelativePath, prio: 1}
		for _, override := range overrides {
			if override.FileIndex == file.FileIndex {
				entry.path, entry.prio = override.RelativePath, override.Priority
				break
			}
		}
		effective = append(effective, entry)
	}
	changed := false
	for index := range effective {
		if effective[index].path == oldPath {
			effective[index].path = newPath
			changed = true
		} else if strings.HasPrefix(effective[index].path, oldPath+"/") {
			effective[index].path = newPath + effective[index].path[len(oldPath):]
			changed = true
		}
	}
	if !changed {
		_ = tx.Rollback()
		return ErrNotFound
	}
	for i := range effective {
		for j := i + 1; j < len(effective); j++ {
			if pathConflicts(effective[i].path, effective[j].path) {
				_ = tx.Rollback()
				return ErrDestinationConflict
			}
		}
	}
	for _, file := range effective {
		old := ""
		for _, manifestFile := range manifest {
			if manifestFile.FileIndex == file.index {
				old = manifestFile.RelativePath
				break
			}
		}
		for _, override := range overrides {
			if override.FileIndex == file.index {
				old = override.RelativePath
				break
			}
		}
		if old == file.path {
			continue
		}
		if err := queries.UpsertDownloadFileOverride(ctx, storedb.UpsertDownloadFileOverrideParams{DownloadHash: hash, FileIndex: file.index, RelativePath: file.path, Priority: file.prio}); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("write folder override: %w", err)
		}
	}
	if _, err := queries.TouchDownload(ctx, storedb.TouchDownloadParams{UpdatedAt: now, Hash: hash}); err != nil {
		_ = tx.Rollback()
		return err
	}
	return s.commitMutationTx(tx, true)
}

// Reverify schedules local verification for a completed download. Other
// states are intentionally idempotent success, matching qB's mutation rules.
func (s *Store) Reverify(ctx context.Context, hash string, now time.Time) error {
	if hash == "" || now.IsZero() {
		return ErrInvalidTransition
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin reverify: %w", err)
	}
	queries := s.queries.WithTx(tx)
	before, err := queries.GetDownload(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return ErrNotFound
	}
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read reverify download: %w", err)
	}
	if before.State != string(domain.StateCompleted) {
		return tx.Commit()
	}
	updated, err := queries.ReverifyDownload(ctx, storedb.ReverifyDownloadParams{Now: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("reverify download: %w", err)
	}
	if updated != 1 {
		_ = tx.Rollback()
		return ErrInvalidTransition
	}
	afterRow, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read reverified download: %w", err)
	}
	after, err := downloadFromDB(afterRow)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, domain.StateCompleted, after); err != nil {
		_ = tx.Rollback()
		return err
	}
	return s.commitEventTx(tx, true)
}

func canonicalQBTTagList(raw string, required bool) ([]string, error) {
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if strings.ContainsAny(part, ",\x00") || strings.ContainsFunc(part, func(r rune) bool { return unicode.IsControl(r) }) {
			return nil, ErrInvalidTransition
		}
		if _, exists := seen[part]; exists {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	if required && len(result) == 0 {
		return nil, ErrInvalidTransition
	}
	return result, nil
}

func uniqueHashes(hashes []string) []string {
	seen := make(map[string]struct{}, len(hashes))
	result := make([]string, 0, len(hashes))
	for _, hash := range hashes {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if hash == "" {
			continue
		}
		if _, ok := seen[hash]; ok {
			continue
		}
		seen[hash] = struct{}{}
		result = append(result, hash)
	}
	return result
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func safeQBTName(value string) bool {
	return value != "" && value != "." && value != ".." && !filepath.IsAbs(value) &&
		filepath.Clean(value) == value && !strings.ContainsAny(value, "/\\") &&
		!strings.ContainsRune(value, 0) && !strings.ContainsFunc(value, unicode.IsControl)
}

func safeRelativePrefix(value string) bool {
	if value == "" || value == "." || filepath.IsAbs(value) || filepath.Clean(value) != value ||
		strings.ContainsAny(value, "\\") || strings.ContainsRune(value, 0) {
		return false
	}
	for _, part := range strings.Split(value, string(filepath.Separator)) {
		if part == "" || part == "." || part == ".." || strings.ContainsFunc(part, unicode.IsControl) {
			return false
		}
	}
	return true
}
