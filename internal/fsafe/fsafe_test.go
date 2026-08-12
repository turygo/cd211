package fsafe

import (
	"errors"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestVerifySingleFile(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	content := writeFile(t, filepath.Join(save, "movie.mkv"), "content")

	got, err := verifier.Verify(save, ExpectedContent{Name: "movie.mkv"})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(content)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if got.Path != want {
		t.Fatalf("Verify() path = %q, want %q", got.Path, want)
	}
	if got.Size != int64(len("content")) {
		t.Fatalf("Verify() size = %d, want %d", got.Size, len("content"))
	}
}

func TestVerifyMultiFileDirectorySumsRegularFiles(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	content := mkdir(t, filepath.Join(save, "album"))
	writeFile(t, filepath.Join(content, "track.flac"), "content")
	writeFile(t, filepath.Join(content, "cover.jpg"), "xx")
	// A nested directory contributes its files but not its own inode size.
	nested := mkdir(t, filepath.Join(content, "extras"))
	writeFile(t, filepath.Join(nested, "notes.txt"), "abc")

	got, err := verifier.Verify(save, ExpectedContent{Name: "album", MultiFile: true})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	want, err := filepath.EvalSymlinks(content)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}
	if got.Path != want {
		t.Fatalf("Verify() path = %q, want %q", got.Path, want)
	}
	if wantSize := int64(len("content") + len("xx") + len("abc")); got.Size != wantSize {
		t.Fatalf("Verify() size = %d, want %d", got.Size, wantSize)
	}
}

func TestVerifyRejectsNameMismatchAndUnsafeName(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	writeFile(t, filepath.Join(save, "actual"), "content")

	if _, err := verifier.Verify(save, ExpectedContent{Name: "expected"}); err == nil {
		t.Fatal("Verify() succeeded for a missing expected name")
	}

	for _, name := range []string{"", ".", "..", "nested/file", `nested\\file`, "line\nbreak", string([]byte{0xff})} {
		t.Run(name, func(t *testing.T) {
			if _, err := verifier.Verify(save, ExpectedContent{Name: name}); err == nil {
				t.Fatalf("Verify() succeeded for unsafe name %q", name)
			}
		})
	}
}

func TestVerifyRejectsMissingCandidate(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))

	if _, err := verifier.Verify(save, ExpectedContent{Name: "missing"}); err == nil {
		t.Fatal("Verify() succeeded for missing candidate")
	}
}

func TestVerifyRejectsCandidateSymlinkEscapingRoot(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	outside := writeFile(t, filepath.Join(t.TempDir(), "outside"), "outside")
	mustSymlink(t, outside, filepath.Join(save, "content"))

	if _, err := verifier.Verify(save, ExpectedContent{Name: "content"}); err == nil {
		t.Fatal("Verify() succeeded for candidate symlink escaping root")
	}
}

func TestVerifyRejectsCandidateSymlinkInsideRoot(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	target := writeFile(t, filepath.Join(save, "target"), "content")
	mustSymlink(t, target, filepath.Join(save, "content"))

	if _, err := verifier.Verify(save, ExpectedContent{Name: "content"}); err == nil {
		t.Fatal("Verify() succeeded for a candidate symbolic link inside root")
	}
}

func TestVerifyRejectsSaveRootSymlinkEscapingLocalRoot(t *testing.T) {
	verifier, root := newTestVerifier(t)
	outsideSave := mkdir(t, filepath.Join(t.TempDir(), "save"))
	writeFile(t, filepath.Join(outsideSave, "content"), "outside")
	saveLink := filepath.Join(root, "save-link")
	mustSymlink(t, outsideSave, saveLink)

	if _, err := verifier.Verify(saveLink, ExpectedContent{Name: "content"}); err == nil {
		t.Fatal("Verify() succeeded for save root symlink escaping local root")
	}
}

func TestVerifyRejectsSiblingPrefixPath(t *testing.T) {
	base := t.TempDir()
	localRoot := mkdir(t, filepath.Join(base, "local"))
	siblingSave := mkdir(t, filepath.Join(base, "local-sibling", "save"))
	writeFile(t, filepath.Join(siblingSave, "content"), "outside")
	verifier, err := New(localRoot)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := verifier.Verify(siblingSave, ExpectedContent{Name: "content"}); err == nil {
		t.Fatal("Verify() accepted sibling-prefix path outside local root")
	}
}

