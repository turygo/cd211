package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/outbox"
	storedb "github.com/turygo/cd211/internal/store/sqlc"
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib"
)

var (
	ErrNotFound            = errors.New("repository record not found")
	ErrClaimLost           = errors.New("repository claim lost")
	ErrDestinationConflict = errors.New("repository destination conflict")
	ErrInvalidTransition   = errors.New("repository invalid transition")
)

// Claim is a leased download together with the identity required to commit it.
type Claim struct {
	Download domain.Download
	Owner    string
	State    domain.State
	Version  int64
}

// UpsertCategory creates or updates a category without changing its creation time.
func (s *Store) UpsertCategory(ctx context.Context, category domain.Category) (domain.Category, error) {
	if err := validateCategory(category); err != nil {
		return domain.Category{}, err
	}

	enabled := int64(0)
	if category.Enabled {
		enabled = 1
	}
	row, err := s.queries.UpsertCategory(ctx, storedb.UpsertCategoryParams{
		Name: category.Name, CloudPath: category.CloudPath, SavePath: category.SavePath,
		Enabled: enabled, CreatedAt: category.CreatedAt, UpdatedAt: category.UpdatedAt,
	})
	if err != nil {
		if destinationConstraint(err) {
			return domain.Category{}, ErrDestinationConflict
		}
		return domain.Category{}, fmt.Errorf("upsert category: %w", err)
	}
	return categoryFromDB(row)
}

// GetCategory returns a category by its exact name.
func (s *Store) GetCategory(ctx context.Context, name string) (domain.Category, error) {
	row, err := s.queries.GetCategory(ctx, name)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Category{}, ErrNotFound
	}
	if err != nil {
		return domain.Category{}, fmt.Errorf("get category: %w", err)
	}
	return categoryFromDB(row)
}

// ListCategories returns all categories ordered by name.
func (s *Store) ListCategories(ctx context.Context) ([]domain.Category, error) {
	rows, err := s.queries.ListCategories(ctx)
	if err != nil {
		return nil, fmt.Errorf("list categories: %w", err)
	}
	categories := make([]domain.Category, 0, len(rows))
	for _, row := range rows {
		category, err := categoryFromDB(row)
		if err != nil {
			return nil, err
		}
		categories = append(categories, category)
	}
	return categories, nil
}

// CreateSubmission atomically creates a download and its files, or revives a deleted one.
func (s *Store) CreateSubmission(ctx context.Context, submission domain.Submission) (domain.Download, bool, error) {
	if err := validateSubmission(submission); err != nil {
		return domain.Download{}, false, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Download{}, false, fmt.Errorf("begin submission: %w", err)
	}
	queries := s.queries.WithTx(tx)
	finish := func(cause error) (domain.Download, bool, error) {
		if rollbackErr := tx.Rollback(); rollbackErr != nil && !errors.Is(rollbackErr, sql.ErrTxDone) {
			return domain.Download{}, false, fmt.Errorf("rollback submission: %w", rollbackErr)
		}
		return domain.Download{}, false, cause
	}

	// previousState distinguishes a fresh lifecycle (empty) from a revival of
	// a DELETED row when serializing the created event payload.
	previousState := domain.State("")
	existing, err := queries.GetDownload(ctx, submission.Download.Hash)
	if err == nil {
		download, conversionErr := downloadFromDB(existing)
		if conversionErr != nil {
			return finish(conversionErr)
		}
		if download.State != domain.StateDeleted {
			if err := tx.Commit(); err != nil {
				return domain.Download{}, false, fmt.Errorf("commit duplicate submission: %w", err)
			}
			return download, false, nil
		}
		previousState = domain.StateDeleted
		if err := queries.ReviveDownload(ctx, reviveDownloadParams(submission.Download)); err != nil {
			return finish(fmt.Errorf("revive submission: %w", err))
		}
		if err := queries.DeleteDownloadFiles(ctx, submission.Download.Hash); err != nil {
			return finish(fmt.Errorf("replace submission files: %w", err))
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return finish(fmt.Errorf("read existing submission: %w", err))
	} else if insertErr := queries.InsertDownload(ctx, insertDownloadParams(submission.Download)); insertErr != nil {
		winner, winnerErr := queries.GetDownload(ctx, submission.Download.Hash)
		if winnerErr == nil {
			download, conversionErr := downloadFromDB(winner)
			if conversionErr != nil {
				return finish(conversionErr)
			}
			if download.State != domain.StateDeleted {
				if err := tx.Commit(); err != nil {
					return domain.Download{}, false, fmt.Errorf("commit duplicate submission: %w", err)
				}
				return download, false, nil
			}
		}
		return finish(fmt.Errorf("insert submission: %w", insertErr))
	}

	for _, file := range submission.Files {
		if err := queries.InsertDownloadFile(ctx, storedb.InsertDownloadFileParams{
			DownloadHash: file.DownloadHash, FileIndex: file.Index, RelativePath: file.RelativePath, Size: file.Size,
		}); err != nil {
			return finish(fmt.Errorf("insert submission file: %w", err))
		}
	}
	row, err := queries.GetDownload(ctx, submission.Download.Hash)
	if err != nil {
		return finish(fmt.Errorf("read created submission: %w", err))
	}
	download, err := downloadFromDB(row)
	if err != nil {
		return finish(err)
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeCreated, previousState, download); err != nil {
		return finish(err)
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return domain.Download{}, false, fmt.Errorf("commit submission: %w", err)
	}
	return download, true, nil
}

// GetDownload returns a download by hash, including removed downloads.
func (s *Store) GetDownload(ctx context.Context, hash string) (domain.Download, error) {
	row, err := s.queries.GetDownload(ctx, hash)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Download{}, ErrNotFound
	}
	if err != nil {
		return domain.Download{}, fmt.Errorf("get download: %w", err)
	}
	return downloadFromDB(row)
}

