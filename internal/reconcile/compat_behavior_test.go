package reconcile

import "testing"

func TestSafeNameRejectsPathTraversal(t *testing.T) {
	for _, value := range []string{"", ".", "..", "../escape", "dir/file", "dir\\file"} {
		if safeName(value) {
			t.Fatalf("safeName(%q) accepted unsafe name", value)
		}
	}
	if !safeName("episode.mkv") {
		t.Fatal("safeName rejected regular name")
	}
}