func TestVerifyUnknownTypeAcceptsFileAndDirectory(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	file := writeFile(t, filepath.Join(save, "movie.mkv"), "content")
	directory := mkdir(t, filepath.Join(save, "album"))
	writeFile(t, filepath.Join(directory, "track.flac"), "content")
	writeFile(t, filepath.Join(directory, "cover.jpg"), "xx")

	fileContent, err := verifier.VerifyUnknownType(save, "movie.mkv")
	if err != nil {
		t.Fatalf("VerifyUnknownType(file) error = %v", err)
	}
	if fileContent.MultiFile || fileContent.Size != int64(len("content")) {
		t.Fatalf("VerifyUnknownType(file) = %+v, want single file", fileContent)
	}
	filePath, err := filepath.EvalSymlinks(file)
	if err != nil {
		t.Fatal(err)
	}
	if fileContent.Path != filePath {
		t.Fatalf("VerifyUnknownType(file) path = %q, want %q", fileContent.Path, filePath)
	}

	directoryContent, err := verifier.VerifyUnknownType(save, "album")
	if err != nil {
		t.Fatalf("VerifyUnknownType(directory) error = %v", err)
	}
	if !directoryContent.MultiFile || directoryContent.Size != int64(len("content")+len("xx")) {
		t.Fatalf("VerifyUnknownType(directory) = %+v, want multi-file tree", directoryContent)
	}
	directoryPath, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatal(err)
	}
	if directoryContent.Path != directoryPath {
		t.Fatalf("VerifyUnknownType(directory) path = %q, want %q", directoryContent.Path, directoryPath)
	}
}

func TestVerifyUnknownTypeRejectsMissingCandidate(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))

	if _, err := verifier.VerifyUnknownType(save, "missing"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("VerifyUnknownType() error = %v, want not exist", err)
	}
}

func TestVerifyUnknownTypeRejectsSymlinkAndEscape(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	outside := writeFile(t, filepath.Join(t.TempDir(), "outside"), "outside")
	mustSymlink(t, outside, filepath.Join(save, "escape"))
	target := writeFile(t, filepath.Join(save, "target"), "content")
	mustSymlink(t, target, filepath.Join(save, "inside"))

	for _, name := range []string{"escape", "inside"} {
		if _, err := verifier.VerifyUnknownType(save, name); err == nil {
			t.Fatalf("VerifyUnknownType() succeeded for symbolic link %q", name)
		}
	}
}

func TestVerifyUnknownTypeRejectsSpecialFile(t *testing.T) {
	// Darwin's per-test TempDir can exceed the Unix socket sun_path limit,
	// so anchor the fixture at a short /tmp root and clean it up explicitly.
	root, err := os.MkdirTemp("/tmp", "cd211-fsafe-")
	if err != nil {
		t.Fatalf("MkdirTemp(/tmp): %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("RemoveAll(%q): %v", root, err)
		}
	})
	verifier, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	save := mkdir(t, filepath.Join(root, "save"))
	socketPath := filepath.Join(save, "socket")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socketPath, Net: "unix"})
	if err != nil {
		t.Fatalf("ListenUnix: %v", err)
	}
	defer listener.Close()

	if _, err := verifier.VerifyUnknownType(save, "socket"); err == nil {
		t.Fatal("VerifyUnknownType() succeeded for a socket")
	}
}