// ListDownloads returns qBittorrent-visible downloads and blocked deletion cleanup, optionally limited to an exact category.
func (s *Store) ListDownloads(ctx context.Context, category *string) ([]domain.Download, error) {
	var (
		rows []storedb.Download
		err  error
	)
	if category == nil {
		rows, err = s.queries.ListAllVisibleDownloads(ctx)
	} else {
		rows, err = s.queries.ListVisibleDownloads(ctx, *category)
	}
	if err != nil {
		return nil, fmt.Errorf("list downloads: %w", err)
	}
	downloads := make([]domain.Download, 0, len(rows))
	for _, row := range rows {
		download, err := downloadFromDB(row)
		if err != nil {
			return nil, err
		}
		downloads = append(downloads, download)
	}
	return downloads, nil
}

// NextDue returns the earliest time at which an eligible workflow item can run.
func (s *Store) NextDue(ctx context.Context, now time.Time) (*time.Time, error) {
	if now.IsZero() {
		return nil, errors.New("due time is required")
	}
	row, err := s.queries.NextDue(ctx, sql.NullTime{Time: now.UTC(), Valid: true})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("get next due: %w", err)
	}
	if !row.Valid {
		return nil, nil
	}
	if row.Time.IsZero() {
		return nil, errors.New("stored next due is invalid")
	}
	due := row.Time.UTC()
	return &due, nil
}

// ListDownloadFiles returns a download's files in index order.
func (s *Store) ListDownloadFiles(ctx context.Context, hash string) ([]domain.DownloadFile, error) {
	rows, err := s.queries.ListDownloadFiles(ctx, hash)
	if err != nil {
		return nil, fmt.Errorf("list download files: %w", err)
	}
	files := make([]domain.DownloadFile, 0, len(rows))
	for _, row := range rows {
		if row.FileIndex < 0 || row.Size < 0 {
			return nil, errors.New("stored download file is invalid")
		}
		files = append(files, domain.DownloadFile{
			DownloadHash: row.DownloadHash, Index: row.FileIndex, RelativePath: row.RelativePath, Size: row.Size,
		})
	}
	return files, nil
}

