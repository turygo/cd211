package domain

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestIsSensitiveError(t *testing.T) {
	uri := "magnet:?xt=urn:btih:abcdef0123456789abcdef0123456789abcdef01"
	tests := []struct {
		name          string
		errorText     string
		submissionURI string
		want          bool
	}{
		{"empty text", "", uri, false},
		{"plain failure", "local deletion failed", "", false},
		{"exact submission uri", "clouddrive ensure_offline: " + uri + " rejected", uri, true},
		{"magnet marker", "invalid magnet:?xt=urn:btih:bad", "", true},
		{"tracker marker", "tracker announce failed", "", true},
		{"tracker uppercase", "TRACKER timed out", "", true},
		{"passkey marker", "passkey=secret rejected", "", true},
		{"token marker", "invalid token", "", true},
		{"sid marker", "session sid=abc expired", "", true},
		{"authorization marker", "authorization header missing", "", true},
		{"bearer marker", "bearer token rejected", "", true},
		{"cookie marker", "cookie expired", "", true},
		{"secret marker", "this is a secret value", "", true},
		{"password marker", "wrong password", "", true},
		{"token inside word", "tokenized error text", "", true},
		{"whitespace around marker", "  connection lost: TOKEN  ", "", true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsSensitiveError(test.errorText, test.submissionURI); got != test.want {
				t.Errorf("IsSensitiveError(%q, %q) = %t, want %t", test.errorText, test.submissionURI, got, test.want)
			}
		})
	}
}

func TestSanitizeErrorText(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		if got := SanitizeErrorText("   \n\t", ""); got != "" {
			t.Errorf("SanitizeErrorText(blank) = %q, want empty", got)
		}
	})

	t.Run("trims and keeps plain text", func(t *testing.T) {
		if got := SanitizeErrorText("  plain failure  ", ""); got != "plain failure" {
			t.Errorf("SanitizeErrorText(plain) = %q, want trimmed text", got)
		}
	})

	t.Run("redacts sensitive text", func(t *testing.T) {
		got := SanitizeErrorText("rejected: passkey=do-not-leak", "")
		if got != RedactedErrorText {
			t.Errorf("SanitizeErrorText(sensitive) = %q, want %q", got, RedactedErrorText)
		}
	})

	t.Run("redacts exact submission uri", func(t *testing.T) {
		uri := "magnet:?xt=urn:btih:secret-hash"
		got := SanitizeErrorText("offline failed for "+uri, uri)
		if got != RedactedErrorText {
			t.Errorf("SanitizeErrorText(uri) = %q, want %q", got, RedactedErrorText)
		}
	})

	t.Run("truncates to 1024 bytes without splitting a rune", func(t *testing.T) {
		long := strings.Repeat("界", 600) // 1800 bytes
		got := SanitizeErrorText(long, "")
		if len(got) > 1024 {
			t.Errorf("SanitizeErrorText length = %d, want at most 1024", len(got))
		}
		if !utf8.ValidString(got) {
			t.Errorf("SanitizeErrorText output is not valid UTF-8")
		}
		if got != long[:len(got)] {
			t.Errorf("SanitizeErrorText must be a byte prefix of the input")
		}
		if len(got) < 1020 {
			t.Errorf("SanitizeErrorText length = %d, want near 1024", len(got))
		}
	})
}

func TestSanitizeDownloadError(t *testing.T) {
	tests := []struct {
		name  string
		field func(*Download)
		want  string
	}{
		{"cloud folder", func(d *Download) {
			d.CloudFolder = "/cloud/folder"
			d.LastError = "copy failed for " + d.CloudFolder
		}, RedactedErrorText},
		{"save path", func(d *Download) {
			d.SavePath = "/local/save"
			d.LastError = "copy failed for " + d.SavePath
		}, RedactedErrorText},
		{"cloud result path", func(d *Download) {
			d.CloudResultPath = "/cloud/source"
			d.LastError = "copy failed for " + d.CloudResultPath
		}, RedactedErrorText},
		{"content path", func(d *Download) {
			d.ContentPath = "/local/content"
			d.LastError = "copy failed for " + d.ContentPath
		}, RedactedErrorText},
		{"plain error", func(d *Download) { d.LastError = "  local deletion failed  " }, "local deletion failed"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			download := Download{}
			test.field(&download)
			if got := SanitizeDownloadError(download); got != test.want {
				t.Errorf("SanitizeDownloadError() = %q, want %q", got, test.want)
			}
		})
	}
}
