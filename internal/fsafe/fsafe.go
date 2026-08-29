// Package fsafe validates and removes torrent content beneath a configured root.
package fsafe

import (
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExpectedFile is one authoritative torrent manifest entry.
type ExpectedFile struct {
	RelativePath string
	Size         int64
}

// ExpectedContent describes the staged candidate and its logical torrent manifest.
type ExpectedContent struct {
	CandidateName string
	MultiFile     bool
	Files         []ExpectedFile
}

// VerifiedContent is the checked content path together with the number of bytes
// actually on disk. CloudDrive2 reports zero bytes for a directory, and a magnet
// submission carries no metadata at all, so the staged content is the only
// trustworthy source for a torrent's size.
type VerifiedContent struct {
	Path string
	Size int64
}

// Verifier validates torrent content beneath localRoot.
type Verifier struct {
	localRoot string
}

// New creates a verifier rooted at an existing absolute directory.
func New(localRoot string) (*Verifier, error) {
	if !filepath.IsAbs(localRoot) {
		return nil, fmt.Errorf("fsafe: local root must be absolute")
	}
	configuredRoot := filepath.Clean(localRoot)
	if err := rejectReservedComponent(configuredRoot); err != nil {
		return nil, err
	}
	evaluatedRoot, err := filepath.EvalSymlinks(configuredRoot)
	if err != nil {
		return nil, fmt.Errorf("fsafe: resolve local root: %w", err)
	}
	evaluatedRoot = filepath.Clean(evaluatedRoot)
	if err := rejectReservedComponent(evaluatedRoot); err != nil {
		return nil, err
	}

	info, err := os.Stat(evaluatedRoot)
	if err != nil {
		return nil, fmt.Errorf("fsafe: inspect local root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fsafe: local root is not a directory")
	}

	return &Verifier{localRoot: evaluatedRoot}, nil
}

// LocalRoot returns the evaluated root used for every durable save path.
func (v *Verifier) LocalRoot() string {
	return v.localRoot
}

// ValidatePersistedRoot validates a configured save root without changing the filesystem.
// Unlike ResolveSaveRoot, the local root itself is a valid persisted root.
func (v *Verifier) ValidatePersistedRoot(savePath string) (string, error) {
	if err := validateExternalSaveRoot(savePath); err != nil {
		return "", err
	}
	evaluated, err := v.resolveSaveRoot(savePath)
	if err != nil {
		return "", err
	}
	if err := rejectReservedComponent(evaluated); err != nil {
		return "", err
	}
	return filepath.Clean(evaluated), nil
}

// ResolveSaveRoot returns the canonical save root without changing the filesystem.
func (v *Verifier) ResolveSaveRoot(savePath string) (string, bool, error) {
	if err := validateExternalSaveRoot(savePath); err != nil {
		return "", false, err
	}
	if evaluated, err := v.resolveSaveRoot(savePath); err == nil {
		if err := rejectReservedComponent(evaluated); err != nil {
			return "", false, err
		}
		if evaluated == v.localRoot {
			return "", false, fmt.Errorf("fsafe: save root must be below local root")
		}
		return evaluated, true, nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return "", false, err
	}

	current := savePath
	var missing []string
	var evaluatedParent string
	for {
		evaluated, err := filepath.EvalSymlinks(current)
		if err == nil {
			evaluatedParent = filepath.Clean(evaluated)
			if err := rejectReservedComponent(evaluatedParent); err != nil {
				return "", false, err
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return "", false, fmt.Errorf("fsafe: resolve save root parent: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", false, fmt.Errorf("fsafe: save root has no existing parent")
		}
		missing = append(missing, filepath.Base(current))
		current = parent
	}
	if evaluatedParent != v.localRoot && !strictlyWithin(v.localRoot, evaluatedParent) {
		return "", false, fmt.Errorf("fsafe: save root escapes local root")
	}
	relative, err := filepath.Rel(v.localRoot, evaluatedParent)
	if err != nil || outsideRoot(relative) {
		return "", false, fmt.Errorf("fsafe: save root escapes local root")
	}
	for index := len(missing) - 1; index >= 0; index-- {
		relative = filepath.Join(relative, missing[index])
	}
	canonical := filepath.Join(v.localRoot, relative)
	if err := rejectReservedComponent(canonical); err != nil {
		return "", false, err
	}
	return canonical, false, nil
}

// validateExternalSaveRoot rejects reserved logical roots before any operation
// can mutate the filesystem. Internal callers use resolveSaveRoot directly so
// derived workspace paths remain valid roots.
func validateExternalSaveRoot(savePath string) error {
	if !filepath.IsAbs(savePath) || filepath.Clean(savePath) != savePath {
		return fmt.Errorf("fsafe: save root must be an absolute clean path")
	}
	if err := rejectReservedComponent(savePath); err != nil {
		return err
	}
	if evaluated, err := filepath.EvalSymlinks(savePath); err == nil {
		if err := rejectReservedComponent(filepath.Clean(evaluated)); err != nil {
			return err
		}
	}
	return nil
}

// saveRootMode lets CloudDrive2 copy into the staging directory and makes the
// content it creates inherit the group, which is what allows CD211 to delete it
// again. CD211 and CloudDrive2 must therefore share a group.
const (
	saveRootMode        = os.ModeSticky | os.ModeSetgid | 0o770
	workspaceParentMode = os.ModeSetgid | 0o750
	workspaceMode       = os.ModeSetgid | 0o770
	quarantineMode      = 0o700
)

// PrepareSaveRoot creates a missing canonical staging directory and hardens
// the resulting directory for shared use.
func (v *Verifier) PrepareSaveRoot(savePath string) (string, error) {
	if err := validateExternalSaveRoot(savePath); err != nil {
		return "", err
	}
	canonical, exists, err := v.ResolveSaveRoot(savePath)
	if err != nil {
		return "", err
	}
	if exists {
		root, evaluated, err := v.openSaveRoot(canonical)
		if err != nil {
			return "", err
		}
		defer root.Close()
		if evaluated != canonical {
			return "", fmt.Errorf("fsafe: save root changed during preparation")
		}
		if err := setRootMode(root, saveRootMode, "save root"); err != nil {
			return "", err
		}
		return canonical, nil
	}
	relative, err := filepath.Rel(v.localRoot, canonical)
	if err != nil || outsideRoot(relative) {
		return "", fmt.Errorf("fsafe: save root escapes local root")
	}
	root, err := os.OpenRoot(v.localRoot)
	if err != nil {
		return "", fmt.Errorf("fsafe: open local root: %w", err)
	}
	defer root.Close()
	if err := root.MkdirAll(relative, 0o770); err != nil {
		return "", fmt.Errorf("fsafe: create save root: %w", err)
	}
	created, err := root.OpenRoot(relative)
	if err != nil {
		return "", fmt.Errorf("fsafe: open save root: %w", err)
	}
	defer created.Close()
	if err := setRootMode(created, saveRootMode, "save root"); err != nil {
		return "", err
	}
	prepared, err := v.resolveSaveRoot(canonical)
	if err != nil {
		return "", err
	}
	if prepared != canonical {
		return "", fmt.Errorf("fsafe: save root changed during preparation")
	}
	return prepared, nil
}

// WorkspacePath derives the isolated workspace for a torrent hash.
func WorkspacePath(savePath, hash string) (string, error) {
	if !filepath.IsAbs(savePath) || filepath.Clean(savePath) != savePath {
		return "", fmt.Errorf("fsafe: save root must be an absolute clean path")
	}
	if err := validateWorkspaceHash(hash); err != nil {
		return "", err
	}
	return filepath.Join(savePath, ".cd211", hash), nil
}

// PrepareWorkspace securely creates and validates the isolated workspace for
// hash beneath savePath. The returned path preserves the logical save path.
func (v *Verifier) PrepareWorkspace(savePath, hash string) (string, error) {
	if err := validateExternalSaveRoot(savePath); err != nil {
		return "", err
	}
	logicalWorkspacePath, err := WorkspacePath(savePath, hash)
	if err != nil {
		return "", err
	}

	saveRoot, _, err := v.openWorkspaceSaveRoot(savePath)
	if err != nil {
		return "", err
	}
	defer saveRoot.Close()

	cdRoot, err := ensureWorkspaceDir(saveRoot, ".cd211", workspaceParentMode)
	if err != nil {
		return "", fmt.Errorf("fsafe: prepare workspace parent: %w", err)
	}
	defer cdRoot.Close()
	cdInfo, err := saveRoot.Lstat(".cd211")
	if err != nil {
		return "", fmt.Errorf("fsafe: revalidate workspace parent: %w", err)
	}
	if err := sameRootDirectory(saveRoot, ".cd211", cdInfo, cdRoot); err != nil {
		return "", err
	}
	workspaceRoot, err := ensureWorkspaceDir(cdRoot, hash, workspaceMode)
	if err != nil {
		return "", fmt.Errorf("fsafe: prepare workspace: %w", err)
	}
	if err := sameRootDirectory(saveRoot, ".cd211", cdInfo, cdRoot); err != nil {
		_ = workspaceRoot.Close()
		return "", err
	}
	if err := workspaceRoot.Close(); err != nil {
		return "", fmt.Errorf("fsafe: close workspace: %w", err)
	}
	return logicalWorkspacePath, nil
}

// DeleteWorkspace safely and idempotently removes exactly hash's workspace,
// retaining the shared .cd211 parent.
func (v *Verifier) DeleteWorkspace(savePath, hash string) error {
	if _, err := WorkspacePath(savePath, hash); err != nil {
		return err
	}

	saveRoot, _, err := v.openWorkspaceSaveRoot(savePath)
	if err != nil {
		return err
	}
	defer saveRoot.Close()

	cdInfo, err := saveRoot.Lstat(".cd211")
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fsafe: inspect workspace parent: %w", err)
	}
	if cdInfo.Mode()&os.ModeSymlink != 0 || !cdInfo.IsDir() {
		return fmt.Errorf("fsafe: workspace parent must be a directory")
	}
	cdRoot, err := saveRoot.OpenRoot(".cd211")
	if err != nil {
		return fmt.Errorf("fsafe: open workspace parent: %w", err)
	}
	defer cdRoot.Close()
	if err := sameRootDirectory(saveRoot, ".cd211", cdInfo, cdRoot); err != nil {
		return err
	}

	quarantineRoot, err := ensureWorkspaceDir(cdRoot, ".quarantine", quarantineMode)
	if err != nil {
		return fmt.Errorf("fsafe: prepare workspace quarantine: %w", err)
	}
	defer quarantineRoot.Close()
	quarantineInfo, err := cdRoot.Lstat(".quarantine")
	if err != nil {
		return fmt.Errorf("fsafe: revalidate workspace quarantine: %w", err)
	}
	if err := sameRootDirectory(cdRoot, ".quarantine", quarantineInfo, quarantineRoot); err != nil {
		return err
	}

	// A previous process may have crashed after quarantine. Clean that exact
	// inode first; a mismatched entry is never recursively removed.
	if err := removeQuarantinedWorkspace(quarantineRoot, hash); err != nil {
		return err
	}

	hashInfo, err := cdRoot.Lstat(hash)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fsafe: inspect workspace: %w", err)
	}
	if hashInfo.Mode()&os.ModeSymlink != 0 || !hashInfo.IsDir() {
		return fmt.Errorf("fsafe: workspace must be a directory")
	}
	hashRoot, err := cdRoot.OpenRoot(hash)
	if err != nil {
		return fmt.Errorf("fsafe: open workspace: %w", err)
	}
	if err := sameRootDirectory(cdRoot, hash, hashInfo, hashRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := rejectWorkspaceTree(hashRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := sameRootDirectory(saveRoot, ".cd211", cdInfo, cdRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := sameRootDirectory(cdRoot, hash, hashInfo, hashRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := cdRoot.Rename(hash, filepath.Join(".quarantine", hash)); err != nil {
		_ = hashRoot.Close()
		return fmt.Errorf("fsafe: quarantine workspace: %w", err)
	}
	quarantinedInfo, err := quarantineRoot.Lstat(hash)
	if err != nil {
		_ = hashRoot.Close()
		return fmt.Errorf("fsafe: revalidate quarantined workspace: %w", err)
	}
	if !os.SameFile(hashInfo, quarantinedInfo) {
		_ = hashRoot.Close()
		return fmt.Errorf("fsafe: workspace changed during quarantine")
	}
	if err := sameRootDirectory(quarantineRoot, hash, quarantinedInfo, hashRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := rejectWorkspaceTree(hashRoot); err != nil {
		_ = hashRoot.Close()
		return err
	}
	if err := hashRoot.Close(); err != nil {
		return fmt.Errorf("fsafe: close quarantined workspace: %w", err)
	}
	if err := quarantineRoot.RemoveAll(hash); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsafe: remove quarantined workspace: %w", err)
	}
	return nil
}

func validateWorkspaceHash(hash string) error {
	if len(hash) != 40 {
		return fmt.Errorf("fsafe: workspace hash must be a lowercase 40-character hex string")
	}
	for index := range len(hash) {
		char := hash[index]
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return fmt.Errorf("fsafe: workspace hash must be a lowercase 40-character hex string")
		}
	}
	return nil
}

func rejectReservedComponent(path string) error {
	for _, component := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if component == ".cd211" {
			return fmt.Errorf("fsafe: path contains reserved component %q", component)
		}
	}
	return nil
}

func (v *Verifier) openWorkspaceSaveRoot(savePath string) (*os.Root, string, error) {
	root, evaluatedSavePath, err := v.openSaveRoot(savePath)
	if err != nil {
		return nil, "", err
	}
	if !withinOrEqual(v.localRoot, evaluatedSavePath) {
		_ = root.Close()
		return nil, "", fmt.Errorf("fsafe: save root must be below or equal to local root")
	}
	if err := setRootMode(root, saveRootMode, "save root"); err != nil {
		_ = root.Close()
		return nil, "", err
	}
	return root, evaluatedSavePath, nil
}

func setRootMode(root *os.Root, mode os.FileMode, name string) error {
	dir, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("fsafe: open %s descriptor: %w", name, err)
	}
	defer dir.Close()
	if err := dir.Chmod(mode); err != nil {
		return fmt.Errorf("fsafe: set %s mode: %w", name, err)
	}
	return nil
}

func removeQuarantinedWorkspace(parent *os.Root, hash string) error {
	info, err := parent.Lstat(hash)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fsafe: inspect quarantined workspace: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("fsafe: quarantined workspace must be a directory")
	}
	child, err := parent.OpenRoot(hash)
	if err != nil {
		return fmt.Errorf("fsafe: open quarantined workspace: %w", err)
	}
	defer child.Close()
	if err := sameRootDirectory(parent, hash, info, child); err != nil {
		return err
	}
	if err := rejectWorkspaceTree(child); err != nil {
		return err
	}
	if err := sameRootDirectory(parent, hash, info, child); err != nil {
		return err
	}
	if err := parent.RemoveAll(hash); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("fsafe: remove quarantined workspace: %w", err)
	}
	return nil
}