// SetCategory changes only a download's visible category label. A same-value
// call succeeds without changing the row or emitting an event; a real change
// emits download.category_changed in the same transaction.
func (s *Store) SetCategory(ctx context.Context, hash, category string, now time.Time) error {
	if (category != "" && !safeCategoryName(category)) || now.IsZero() {
		return errors.New("category or update time is invalid")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin category update: %w", err)
	}
	queries := s.queries.WithTx(tx)
	before, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read category download: %w", err)
	}
	if before.Category == category {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit same-category update: %w", err)
		}
		return nil
	}
	updated, err := queries.SetDownloadCategory(ctx, storedb.SetDownloadCategoryParams{Category: category, UpdatedAt: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("set category: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return s.intentMiss(ctx, hash, domain.StateAccepted)
	}
	row, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read updated category download: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeCategoryChanged, download.State, download); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return fmt.Errorf("commit category update: %w", err)
	}
	return nil
}

// Start schedules a stopped download and is idempotent while that start is
// entering its first resumable phase. Only a real STOPPED ->
// ACCEPTED|SUBMITTING_COPY|VERIFYING_LOCAL transition emits
// download.state_changed.
func (s *Store) Start(ctx context.Context, hash string, now time.Time) error {
	if now.IsZero() {
		return errors.New("start time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin start: %w", err)
	}
	queries := s.queries.WithTx(tx)
	updated, err := queries.StartDownload(ctx, storedb.StartDownloadParams{Now: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("start download: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		download, getErr := s.GetDownload(ctx, hash)
		if getErr != nil {
			return getErr
		}
		if download.State == domain.StateAccepted || download.State == domain.StateSubmittingCopy ||
			download.State == domain.StateVerifyingLocal {
			return nil
		}
		return ErrInvalidTransition
	}
	row, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read started download: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, domain.StateStopped, download); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return fmt.Errorf("commit start: %w", err)
	}
	return nil
}

// Retry resumes a failed workflow or a cleanup intent blocked by operator
// action. A real FAILED -> retry target transition emits
// download.state_changed; a cleanup retry that only clears an error without
// changing state emits nothing.
func (s *Store) Retry(ctx context.Context, hash string, target domain.State, now time.Time) error {
	if now.IsZero() {
		return ErrInvalidTransition
	}
	cleanup := target == domain.StateCancelRequested || target == domain.StateDeleteRequested
	if !cleanup {
		if !domain.CanTransition(domain.StateFailed, target) || target == domain.StateDeleteRequested {
			return ErrInvalidTransition
		}
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin retry: %w", err)
	}
	queries := s.queries.WithTx(tx)
	var (
		updated int64
		runErr  error
	)
	if cleanup {
		updated, runErr = queries.RetryCleanup(ctx, storedb.RetryCleanupParams{Now: now, Hash: hash})
	} else {
		updated, runErr = queries.RetryDownload(ctx, storedb.RetryDownloadParams{State: string(target), Now: now, Hash: hash})
	}
	if runErr != nil {
		_ = tx.Rollback()
		return fmt.Errorf("retry download: %w", runErr)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return s.intentMiss(ctx, hash, "")
	}
	row, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read retried download: %w", err)
	}
	// The retry transition is pre-authorized by CanTransition and the row was
	// valid in its pre-update state; phase prerequisites such as
	// cloud_source_path are materialized when the workflow runs the target
	// phase, so the event payload is built from a structurally converted row
	// rather than one gated by full domain validation.
	download, err := downloadRow(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if !cleanup {
		if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, domain.StateFailed, download); err != nil {
			_ = tx.Rollback()
			return err
		}
	}
	if err := s.commitEventTx(tx, !cleanup); err != nil {
		return fmt.Errorf("commit retry: %w", err)
	}
	return nil
}

// Pause requests cleanup of active upstream work while retaining enough
// evidence to resume the workflow from its last completed stage.
func (s *Store) Pause(ctx context.Context, hash string, now time.Time) error {
	if now.IsZero() {
		return errors.New("pause time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin pause: %w", err)
	}
	queries := s.queries.WithTx(tx)
	beforeRow, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read pause download: %w", err)
	}
	before, err := downloadFromDB(beforeRow)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	updated, err := queries.PauseDownload(ctx, storedb.PauseDownloadParams{Now: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("pause download: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		row, getErr := s.GetDownload(ctx, hash)
		if getErr != nil {
			return getErr
		}
		if row.State == domain.StateCancelRequested && row.PauseRequested {
			return nil
		}
		return ErrInvalidTransition
	}
	row, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read paused download: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, before.State, download); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return fmt.Errorf("commit pause: %w", err)
	}
	return nil
}

