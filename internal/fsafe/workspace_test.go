package fsafe

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

const (
	workspaceHashA = "0123456789abcdef0123456789abcdef01234567"
	workspaceHashB = "fedcba9876543210fedcba9876543210fedcba98"
)

func TestWorkspacePathValidatesExactDerivation(t *testing.T) {
	save := "/var/lib/cd211"
	got, err := WorkspacePath(save, workspaceHashA)
	if err != nil {
		t.Fatalf("WorkspacePath() error = %v", err)
	}
	want := filepath.Join(save, ".cd211", workspaceHashA)
	if got != want {
		t.Fatalf("WorkspacePath() = %q, want %q", got, want)
	}

	for _, test := range []struct {
		name string
		save string
		hash string
	}{
		{name: "relative save", save: "relative", hash: workspaceHashA},
		{name: "unclean save", save: "/var/lib/../cd211", hash: workspaceHashA},
		{name: "short hash", save: save, hash: "0123"},
		{name: "uppercase hash", save: save, hash: "0123456789ABCDEF0123456789abcdef01234567"},
		{name: "nonhex hash", save: save, hash: "0123456789abcdef0123456789abcdef0123456g"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := WorkspacePath(test.save, test.hash); err == nil {
				t.Fatal("WorkspacePath() accepted invalid input")
			}
		})
	}
}

func TestReservedControlComponentRejectedBeforeSaveRootMutation(t *testing.T) {
	verifier, root := newTestVerifier(t)
	reserved := mkdir(t, filepath.Join(root, ".cd211"))
	target := mkdir(t, filepath.Join(root, "target"))
	targetReserved := mkdir(t, filepath.Join(target, ".cd211"))
	alias := filepath.Join(root, "alias")
	mustSymlink(t, targetReserved, alias)
	near := mkdir(t, filepath.Join(root, ".cd211-backup"))

	for _, save := range []string{reserved, alias} {
		t.Run(filepath.Base(save), func(t *testing.T) {
			before, err := os.Stat(save)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := verifier.ResolveSaveRoot(save); err == nil {
				t.Fatal("ResolveSaveRoot() accepted a reserved save root")
			}
			if _, err := verifier.PrepareSaveRoot(save); err == nil {
				t.Fatal("PrepareSaveRoot() accepted a reserved save root")
			}
			after, err := os.Stat(save)
			if err != nil {
				t.Fatal(err)
			}
			if !os.SameFile(before, after) || before.Mode() != after.Mode() {
				t.Fatal("PrepareSaveRoot() mutated a reserved save root")
			}
			if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err == nil {
				t.Fatal("PrepareWorkspace() accepted a reserved save root")
			}
		})
	}

	want := filepath.Join(verifier.LocalRoot(), filepath.Base(near))
	resolved, exists, err := verifier.ResolveSaveRoot(near)
	if err != nil {
		t.Fatalf("ResolveSaveRoot() rejected near-name root: %v", err)
	}
	if !exists || resolved != want {
		t.Fatalf("ResolveSaveRoot() = (%q, %t), want (%q, true)", resolved, exists, want)
	}
	if _, err := verifier.PrepareSaveRoot(near); err != nil {
		t.Fatalf("PrepareSaveRoot() rejected near-name root: %v", err)
	}
	if _, err := verifier.PrepareWorkspace(near, workspaceHashA); err != nil {
		t.Fatalf("PrepareWorkspace() rejected near-name root: %v", err)
	}
}