func ensureWorkspaceDir(parent *os.Root, name string, mode os.FileMode) (*os.Root, error) {
	info, err := parent.Lstat(name)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("fsafe: inspect %q: %w", name, err)
		}
		if err := parent.Mkdir(name, 0o770); err != nil && !os.IsExist(err) {
			return nil, fmt.Errorf("fsafe: create %q: %w", name, err)
		}
		info, err = parent.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("fsafe: inspect created %q: %w", name, err)
		}
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("fsafe: %q must not be a symbolic link", name)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("fsafe: %q is not a directory", name)
	}

	child, err := parent.OpenRoot(name)
	if err != nil {
		return nil, fmt.Errorf("fsafe: open %q: %w", name, err)
	}
	if err := sameRootDirectory(parent, name, info, child); err != nil {
		_ = child.Close()
		return nil, err
	}
	if err := setRootMode(child, mode, name); err != nil {
		_ = child.Close()
		return nil, err
	}
	if err := sameRootDirectory(parent, name, info, child); err != nil {
		_ = child.Close()
		return nil, err
	}
	return child, nil
}

func sameRootDirectory(parent *os.Root, name string, expected os.FileInfo, child *os.Root) error {
	anchored, err := child.Stat(".")
	if err != nil {
		return fmt.Errorf("fsafe: inspect anchored %q: %w", name, err)
	}
	if !os.SameFile(expected, anchored) {
		return fmt.Errorf("fsafe: %q changed during validation", name)
	}
	current, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("fsafe: revalidate %q: %w", name, err)
	}
	if current.Mode()&os.ModeSymlink != 0 || !os.SameFile(expected, current) {
		return fmt.Errorf("fsafe: %q changed during validation", name)
	}
	return nil
}