// Cancel requests cancellation of active or stopped downloads. A real
// transition to CANCEL_REQUESTED emits download.state_changed; idempotent
// calls emit nothing.
func (s *Store) Cancel(ctx context.Context, hash string, now time.Time) error {
	if now.IsZero() {
		return errors.New("cancel time is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin cancel: %w", err)
	}
	queries := s.queries.WithTx(tx)
	beforeRow, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("read cancel download: %w", err)
	}
	before, err := downloadFromDB(beforeRow)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	updated, err := queries.CancelDownload(ctx, storedb.CancelDownloadParams{Now: now, Hash: hash})
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("cancel download: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		row, err := s.GetDownload(ctx, hash)
		if err != nil {
			return err
		}
		if row.State == domain.StateCancelRequested || row.State == domain.StateCancelled {
			return nil
		}
		return ErrInvalidTransition
	}
	row, err := queries.GetDownload(ctx, hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read cancelled download: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, before.State, download); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := s.commitEventTx(tx, true); err != nil {
		return fmt.Errorf("commit cancel: %w", err)
	}
	return nil
}

// RequestDelete hides matching downloads and records the strongest delete-files
// intent. A real transition to DELETE_REQUESTED emits download.state_changed;
// strengthening delete_files_requested on an already requested delete emits
// nothing. Unknown hashes are skipped silently.
func (s *Store) RequestDelete(ctx context.Context, hashes []string, deleteFiles bool, now time.Time) error {
	if now.IsZero() {
		return errors.New("delete time is required")
	}
	unique := make(map[string]struct{}, len(hashes))
	for _, hash := range hashes {
		hash = strings.ToLower(strings.TrimSpace(hash))
		if hash != "" {
			unique[hash] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete request: %w", err)
	}
	queries := s.queries.WithTx(tx)
	emitted := false
	for hash := range unique {
		beforeRow, err := queries.GetDownload(ctx, hash)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("request delete read: %w", err)
		}
		before, err := downloadFromDB(beforeRow)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if _, err := queries.RequestDelete(ctx, storedb.RequestDeleteParams{DeleteFilesRequested: boolInteger(deleteFiles), Now: now, Hash: hash}); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("request delete: %w", err)
		}
		if before.State == domain.StateDeleteRequested || before.State == domain.StateDeleted {
			continue
		}
		row, err := queries.GetDownload(ctx, hash)
		if err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("read delete request: %w", err)
		}
		download, err := downloadFromDB(row)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := emitDownloadEvent(ctx, queries, outbox.EventTypeStateChanged, before.State, download); err != nil {
			_ = tx.Rollback()
			return err
		}
		emitted = true
	}
	if err := s.commitEventTx(tx, emitted); err != nil {
		return fmt.Errorf("commit delete request: %w", err)
	}
	return nil
}

// ClaimDue atomically leases the earliest due unleased workflow item.
func (s *Store) ClaimDue(ctx context.Context, owner string, now time.Time, leaseDuration time.Duration) (*Claim, error) {
	if strings.TrimSpace(owner) == "" || leaseDuration <= 0 || now.IsZero() {
		return nil, errors.New("claim owner, time, or lease duration is invalid")
	}
	now = now.UTC()
	leaseUntil := now.Add(leaseDuration).UTC()
	row, err := s.queries.ClaimDue(ctx, storedb.ClaimDueParams{
		Owner:      sql.NullString{String: owner, Valid: true},
		LeaseUntil: sql.NullTime{Time: leaseUntil, Valid: true},
		Now:        sql.NullTime{Time: now, Valid: true},
	})
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("claim due download: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		return nil, err
	}
	return &Claim{Download: download, Owner: owner, State: download.State, Version: download.RowVersion}, nil
}