func TestValidatePersistedRootReadOnly(t *testing.T) {
	verifier, _ := newTestVerifier(t)
	localRoot := verifier.LocalRoot()
	child := mkdir(t, filepath.Join(localRoot, "child"))
	near := mkdir(t, filepath.Join(localRoot, ".cd211-backup"))
	reserved := mkdir(t, filepath.Join(localRoot, ".cd211"))
	quarantineTarget := mkdir(t, filepath.Join(child, ".cd211", ".quarantine", workspaceHashA))
	quarantineAlias := filepath.Join(localRoot, "quarantine-alias")
	mustSymlink(t, quarantineTarget, quarantineAlias)
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	missing := filepath.Join(child, "missing")

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "local root", path: localRoot, want: localRoot},
		{name: "child", path: child, want: child},
		{name: "missing child", path: missing},
		{name: "near name", path: near, want: near},
		{name: "logical reserved component", path: reserved},
		{name: "canonical quarantine alias", path: quarantineAlias},
		{name: "outside root", path: outside},
	}

	before := make(map[string]os.FileInfo)
	for _, path := range []string{localRoot, child, near, reserved, quarantineTarget, quarantineAlias, outside} {
		info, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%q): %v", path, err)
		}
		before[path] = info
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := verifier.ValidatePersistedRoot(test.path)
			if test.want == "" {
				if err == nil {
					t.Fatalf("ValidatePersistedRoot(%q) = %q, nil error; want rejection", test.path, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidatePersistedRoot(%q): %v", test.path, err)
			}
			if got != test.want {
				t.Fatalf("ValidatePersistedRoot(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}

	if _, err := os.Lstat(missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ValidatePersistedRoot() created %q: Lstat error = %v", missing, err)
	}
	for path, want := range before {
		got, err := os.Lstat(path)
		if err != nil {
			t.Fatalf("Lstat(%q) after validation: %v", path, err)
		}
		if !os.SameFile(want, got) || want.Mode() != got.Mode() {
			t.Fatalf("ValidatePersistedRoot() mutated %q", path)
		}
	}
}

func TestInternalWorkspacePathWorksAsOperationRoot(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	workspace, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatalf("PrepareWorkspace() error = %v", err)
	}
	content := writeFile(t, filepath.Join(workspace, "content"), "content")

	verified, err := verifier.Verify(workspace, ExpectedContent{CandidateName: "content"})
	if err != nil {
		t.Fatalf("Verify() error = %v", err)
	}
	if verified.Path != content {
		t.Fatalf("Verify() path = %q, want %q", verified.Path, content)
	}
	if err := verifier.ApplyFilePlan(workspace, workspaceHashA, []FilePlan{{
		Index: 1, OriginalPath: "content", EffectivePath: "renamed", Priority: 1, Size: int64(len("content")),
	}}); err != nil {
		t.Fatalf("ApplyFilePlan() error = %v", err)
	}
	renamed := filepath.Join(workspace, "renamed")
	if err := verifier.Delete(renamed, workspace); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("DeleteWorkspace() error = %v", err)
	}
}

func TestPrepareWorkspaceCreatesIsolatedSiblingDirectories(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))

	first, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatalf("PrepareWorkspace(A) error = %v", err)
	}
	second, err := verifier.PrepareWorkspace(save, workspaceHashB)
	if err != nil {
		t.Fatalf("PrepareWorkspace(B) error = %v", err)
	}
	if first == second {
		t.Fatalf("sibling workspace paths are equal: %q", first)
	}
	if want := filepath.Join(save, ".cd211", workspaceHashA); first != want {
		t.Fatalf("PrepareWorkspace(A) = %q, want %q", first, want)
	}
	if want := filepath.Join(save, ".cd211", workspaceHashB); second != want {
		t.Fatalf("PrepareWorkspace(B) = %q, want %q", second, want)
	}

	for _, test := range []struct {
		path string
		mode os.FileMode
	}{
		{filepath.Join(save, ".cd211"), os.ModeSetgid | 0o750},
		{first, os.ModeSetgid | 0o770},
		{second, os.ModeSetgid | 0o770},
	} {
		info, err := os.Stat(test.path)
		if err != nil {
			t.Fatalf("Stat(%q): %v", test.path, err)
		}
		if !info.IsDir() {
			t.Fatalf("%q is not a directory", test.path)
		}
		if got := info.Mode() & (os.ModePerm | os.ModeSetgid | os.ModeSticky); got != test.mode {
			t.Fatalf("%q mode = %#o, want %#o", test.path, got, test.mode)
		}
	}
	saveInfo, err := os.Stat(save)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := saveInfo.Mode()&(os.ModePerm|os.ModeSetgid|os.ModeSticky), os.ModeSticky|os.ModeSetgid|0o770; got != want {
		t.Fatalf("save mode = %#o, want %#o", got, want)
	}
	if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("idempotent PrepareWorkspace(A) error = %v", err)
	}
}