func rejectWorkspaceTree(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("fsafe: inspect workspace tree: %w", err)
		}
		if path == "." {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fsafe: workspace tree contains symbolic link at %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("fsafe: inspect workspace tree entry %q: %w", path, err)
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("fsafe: workspace tree contains special file at %q", path)
		}
		return nil
	})
}

// Verify checks that the expected torrent content is a safe child of savePath
// and returns its cleaned logical absolute path with the bytes on disk.
func (v *Verifier) Verify(savePath string, expected ExpectedContent) (VerifiedContent, error) {
	if err := validateName(expected.CandidateName); err != nil {
		return VerifiedContent{}, err
	}

	saveRoot, err := v.resolveSaveRoot(savePath)
	if err != nil {
		return VerifiedContent{}, err
	}
	if !expected.MultiFile && len(expected.Files) > 0 {
		if len(expected.Files) != 1 {
			return VerifiedContent{}, fmt.Errorf("fsafe: single-file manifest does not match candidate")
		}
		size, verifyErr := verifyManifest(saveRoot, expected.Files)
		if verifyErr != nil {
			return VerifiedContent{}, verifyErr
		}
		candidate := filepath.Join(filepath.Clean(savePath), filepath.FromSlash(expected.Files[0].RelativePath))
		return VerifiedContent{Path: candidate, Size: size}, nil
	}

	candidatePath := filepath.Join(filepath.Clean(savePath), expected.CandidateName)
	info, err := os.Lstat(candidatePath)
	if err != nil {
		return VerifiedContent{}, fmt.Errorf("fsafe: inspect candidate: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return VerifiedContent{}, fmt.Errorf("fsafe: candidate must not be a symbolic link")
	}

	candidate, err := filepath.EvalSymlinks(candidatePath)
	if err != nil {
		return VerifiedContent{}, fmt.Errorf("fsafe: resolve candidate: %w", err)
	}
	candidate = filepath.Clean(candidate)
	if !strictlyWithin(saveRoot, candidate) || !strictlyWithin(v.localRoot, candidate) {
		return VerifiedContent{}, fmt.Errorf("fsafe: candidate escapes configured roots")
	}

	info, err = os.Stat(candidate)
	if err != nil {
		return VerifiedContent{}, fmt.Errorf("fsafe: inspect resolved candidate: %w", err)
	}
	if !expected.MultiFile {
		if !info.Mode().IsRegular() {
			return VerifiedContent{}, fmt.Errorf("fsafe: single-file candidate is not a regular file")
		}
		if len(expected.Files) > 0 {
			if len(expected.Files) != 1 || !validManifestPath(expected.Files[0].RelativePath) ||
				expected.Files[0].Size < 0 || info.Size() != expected.Files[0].Size {
				return VerifiedContent{}, fmt.Errorf("fsafe: single-file manifest does not match candidate")
			}
		}
		return VerifiedContent{Path: candidatePath, Size: info.Size()}, nil
	}
	if !info.IsDir() {
		return VerifiedContent{}, fmt.Errorf("fsafe: multi-file candidate is not a directory")
	}
	if len(expected.Files) == 0 {
		size, err := treeSize(candidate)
		if err != nil {
			return VerifiedContent{}, err
		}
		return VerifiedContent{Path: candidatePath, Size: size}, nil
	}
	size, err := verifyManifest(candidate, expected.Files)
	if err != nil {
		return VerifiedContent{}, err
	}
	return VerifiedContent{Path: filepath.Join(filepath.Clean(savePath), expected.CandidateName), Size: size}, nil
}

// UnknownContent is the verified shape of magnet content whose file-vs-folder
// kind is only known from the actual copy on disk.
type UnknownContent struct {
	Path      string
	Size      int64
	MultiFile bool
}

// VerifyUnknownType validates the candidate at savePath/name when the
// expected kind is unknown: a magnet carries no file metadata, so the staged
// tree itself decides whether the content is one file or a directory. Only a
// regular file or a directory is accepted; symlinks, root escapes, and
// FIFO/socket/device/special files are rejected, and the same safe-name
// validation, root confinement, and size measurement as Verify apply.
func (v *Verifier) VerifyUnknownType(savePath, name string) (UnknownContent, error) {
	if err := validateName(name); err != nil {
		return UnknownContent{}, err
	}

	saveRoot, err := v.resolveSaveRoot(savePath)
	if err != nil {
		return UnknownContent{}, err
	}

	candidatePath := filepath.Join(filepath.Clean(savePath), name)
	info, err := os.Lstat(candidatePath)
	if err != nil {
		return UnknownContent{}, fmt.Errorf("fsafe: inspect candidate: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return UnknownContent{}, fmt.Errorf("fsafe: candidate must not be a symbolic link")
	}

	candidate, err := filepath.EvalSymlinks(candidatePath)
	if err != nil {
		return UnknownContent{}, fmt.Errorf("fsafe: resolve candidate: %w", err)
	}
	candidate = filepath.Clean(candidate)
	if !strictlyWithin(saveRoot, candidate) || !strictlyWithin(v.localRoot, candidate) {
		return UnknownContent{}, fmt.Errorf("fsafe: candidate escapes configured roots")
	}

	info, err = os.Stat(candidate)
	if err != nil {
		return UnknownContent{}, fmt.Errorf("fsafe: inspect resolved candidate: %w", err)
	}
	if info.IsDir() {
		size, err := treeSize(candidate)
		if err != nil {
			return UnknownContent{}, err
		}
		return UnknownContent{Path: candidatePath, Size: size, MultiFile: true}, nil
	}
	if !info.Mode().IsRegular() {
		return UnknownContent{}, fmt.Errorf("fsafe: candidate is not a regular file or directory")
	}
	return UnknownContent{Path: candidatePath, Size: info.Size(), MultiFile: false}, nil
}

// treeSize sums the regular files under root. Symlinks are skipped rather than
// followed, matching Verify's refusal to trust links inside the staging tree.
func treeSize(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.Type().IsRegular() {

			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		total += info.Size()
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("fsafe: measure content: %w", err)
	}
	return total, nil
}

func validManifestPath(relative string) bool {
	return relative != "" && !filepath.IsAbs(relative) && filepath.Clean(relative) == relative &&
		relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!strings.ContainsAny(relative, "\\\x00") && utf8.ValidString(relative) &&
		!strings.ContainsFunc(relative, unicode.IsControl)
}
func verifyManifest(root string, files []ExpectedFile) (int64, error) {
	var total int64
	seen := make(map[string]struct{}, len(files))
	for _, expected := range files {
		relative := expected.RelativePath
		if !validManifestPath(relative) || expected.Size < 0 {
			return 0, fmt.Errorf("fsafe: manifest path or size is invalid")
		}
		if _, ok := seen[relative]; ok {
			return 0, fmt.Errorf("fsafe: manifest contains duplicate path")
		}
		seen[relative] = struct{}{}
		current := root
		parts := strings.Split(relative, string(filepath.Separator))
		for index, part := range parts {
			current = filepath.Join(current, part)
			info, err := os.Lstat(current)
			if err != nil {
				return 0, fmt.Errorf("fsafe: inspect manifest path: %w", err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return 0, fmt.Errorf("fsafe: manifest path contains symbolic link")
			}
			if index < len(parts)-1 && !info.IsDir() {
				return 0, fmt.Errorf("fsafe: manifest parent is not a directory")
			}
			if index == len(parts)-1 {
				if !info.Mode().IsRegular() || info.Size() != expected.Size {
					return 0, fmt.Errorf("fsafe: manifest file is not an exact regular file")
				}
			}
		}
		if total > math.MaxInt64-expected.Size {
			return 0, fmt.Errorf("fsafe: manifest size overflows")
		}
		total += expected.Size
	}
	return total, nil
}

// Delete safely removes verified torrent content beneath savePath. Direct child
// directories and regular files at any safe relative path are supported. A
// missing path is treated as already deleted after its roots are validated.
func (v *Verifier) Delete(contentPath, savePath string) error {
	if !filepath.IsAbs(savePath) || !filepath.IsAbs(contentPath) {
		return fmt.Errorf("fsafe: save and content paths must be absolute")
	}

	cleanSavePath := filepath.Clean(savePath)
	cleanContentPath := filepath.Clean(contentPath)
	root, evaluatedSavePath, err := v.openSaveRoot(cleanSavePath)
	if err != nil {
		return err
	}
	defer root.Close()

	relative, err := filepath.Rel(cleanSavePath, cleanContentPath)
	if err != nil || outsideRoot(relative) {
		relative, err = filepath.Rel(evaluatedSavePath, cleanContentPath)
	}
	if err != nil || !validManifestPath(relative) {
		return fmt.Errorf("fsafe: content escapes save root")
	}
	if err := safePlanComponents(evaluatedSavePath, relative); err != nil {
		return err
	}

	info, err := root.Lstat(relative)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("fsafe: inspect content: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("fsafe: content must not be a symbolic link")
	}
	if !info.IsDir() && !info.Mode().IsRegular() {
		return fmt.Errorf("fsafe: content is not a regular file or directory")
	}
	if info.IsDir() && filepath.Dir(relative) != "." {
		return fmt.Errorf("fsafe: directory content must be a direct child of save root")
	}

	if info.IsDir() {
		if err := rejectSymlinkTree(filepath.Join(evaluatedSavePath, relative)); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		err = root.RemoveAll(relative)
	} else {
		err = root.Remove(relative)
	}
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("fsafe: remove content: %w", err)
	}

	return nil
}

func rejectSymlinkTree(root string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("fsafe: inspect deletion tree: %w", err)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("fsafe: deletion tree contains symbolic link at %q", path)
		}
		return nil
	})
}