// CommitClaim persists a claimed workflow result only if the lease is still
// current, atomically with any terminal/state event and its fanout: entering
// COMPLETED emits download.completed, entering FAILED emits download.failed,
// and every other real state transition emits download.state_changed.
// Same-state progress, retry bookkeeping, and lease changes emit nothing.
func (s *Store) CommitClaim(ctx context.Context, claim Claim, next domain.Download) error {
	if strings.TrimSpace(claim.Owner) == "" || claim.Version < 0 || claim.Download.Hash == "" || !claim.State.Valid() || claim.Download.State != claim.State {
		return ErrClaimLost
	}
	if !sameClaimIdentity(claim.Download, next) || (next.State != claim.State && !domain.CanTransition(claim.State, next.State)) {
		return ErrInvalidTransition
	}
	next.LeaseOwner = ""
	next.LeaseUntil = nil
	if err := domain.ValidateDownload(next); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin claim commit: %w", err)
	}
	queries := s.queries.WithTx(tx)
	updated, err := queries.CommitClaim(ctx, commitClaimParams(claim, next))
	if err != nil {
		_ = tx.Rollback()
		if destinationConstraint(err) && next.DestinationName != "" {
			return ErrDestinationConflict
		}
		return fmt.Errorf("commit claim: %w", err)
	}
	if updated == 0 {
		_ = tx.Rollback()
		return ErrClaimLost
	}
	row, err := queries.GetDownload(ctx, next.Hash)
	if err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("read committed claim: %w", err)
	}
	download, err := downloadFromDB(row)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	emitted := false
	if download.State != claim.State {
		eventType := outbox.EventTypeStateChanged
		switch download.State {
		case domain.StateCompleted:
			eventType = outbox.EventTypeCompleted
		case domain.StateFailed:
			eventType = outbox.EventTypeFailed
		}
		if err := emitDownloadEvent(ctx, queries, eventType, claim.State, download); err != nil {
			_ = tx.Rollback()
			return err
		}
		emitted = true
	}
	if err := s.commitEventTx(tx, emitted); err != nil {
		return fmt.Errorf("commit claim transaction: %w", err)
	}
	return nil
}

func (s *Store) intentMiss(ctx context.Context, hash string, idempotent domain.State) error {
	download, err := s.GetDownload(ctx, hash)
	if err != nil {
		return err
	}
	if idempotent != "" && download.State == idempotent {
		return nil
	}
	return ErrInvalidTransition
}

func validateCategory(category domain.Category) error {
	if !safeCategoryName(category.Name) || !absolutePath(category.CloudPath) || !absolutePath(category.SavePath) || category.CreatedAt.IsZero() || category.UpdatedAt.IsZero() || category.UpdatedAt.Before(category.CreatedAt) {
		return errors.New("category is invalid")
	}
	return nil
}

func validateSubmission(submission domain.Submission) error {
	download := submission.Download
	if err := domain.ValidateDownload(download); err != nil {
		return err
	}
	if download.State != domain.StateAccepted && download.State != domain.StateStopped && download.State != domain.StateVerifyingLocal {
		return ErrInvalidTransition
	}
	if download.LeaseOwner != "" || download.LeaseUntil != nil {
		return errors.New("initial submission must not have a lease")
	}
	for index, file := range submission.Files {
		if file.DownloadHash != download.Hash || file.Index != int64(index) || file.Size < 0 {
			return errors.New("submission files are invalid")
		}
	}
	return nil
}

func categoryFromDB(row storedb.Category) (domain.Category, error) {
	if row.Enabled != 0 && row.Enabled != 1 || !safeCategoryName(row.Name) || !absolutePath(row.CloudPath) || !absolutePath(row.SavePath) || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) {
		return domain.Category{}, errors.New("stored category is invalid")
	}
	return domain.Category{Name: row.Name, CloudPath: row.CloudPath, SavePath: row.SavePath, Enabled: row.Enabled == 1, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt}, nil
}

