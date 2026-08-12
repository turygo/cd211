// Package fsafe validates and removes torrent content beneath a configured root.
package fsafe

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ExpectedContent describes the content created for a torrent.
type ExpectedContent struct {
	Name      string
	MultiFile bool
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
	evaluatedRoot, err := filepath.EvalSymlinks(configuredRoot)
	if err != nil {
		return nil, fmt.Errorf("fsafe: resolve local root: %w", err)
	}
	evaluatedRoot = filepath.Clean(evaluatedRoot)

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

// ResolveSaveRoot returns the canonical save root without changing the filesystem.
func (v *Verifier) ResolveSaveRoot(savePath string) (string, bool, error) {
	if !filepath.IsAbs(savePath) || filepath.Clean(savePath) != savePath {
		return "", false, fmt.Errorf("fsafe: save root must be an absolute clean path")
	}
	if evaluated, err := v.resolveSaveRoot(savePath); err == nil {
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
	return filepath.Join(v.localRoot, relative), false, nil
}

// saveRootMode lets CloudDrive2 copy into the staging directory and makes the
// content it creates inherit the group, which is what allows CD211 to delete it
// again. CD211 and CloudDrive2 must therefore share a group.
const saveRootMode = os.ModeSetgid | 0o770

// PrepareSaveRoot creates a missing canonical staging directory. An existing
// directory keeps the mode and owner it already has.
func (v *Verifier) PrepareSaveRoot(savePath string) (string, error) {
	canonical, exists, err := v.ResolveSaveRoot(savePath)
	if err != nil || exists {
		return canonical, err
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
	// MkdirAll applies the process umask, so the group and setgid bits are set
	// explicitly. The mode is applied through the descriptor of the directory
	// that was just checked, which a path-based chmod cannot do without racing
	// a symlink swap.
	created, err := root.Open(relative)
	if err != nil {
		return "", fmt.Errorf("fsafe: open save root: %w", err)
	}
	defer created.Close()
	info, err := created.Stat()
	if err != nil {
		return "", fmt.Errorf("fsafe: inspect save root: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("fsafe: save root is not a directory")
	}
	if err := created.Chmod(saveRootMode); err != nil {
		return "", fmt.Errorf("fsafe: set save root mode: %w", err)
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

// Verify checks that the expected torrent content is a safe child of savePath
// and returns its cleaned, evaluated absolute path with the bytes on disk.
func (v *Verifier) Verify(savePath string, expected ExpectedContent) (VerifiedContent, error) {
	if err := validateName(expected.Name); err != nil {
		return VerifiedContent{}, err
	}

	saveRoot, err := v.resolveSaveRoot(savePath)
	if err != nil {
		return VerifiedContent{}, err
	}

	candidatePath := filepath.Join(filepath.Clean(savePath), expected.Name)
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
		return VerifiedContent{Path: candidate, Size: info.Size()}, nil
	}
	if !info.IsDir() {
		return VerifiedContent{}, fmt.Errorf("fsafe: multi-file candidate is not a directory")
	}
	size, err := treeSize(candidate)
	if err != nil {
		return VerifiedContent{}, err
	}
	return VerifiedContent{Path: candidate, Size: size}, nil
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
		return UnknownContent{Path: candidate, Size: size, MultiFile: true}, nil
	}
	if !info.Mode().IsRegular() {
		return UnknownContent{}, fmt.Errorf("fsafe: candidate is not a regular file or directory")
	}
	return UnknownContent{Path: candidate, Size: info.Size(), MultiFile: false}, nil
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

// Delete safely removes a checked direct child of savePath. A missing direct
// child is treated as already deleted after its roots have been validated.
func (v *Verifier) Delete(contentPath, savePath string) error {
	if !filepath.IsAbs(savePath) || !filepath.IsAbs(contentPath) {
		return fmt.Errorf("fsafe: save and content paths must be absolute")
	}

	cleanSavePath := filepath.Clean(savePath)
	cleanContentPath := filepath.Clean(contentPath)
	rawDirectChild := directChild(cleanSavePath, cleanContentPath)

	root, evaluatedSavePath, err := v.openSaveRoot(cleanSavePath)
	if err != nil {
		return err
	}
	defer root.Close()
	if !rawDirectChild && !directChild(evaluatedSavePath, cleanContentPath) {
		return fmt.Errorf("fsafe: content must be a direct child of save root")
	}

	name := filepath.Base(cleanContentPath)
	info, err := root.Lstat(name)
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

	if info.IsDir() {
		if err := rejectSymlinkTree(cleanContentPath); err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
	}
	if info.IsDir() {
		err = root.RemoveAll(name)
	} else {
		err = root.Remove(name)
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
	localRoot, err := os.OpenRoot(v.localRoot)
	if err != nil {
		return nil, "", fmt.Errorf("fsafe: open local root: %w", err)
	}
	defer localRoot.Close()

	evaluatedSavePath, err := filepath.EvalSymlinks(filepath.Clean(savePath))
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

	evaluatedSavePath, err := filepath.EvalSymlinks(filepath.Clean(savePath))
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

func directChild(parent, child string) bool {
	return filepath.Dir(child) == parent
}

func outsideRoot(relativePath string) bool {
	return relativePath == ".." || strings.HasPrefix(relativePath, ".."+string(filepath.Separator))
}