func (v *Verifier) openSaveRoot(savePath string) (*os.Root, string, error) {
	cleanSavePath := filepath.Clean(savePath)

	localRoot, err := os.OpenRoot(v.localRoot)
	if err != nil {
		return nil, "", fmt.Errorf("fsafe: open local root: %w", err)
	}
	defer localRoot.Close()

	evaluatedSavePath, err := filepath.EvalSymlinks(cleanSavePath)
	if err != nil {
		return nil, "", fmt.Errorf("fsafe: resolve save root: %w", err)
	}
	evaluatedSavePath = filepath.Clean(evaluatedSavePath)

	expectedInfo, err := os.Stat(evaluatedSavePath)
	if err != nil {
		return nil, "", fmt.Errorf("fsafe: inspect save root: %w", err)
	}
	if !expectedInfo.IsDir() {
		return nil, "", fmt.Errorf("fsafe: save root is not a directory")
	}

	relativeSavePath, err := filepath.Rel(v.localRoot, evaluatedSavePath)
	if err != nil || outsideRoot(relativeSavePath) {
		return nil, "", fmt.Errorf("fsafe: save root escapes local root")
	}
	saveRoot, err := localRoot.OpenRoot(relativeSavePath)
	if err != nil {
		return nil, "", fmt.Errorf("fsafe: open save root: %w", err)
	}
	anchoredInfo, err := saveRoot.Stat(".")
	if err != nil {
		_ = saveRoot.Close()
		return nil, "", fmt.Errorf("fsafe: inspect anchored save root: %w", err)
	}
	if !os.SameFile(expectedInfo, anchoredInfo) {
		_ = saveRoot.Close()
		return nil, "", fmt.Errorf("fsafe: save root changed during validation")
	}

	return saveRoot, evaluatedSavePath, nil
}