// downloadRow maps a stored download row to a domain.Download after checking
// structural integrity only. It deliberately skips domain.ValidateDownload:
// phase-prerequisite fields (for example cloud_source_path for copy states)
// are materialized by the workflow that runs the phase, so authorized
// transitions such as Retry can observe rows that are momentarily mid-phase.
// Callers that need the full domain invariants must use downloadFromDB.
func downloadRow(row storedb.Download) (domain.Download, error) {
	if row.IsMultiFile.Valid && row.IsMultiFile.Int64 != 0 && row.IsMultiFile.Int64 != 1 ||
		row.DeleteFilesRequested != 0 && row.DeleteFilesRequested != 1 ||
		row.PauseRequested != 0 && row.PauseRequested != 1 {
		return domain.Download{}, errors.New("stored download boolean is invalid")
	}
	sourceKind := domain.SourceKind(row.SourceKind)
	state := domain.State(row.State)
	if !sourceKind.Valid() || !state.Valid() || row.PhaseStartedAt.IsZero() || row.CreatedAt.IsZero() || row.UpdatedAt.IsZero() || row.UpdatedAt.Before(row.CreatedAt) || !validNullableTime(row.NextRunAt) || !validNullableTime(row.LeaseUntil) || !validNullableTime(row.CompletedAt) || !validNullableTime(row.RemovedAt) {
		return domain.Download{}, errors.New("stored download is invalid")
	}
	download := domain.Download{
		Hash: row.Hash, Name: row.Name, SourceKind: sourceKind, SubmissionURI: row.SubmissionUri,
		Category: row.Category, CloudFolder: row.CloudFolder, SavePath: row.SavePath, DestinationName: nullString(row.DestinationName),
		CloudTaskName: nullString(row.CloudTaskName), CloudSourcePath: nullString(row.CloudSourcePath), ContentPath: nullString(row.ContentPath),
		TotalSize: row.TotalSize, State: state, OfflineProgress: row.OfflineProgress, CopyProgress: row.CopyProgress, QbitProgress: row.QbitProgress,
		LastUpstreamStatus: nullString(row.LastUpstreamStatus), LastError: nullString(row.LastError),
		PhaseStartedAt: row.PhaseStartedAt, NextRunAt: nullTime(row.NextRunAt), LeaseUntil: nullTime(row.LeaseUntil), LeaseOwner: nullString(row.LeaseOwner),
		AttemptCount: row.AttemptCount, DeleteFilesRequested: row.DeleteFilesRequested == 1, PauseRequested: row.PauseRequested == 1,
		CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CompletedAt: nullTime(row.CompletedAt), RemovedAt: nullTime(row.RemovedAt), RowVersion: row.RowVersion,
	}
	if row.IsMultiFile.Valid {
		value := row.IsMultiFile.Int64 == 1
		download.IsMultiFile = &value
	}
	return download, nil
}

func downloadFromDB(row storedb.Download) (domain.Download, error) {
	download, err := downloadRow(row)
	if err != nil {
		return domain.Download{}, err
	}
	if err := domain.ValidateDownload(download); err != nil {
		return domain.Download{}, fmt.Errorf("stored download is invalid: %w", err)
	}
	return download, nil
}

func insertDownloadParams(download domain.Download) storedb.InsertDownloadParams {
	return storedb.InsertDownloadParams{
		Hash: download.Hash, Name: download.Name, SourceKind: string(download.SourceKind), SubmissionUri: download.SubmissionURI,
		Category: download.Category, CloudFolder: download.CloudFolder, SavePath: download.SavePath, DestinationName: nullableString(download.DestinationName),
		CloudTaskName: nullableString(download.CloudTaskName), CloudSourcePath: nullableString(download.CloudSourcePath), ContentPath: nullableString(download.ContentPath),
		IsMultiFile: nullableBool(download.IsMultiFile), TotalSize: download.TotalSize, State: string(download.State),
		OfflineProgress: download.OfflineProgress, CopyProgress: download.CopyProgress, QbitProgress: download.QbitProgress,
		LastUpstreamStatus: nullableString(download.LastUpstreamStatus), LastError: nullableString(download.LastError),
		PhaseStartedAt: download.PhaseStartedAt, NextRunAt: nullableTime(download.NextRunAt), LeaseUntil: nullableTime(download.LeaseUntil), LeaseOwner: nullableString(download.LeaseOwner),
		AttemptCount: download.AttemptCount, DeleteFilesRequested: boolInteger(download.DeleteFilesRequested),
		CreatedAt: download.CreatedAt, UpdatedAt: download.UpdatedAt, CompletedAt: nullableTime(download.CompletedAt), RemovedAt: nullableTime(download.RemovedAt), RowVersion: download.RowVersion,
	}
}