func TestVerifyRejectsStrictKindMismatch(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	writeFile(t, filepath.Join(save, "payload"), "content")
	directory := mkdir(t, filepath.Join(save, "album"))

	// Uploaded torrents must keep strict expected verification: a directory
	// cannot satisfy a single-file expectation and vice versa, even though a
	// magnet with the same candidate would be accepted by type.
	if _, err := verifier.Verify(save, ExpectedContent{Name: filepath.Base(directory), MultiFile: false}); err == nil {
		t.Fatal("Verify() accepted a directory for a single-file torrent")
	}
	if _, err := verifier.Verify(save, ExpectedContent{Name: "payload", MultiFile: true}); err == nil {
		t.Fatal("Verify() accepted a regular file for a multi-file torrent")
	}
	if _, err := verifier.VerifyUnknownType(save, "payload"); err != nil {
		t.Fatalf("VerifyUnknownType(file) error = %v, want type-unknown acceptance", err)
	}
	if _, err := verifier.VerifyUnknownType(save, "album"); err != nil {
		t.Fatalf("VerifyUnknownType(directory) error = %v, want type-unknown acceptance", err)
	}
}

func TestDeleteRejectsRootEquality(t *testing.T) {
	verifier, root := newTestVerifier(t)

	if err := verifier.Delete(root, root); err == nil {
		t.Fatal("Delete() allowed deleting the save root")
	}
	if _, err := os.Stat(root); err != nil {
		t.Fatalf("save root removed: %v", err)
	}
}

func TestDeleteRemovesOnlyRequestedDirectChild(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	target := writeFile(t, filepath.Join(save, "target"), "delete me")
	collateral := writeFile(t, filepath.Join(save, "keep"), "keep me")

	if err := verifier.Delete(target, save); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	assertNotExist(t, target)
	assertFileContent(t, collateral, "keep me")
}

func TestDeleteMissingDirectChildIsIdempotent(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))

	if err := verifier.Delete(filepath.Join(save, "missing"), save); err != nil {
		t.Fatalf("Delete() error = %v, want nil for missing direct child", err)
	}
}

func TestDeleteRejectsNonDirectChild(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	nested := writeFile(t, filepath.Join(save, "directory", "content"), "keep me")

	if err := verifier.Delete(nested, save); err == nil {
		t.Fatal("Delete() allowed deleting a non-direct child")
	}
	assertFileContent(t, nested, "keep me")
}

func TestDeleteRejectsUnsafeSymlinkWithoutTouchingOutsideContent(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	outside := writeFile(t, filepath.Join(t.TempDir(), "outside"), "outside")
	mustSymlink(t, outside, filepath.Join(save, "content"))

	if err := verifier.Delete(filepath.Join(save, "content"), save); err == nil {
		t.Fatal("Delete() accepted a symbolic-link content path")
	}
	assertFileContent(t, outside, "outside")
}

func TestDeleteRejectsDirectoryContainingNestedSymlink(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	target := mkdir(t, filepath.Join(save, "content"))
	outside := writeFile(t, filepath.Join(t.TempDir(), "outside"), "outside")
	mustSymlink(t, outside, filepath.Join(target, "outside-link"))

	if err := verifier.Delete(target, save); err == nil {
		t.Fatal("Delete() accepted a directory containing a symbolic link")
	}
	if _, err := os.Lstat(filepath.Join(target, "outside-link")); err != nil {
		t.Fatalf("nested symbolic link was removed: %v", err)
	}
	assertFileContent(t, outside, "outside")
}

func TestPrepareSaveRootCanonicalizesSymlinkedParent(t *testing.T) {
	verifier, root := newTestVerifier(t)
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks(root): %v", err)
	}
	if verifier.LocalRoot() != canonicalRoot {
		t.Fatalf("LocalRoot() = %q, want %q", verifier.LocalRoot(), canonicalRoot)
	}
	physical := mkdir(t, filepath.Join(root, "physical"))
	alias := filepath.Join(root, "alias")
	mustSymlink(t, physical, alias)

	canonicalPhysical, err := filepath.EvalSymlinks(physical)
	if err != nil {
		t.Fatalf("EvalSymlinks(physical): %v", err)
	}
	want := filepath.Join(canonicalPhysical, "movies")
	resolved, exists, err := verifier.ResolveSaveRoot(filepath.Join(alias, "movies"))
	if err != nil || exists || resolved != want {
		t.Fatalf("ResolveSaveRoot() = (%q, %t, %v), want (%q, false, nil)", resolved, exists, err, want)
	}
	assertNotExist(t, filepath.Join(alias, "movies"))
	prepared, err := verifier.PrepareSaveRoot(filepath.Join(alias, "movies"))
	if err != nil {
		t.Fatalf("PrepareSaveRoot(): %v", err)
	}
	if prepared != want {
		t.Fatalf("PrepareSaveRoot() = %q, want %q", prepared, want)
	}
	info, err := os.Stat(want)
	if err != nil || !info.IsDir() {
		t.Fatalf("prepared directory = (%+v, %v)", info, err)
	}

	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	mustSymlink(t, outside, filepath.Join(root, "outside-link"))
	if _, err := verifier.PrepareSaveRoot(filepath.Join(root, "outside-link", "escape")); err == nil {
		t.Fatal("PrepareSaveRoot() accepted an escaping symlink")
	}
}