func TestPrepareWorkspaceRejectsUnsafeComponents(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))

	mustSymlink(t, outside, filepath.Join(save, ".cd211"))
	if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("PrepareWorkspace() accepted a .cd211 symlink")
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted a .cd211 symlink")
	}
	if _, err := os.Lstat(filepath.Join(outside, workspaceHashA)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("symlink target changed: %v", err)
	}

	if err := os.Remove(filepath.Join(save, ".cd211")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(save, ".cd211"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("PrepareWorkspace() accepted a non-directory .cd211")
	}
}

func TestWorkspaceHashSymlinkAndSpecialFileAreRejected(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	cd := mkdir(t, filepath.Join(save, ".cd211"))
	outside := mkdir(t, filepath.Join(t.TempDir(), "outside"))
	mustSymlink(t, outside, filepath.Join(cd, workspaceHashA))
	if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("PrepareWorkspace() accepted a hash-directory symlink")
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted a hash-directory symlink")
	}

	if err := os.Remove(filepath.Join(cd, workspaceHashA)); err != nil {
		t.Fatal(err)
	}
	specialPath := filepath.Join(cd, workspaceHashA)
	if err := syscall.Mkfifo(specialPath, 0o600); err != nil {
		t.Fatalf("Mkfifo: %v", err)
	}
	if _, err := verifier.PrepareWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("PrepareWorkspace() accepted a special hash path")
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted a special hash path")
	}
}

func TestDeleteWorkspaceIsolatesSiblingsAndIsIdempotent(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("DeleteWorkspace() missing parent: %v", err)
	}
	workspaceA, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := verifier.PrepareWorkspace(save, workspaceHashB)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workspaceA, "content", "a.bin"), "A")
	writeFile(t, filepath.Join(workspaceB, "content", "b.bin"), "B")

	if err := verifier.DeleteWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("DeleteWorkspace(A): %v", err)
	}
	assertNotExist(t, workspaceA)
	assertFileContent(t, filepath.Join(workspaceB, "content", "b.bin"), "B")
	if _, err := os.Stat(filepath.Join(save, ".cd211")); err != nil {
		t.Fatalf("shared workspace parent removed: %v", err)
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("idempotent DeleteWorkspace(A): %v", err)
	}
	if err := verifier.DeleteWorkspace(save, workspaceHashB); err != nil {
		t.Fatalf("DeleteWorkspace(B): %v", err)
	}
	assertNotExist(t, workspaceB)
}

func TestDeleteWorkspaceRetriesQuarantinedEntry(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	workspaceA, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := verifier.PrepareWorkspace(save, workspaceHashB)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workspaceA, "content"), "A")
	writeFile(t, filepath.Join(workspaceB, "content"), "B")
	quarantine := filepath.Join(save, ".cd211", ".quarantine")
	mkdir(t, quarantine)
	if err := os.Rename(workspaceA, filepath.Join(quarantine, workspaceHashA)); err != nil {
		t.Fatal(err)
	}

	if err := verifier.DeleteWorkspace(save, workspaceHashA); err != nil {
		t.Fatalf("DeleteWorkspace() retry: %v", err)
	}
	assertNotExist(t, workspaceA)
	assertNotExist(t, filepath.Join(quarantine, workspaceHashA))
	assertFileContent(t, filepath.Join(workspaceB, "content"), "B")
}

