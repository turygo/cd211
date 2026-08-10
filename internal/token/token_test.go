package token

import (
	"crypto/sha256"
	"strings"
	"testing"
)

func TestGenerateShapeAndUniqueness(t *testing.T) {
	first, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	second, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if first == second {
		t.Fatal("Generate() returned the same token twice")
	}
	if !strings.HasPrefix(string(first), Prefix) {
		t.Errorf("token = %q, want prefix %q", first, Prefix)
	}
	if len(first) != SecretLength {
		t.Errorf("token length = %d, want %d", len(first), SecretLength)
	}
	if !Valid(first) {
		t.Errorf("generated token %q fails Valid()", first)
	}
}

func TestHashAndVerify(t *testing.T) {
	secret, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	digest := Hash(secret)
	if len(digest) != sha256.Size {
		t.Fatalf("Hash() length = %d, want %d", len(digest), sha256.Size)
	}
	if !Verify(secret, digest) {
		t.Error("Verify(secret, Hash(secret)) = false, want true")
	}

	other, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if Verify(other, digest) {
		t.Error("Verify(other token, digest) = true, want false")
	}
	if Verify(secret, nil) {
		t.Error("Verify(secret, nil digest) = true, want false")
	}
	if Verify(secret, digest[:16]) {
		t.Error("Verify(secret, truncated digest) = true, want false")
	}
}

func TestVerifyRejectsMalformedTokensBeforeComparison(t *testing.T) {
	secret, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	digest := Hash(secret)

	malformed := []Secret{
		"", // empty
		Secret("other_api_" + string(secret[len(Prefix):])), // wrong prefix
		"cd211_api_",                                        // prefix only
		"cd211_api_short",                                   // wrong length
		Prefix + "not base64url!!!",                         // bad encoding at fixed length
		Secret(Prefix + strings.Repeat("A", 43)),            // valid base64, wrong payload size
		Secret(string(secret) + "x"),                        // too long
		Secret("CD211_API_" + string(secret[len(Prefix):])), // case-sensitive prefix
	}
	for _, value := range malformed {
		if Verify(value, digest) {
			t.Errorf("Verify(%q, digest) = true, want false for a malformed token", value)
		}
	}
}

func TestHint(t *testing.T) {
	secret, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	hint := Hint(secret)
	if !strings.HasPrefix(hint, Prefix+"…") {
		t.Errorf("hint = %q, want %q prefix with ellipsis", hint, Prefix+"…")
	}
	if !strings.HasSuffix(hint, string(secret[len(secret)-HintSuffix:])) {
		t.Errorf("hint = %q, want suffix %q", hint, string(secret[len(secret)-HintSuffix:]))
	}
	if Hint("malformed") != "" {
		t.Error("Hint(malformed) = non-empty, want empty")
	}
}