// CloudDrive2 copies into the staging directory as a different user, so the
// group must keep write access even under a restrictive umask, and setgid must
// pass the shared group on to the content CloudDrive2 creates.
func TestPrepareSaveRootStaysWritableForTheSharedGroup(t *testing.T) {
	verifier, root := newTestVerifier(t)
	previousUmask := syscall.Umask(0o022)
	defer syscall.Umask(previousUmask)

	prepared, err := verifier.PrepareSaveRoot(filepath.Join(root, "tv"))
	if err != nil {
		t.Fatalf("PrepareSaveRoot(): %v", err)
	}
	info, err := os.Stat(prepared)
	if err != nil {
		t.Fatalf("Stat(prepared): %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o770); got != want {
		t.Fatalf("prepared permissions = %#o, want %#o", got, want)
	}
	if info.Mode()&os.ModeSetgid == 0 {
		t.Fatalf("prepared mode = %v, want the setgid bit", info.Mode())
	}
}

func TestVerifyThenDeleteThroughSaveRootSymlink(t *testing.T) {
	verifier, root := newTestVerifier(t)
	physicalSave := mkdir(t, filepath.Join(root, "physical-save"))
	saveLink := filepath.Join(root, "save-link")
	mustSymlink(t, physicalSave, saveLink)
	target := writeFile(t, filepath.Join(physicalSave, "payload"), "content")

	contentPath, err := verifier.Verify(saveLink, ExpectedContent{Name: "payload"})
	if err != nil {
		t.Fatalf("Verify() through save-root symlink: %v", err)
	}
	evaluatedTarget, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if contentPath.Path != evaluatedTarget {
		t.Fatalf("Verify() content path = %q, want %q", contentPath.Path, evaluatedTarget)
	}
	if err := verifier.Delete(contentPath.Path, saveLink); err != nil {
		t.Fatalf("Delete() verified symlink-root content: %v", err)
	}
	assertNotExist(t, target)
}

func TestOpenSaveRootRemainsAnchoredAfterRename(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	original := writeFile(t, filepath.Join(save, "target"), "original")
	anchored, _, err := verifier.openSaveRoot(save)
	if err != nil {
		t.Fatalf("openSaveRoot() error = %v", err)
	}
	defer anchored.Close()

	moved := filepath.Join(root, "save-moved")
	if err := os.Rename(save, moved); err != nil {
		t.Fatal(err)
	}
	replacement := mkdir(t, save)
	replacementTarget := writeFile(t, filepath.Join(replacement, "target"), "replacement")
	if err := anchored.Remove("target"); err != nil {
		t.Fatalf("anchored Remove() error = %v", err)
	}

	assertNotExist(t, filepath.Join(moved, filepath.Base(original)))
	assertFileContent(t, replacementTarget, "replacement")
}

func newTestVerifier(t *testing.T) (*Verifier, string) {
	t.Helper()
	root := mkdir(t, filepath.Join(t.TempDir(), "local"))
	verifier, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return verifier, root
}

func mkdir(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", path, err)
	}
	return path
}

func writeFile(t *testing.T, path, content string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("MkdirAll(%q) error = %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(%q) error = %v", path, err)
	}
	return path
}

func mustSymlink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("Symlink(%q, %q) error = %v", target, link, err)
	}
}

func assertNotExist(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Lstat(%q) error = %v, want not exist", path, err)
	}
}

func assertFileContent(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%q) error = %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("ReadFile(%q) = %q, want %q", path, got, want)
	}
}