func TestDeleteWorkspaceRejectsQuarantineReplacement(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	workspaceA, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	workspaceB, err := verifier.PrepareWorkspace(save, workspaceHashB)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workspaceA, "content"), "A")
	writeFile(t, filepath.Join(workspaceB, "content"), "B")
	quarantine := filepath.Join(save, ".cd211", ".quarantine")
	mkdir(t, quarantine)
	if err := os.Rename(workspaceA, filepath.Join(quarantine, workspaceHashA)); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(quarantine, workspaceHashA)); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, workspaceB, filepath.Join(quarantine, workspaceHashA))
	t.Cleanup(func() {
		if err := os.Remove(filepath.Join(quarantine, workspaceHashA)); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove malicious quarantine replacement: %v", err)
		}
	})

	if err := verifier.DeleteWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted a replaced quarantined workspace")
	}
	assertFileContent(t, filepath.Join(workspaceB, "content"), "B")
}

func TestWorkspaceLifecycleRejectsSaveRootOutsideLocalRoot(t *testing.T) {
	verifier, root := newTestVerifier(t)
	outside := mkdir(t, filepath.Join(t.TempDir(), "save"))
	if _, err := verifier.PrepareWorkspace(outside, workspaceHashA); err == nil {
		t.Fatal("PrepareWorkspace() accepted an invalid save root")
	}
	if err := verifier.DeleteWorkspace(outside, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted an invalid save root")
	}

	workspace, err := verifier.PrepareWorkspace(root, workspaceHashA)
	if err != nil {
		t.Fatalf("PrepareWorkspace() rejected the default root: %v", err)
	}
	if want := filepath.Join(root, ".cd211", workspaceHashA); workspace != want {
		t.Fatalf("PrepareWorkspace() = %q, want %q", workspace, want)
	}
	if err := verifier.DeleteWorkspace(root, workspaceHashA); err != nil {
		t.Fatalf("DeleteWorkspace() default root: %v", err)
	}
}

func TestDeleteWorkspaceRejectsReplacementBeforeRemoval(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	workspace, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(workspace, "content"), "keep")
	replacement := mkdir(t, filepath.Join(root, "replacement"))
	if err := os.Rename(filepath.Join(save, ".cd211"), filepath.Join(replacement, ".cd211")); err != nil {
		t.Fatal(err)
	}
	mustSymlink(t, replacement, filepath.Join(save, ".cd211"))
	if err := verifier.DeleteWorkspace(save, workspaceHashA); err == nil {
		t.Fatal("DeleteWorkspace() accepted a replaced .cd211 directory")
	}
	assertFileContent(t, filepath.Join(replacement, ".cd211", workspaceHashA, "content"), "keep")
}

func TestPrepareWorkspaceThroughSaveRootSymlinkPreservesLogicalPath(t *testing.T) {
	verifier, root := newTestVerifier(t)
	physical := mkdir(t, filepath.Join(root, "physical"))
	alias := filepath.Join(root, "alias")
	mustSymlink(t, physical, alias)
	got, err := verifier.PrepareWorkspace(alias, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(alias, ".cd211", workspaceHashA)
	if got != want {
		t.Fatalf("PrepareWorkspace() = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(physical, ".cd211", workspaceHashA)); err != nil {
		t.Fatalf("prepared canonical workspace missing: %v", err)
	}
}

func TestWorkspaceModeUnderRestrictiveUmask(t *testing.T) {
	verifier, root := newTestVerifier(t)
	save := mkdir(t, filepath.Join(root, "save"))
	previousUmask := syscall.Umask(0o022)
	defer syscall.Umask(previousUmask)
	workspace, err := verifier.PrepareWorkspace(save, workspaceHashA)
	if err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(workspace); err != nil {
		t.Fatal(err)
	} else if info.Mode().Perm() != 0o770 {
		t.Fatalf("workspace permissions = %#o, want %#o", info.Mode().Perm(), 0o770)
	}
}