func reviveDownloadParams(download domain.Download) storedb.ReviveDownloadParams {
	return storedb.ReviveDownloadParams{
		Name: download.Name, SourceKind: string(download.SourceKind), SubmissionUri: download.SubmissionURI,
		Category: download.Category, CloudFolder: download.CloudFolder, SavePath: download.SavePath, DestinationName: nullableString(download.DestinationName),
		CloudTaskName: nullableString(download.CloudTaskName), CloudSourcePath: nullableString(download.CloudSourcePath), ContentPath: nullableString(download.ContentPath),
		IsMultiFile: nullableBool(download.IsMultiFile), TotalSize: download.TotalSize, State: string(download.State),
		OfflineProgress: download.OfflineProgress, CopyProgress: download.CopyProgress, QbitProgress: download.QbitProgress, LastUpstreamStatus: nullableString(download.LastUpstreamStatus),
		PhaseStartedAt: download.PhaseStartedAt, NextRunAt: nullableTime(download.NextRunAt), CreatedAt: download.CreatedAt, UpdatedAt: download.UpdatedAt, Hash: download.Hash,
	}
}

func commitClaimParams(claim Claim, download domain.Download) storedb.CommitClaimParams {
	return storedb.CommitClaimParams{
		Name:            download.Name,
		DestinationName: nullableString(download.DestinationName),
		CloudTaskName:   nullableString(download.CloudTaskName), CloudSourcePath: nullableString(download.CloudSourcePath), ContentPath: nullableString(download.ContentPath),
		IsMultiFile: nullableBool(download.IsMultiFile), TotalSize: download.TotalSize, State: string(download.State),
		OfflineProgress: download.OfflineProgress, CopyProgress: download.CopyProgress, QbitProgress: download.QbitProgress,
		LastUpstreamStatus: nullableString(download.LastUpstreamStatus), LastError: nullableString(download.LastError),
		PhaseStartedAt: download.PhaseStartedAt, NextRunAt: nullableTime(download.NextRunAt), AttemptCount: download.AttemptCount,
		DeleteFilesRequested: boolInteger(download.DeleteFilesRequested), PauseRequested: boolInteger(download.PauseRequested),
		UpdatedAt: download.UpdatedAt, CompletedAt: nullableTime(download.CompletedAt), RemovedAt: nullableTime(download.RemovedAt),
		Hash: claim.Download.Hash, ExpectedState: string(claim.State), LeaseOwner: sql.NullString{String: claim.Owner, Valid: true}, ExpectedRowVersion: claim.Version,
	}
}

func sameClaimIdentity(current, next domain.Download) bool {
	return current.Hash == next.Hash && current.SourceKind == next.SourceKind && current.SubmissionURI == next.SubmissionURI && current.CloudFolder == next.CloudFolder && current.SavePath == next.SavePath && current.CreatedAt.Equal(next.CreatedAt)
}

func safeCategoryName(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) || filepath.IsAbs(value) {
		return false
	}
	for _, character := range value {
		if character == '/' || character == '\\' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func destinationConstraint(err error) bool {
	var sqliteErr *sqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	return sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE ||
		sqliteErr.Code() == sqlite3.SQLITE_CONSTRAINT_TRIGGER
}

func absolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path)
}

func nullableString(value string) sql.NullString {
	return sql.NullString{String: value, Valid: value != ""}
}

func nullString(value sql.NullString) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func nullableTime(value *time.Time) sql.NullTime {
	if value == nil {
		return sql.NullTime{}
	}
	return sql.NullTime{Time: *value, Valid: true}
}

func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	copy := value.Time
	return &copy
}

func validNullableTime(value sql.NullTime) bool {
	return !value.Valid || !value.Time.IsZero()
}

func nullableBool(value *bool) sql.NullInt64 {
	if value == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: boolInteger(*value), Valid: true}
}

func boolInteger(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
