package httpapi

import "testing"

func TestNormalizeTagsCanonicalizesInput(t *testing.T) {
	got, ok := normalizeTags("  anime, ,anime, drama ", true)
	if !ok || got != "anime,drama" {
		t.Fatalf("normalizeTags() = %q, %v", got, ok)
	}
}

func TestNormalizeTagsRejectsControlCharacters(t *testing.T) {
	if _, ok := normalizeTags("anime,drama\nsecret", true); ok {
		t.Fatal("normalizeTags accepted control character")
	}
}
