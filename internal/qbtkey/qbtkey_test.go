package qbtkey

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestGenerateShapeHintAndVerify(t *testing.T) {
	secret, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if !Valid(secret) {
		t.Fatalf("generated secret %q is not valid", secret)
	}
	if !strings.HasPrefix(string(secret), Prefix) {
		t.Fatalf("generated secret = %q, want prefix %q", secret, Prefix)
	}
	if got, want := len(secret), SecretLength; got != want {
		t.Errorf("generated secret length = %d, want %d", got, want)
	}
	if got, want := Hint(secret), "qbt_…"+string(secret[len(secret)-HintSuffix:]); got != want {
		t.Errorf("Hint() = %q, want %q", got, want)
	}
	digest := Hash(secret)
	if len(digest) != 32 || bytes.Equal(digest, []byte(secret)) {
		t.Fatalf("Hash() = %x, want a 32-byte digest distinct from plaintext", digest)
	}
	if !Verify(secret, digest) {
		t.Error("Verify() rejected the generated secret")
	}
	if Verify(Secret(string(secret)+"x"), digest) || Verify(Secret("qbt_"+string(secret[4:])), digest[:31]) {
		t.Error("Verify() accepted malformed secret or digest")
	}
}

func TestValidRejectsMalformedShapes(t *testing.T) {
	valid, err := Generate()
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	cases := []Secret{
		"",
		"qbt_",
		Secret(string(valid)[:len(valid)-1]),
		Secret("qbt_" + strings.Repeat("A", 42) + "!"),
		Secret("QBT_" + string(valid[4:])),
		Secret("qbt-" + string(valid[4:])),
	}
	for _, tc := range cases {
		if Valid(tc) {
			t.Errorf("Valid(%q) = true, want false", tc)
		}
		if Hint(tc) != "" {
			t.Errorf("Hint(%q) = %q, want empty", tc, Hint(tc))
		}
	}
}

func TestSentinelErrorsAreDistinctAndExplicit(t *testing.T) {
	if errors.Is(ErrNotFound, ErrConflict) || errors.Is(ErrConflict, ErrNotFound) {
		t.Fatal("qBittorrent API key sentinel errors must be distinct")
	}
	if !strings.Contains(ErrNotFound.Error(), "qBittorrent API key") ||
		!strings.Contains(ErrConflict.Error(), "qBittorrent API key") {
		t.Fatal("qBittorrent API key errors must identify their domain")
	}
}
