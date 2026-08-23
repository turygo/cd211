package fsafe

import (
	"os"
	"path/filepath"
	"testing"
)

func TestApplyFilePlanRenameAndExclude(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "task")
	if err := os.Mkdir(candidate, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "old.txt"), []byte("data"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "drop.txt"), []byte("drop"), 0o660); err != nil {
		t.Fatal(err)
	}
	verifier, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := verifier.ApplyFilePlan(candidate, "0123456789abcdef0123456789abcdef01234567", []FilePlan{{Index: 0, OriginalPath: "old.txt", EffectivePath: "new.txt", Priority: 1, Size: 4}, {Index: 1, OriginalPath: "drop.txt", EffectivePath: "drop.txt", Priority: 0, Size: 4}}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "new.txt")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(candidate, "drop.txt")); !os.IsNotExist(err) {
		t.Fatalf("excluded file still exists: %v", err)
	}
}
func TestApplyFilePlanSupportsSwapAndRejectsCollision(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "task")
	if err := os.Mkdir(candidate, 0o770); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "a"), []byte("A"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "b"), []byte("B"), 0o660); err != nil {
		t.Fatal(err)
	}
	verifier, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	plans := []FilePlan{{Index: 0, OriginalPath: "a", EffectivePath: "b", Priority: 1, Size: 1}, {Index: 1, OriginalPath: "b", EffectivePath: "a", Priority: 1, Size: 1}}
	if err := verifier.ApplyFilePlan(candidate, "fedcba9876543210fedcba9876543210fedcba98", plans); err != nil {
		t.Fatal(err)
	}
	a, err := os.ReadFile(filepath.Join(candidate, "a"))
	if err != nil || string(a) != "B" {
		t.Fatalf("a = %q, %v", a, err)
	}
	b, err := os.ReadFile(filepath.Join(candidate, "b"))
	if err != nil || string(b) != "A" {
		t.Fatalf("b = %q, %v", b, err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "occupied"), []byte("keep"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(candidate, "source"), []byte("source"), 0o660); err != nil {
		t.Fatal(err)
	}
	if err := verifier.ApplyFilePlan(candidate, "0123456789abcdef0123456789abcdef01234567", []FilePlan{{Index: 2, OriginalPath: "source", EffectivePath: "occupied", Priority: 1, Size: 6}}); err == nil {
		t.Fatal("existing target collision accepted")
	}
}