func (v *Verifier) resolveSaveRoot(savePath string) (string, error) {
	if !filepath.IsAbs(savePath) {
		return "", fmt.Errorf("fsafe: save root must be absolute")
	}
	cleanSavePath := filepath.Clean(savePath)

	evaluatedSavePath, err := filepath.EvalSymlinks(cleanSavePath)
	if err != nil {
		return "", fmt.Errorf("fsafe: resolve save root: %w", err)
	}
	evaluatedSavePath = filepath.Clean(evaluatedSavePath)

	info, err := os.Stat(evaluatedSavePath)
	if err != nil {
		return "", fmt.Errorf("fsafe: inspect save root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fsafe: save root is not a directory")
	}
	if !withinOrEqual(v.localRoot, evaluatedSavePath) {
		return "", fmt.Errorf("fsafe: save root escapes local root")
	}

	return evaluatedSavePath, nil
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." {
		return fmt.Errorf("fsafe: content name is not a safe path segment")
	}
	if !utf8.ValidString(name) {
		return fmt.Errorf("fsafe: content name is not valid UTF-8")
	}
	for _, r := range name {
		if r == '/' || r == '\\' || unicode.IsControl(r) {
			return fmt.Errorf("fsafe: content name is not a safe path segment")
		}
	}
	return nil
}

func withinOrEqual(root, path string) bool {
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return !outsideRoot(relativePath)
}

func strictlyWithin(root, path string) bool {
	relativePath, err := filepath.Rel(root, path)
	if err != nil || relativePath == "." {
		return false
	}
	return !outsideRoot(relativePath)
}

func outsideRoot(relativePath string) bool {
	return relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
}
