package domain

import (
	"strings"
	"unicode/utf8"
)

// RedactedErrorText is the fixed value persisted in event payloads and shown
// in the Web UI when a stored error message contains sensitive markers. It is
// deliberately a fixed English value shared by all consumers.
const RedactedErrorText = "error details redacted"

// maxErrorTextBytes bounds sanitized error text in event payloads.
const maxErrorTextBytes = 1024

// sensitiveErrorMarkers are matched case-insensitively against trimmed error
// text. They cover magnets, tracker passkeys, bearer tokens, and cloud
// credentials; the Web UI and event payload creation share this predicate so
// redaction cannot drift between the two surfaces.
var sensitiveErrorMarkers = []string{
	"magnet:",
	"tracker",
	"passkey",
	"token",
	"sid=",
	"authorization",
	"bearer",
	"cookie",
	"secret",
	"password",
}

// IsSensitiveError reports whether errorText must be redacted before it is
// rendered or persisted. It is sensitive when it contains the exact non-empty
// submissionURI or case-insensitively contains any sensitivity marker.
func IsSensitiveError(errorText, submissionURI string) bool {
	trimmed := strings.TrimSpace(errorText)
	if trimmed == "" {
		return false
	}
	if submissionURI != "" && strings.Contains(trimmed, submissionURI) {
		return true
	}
	lower := strings.ToLower(trimmed)
	for _, marker := range sensitiveErrorMarkers {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

// SanitizeErrorText prepares an error message for durable event payloads: it
// trims whitespace, redacts sensitive text to RedactedErrorText, and truncates
// the remainder to at most 1024 UTF-8 bytes without splitting a rune.
func SanitizeErrorText(errorText, submissionURI string) string {
	trimmed := strings.TrimSpace(errorText)
	if trimmed == "" {
		return ""
	}
	if IsSensitiveError(trimmed, submissionURI) {
		return RedactedErrorText
	}
	if len(trimmed) <= maxErrorTextBytes {
		return trimmed
	}
	cut := trimmed[:maxErrorTextBytes]
	for !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// SanitizeDownloadError prepares a persisted download error for external
// sinks. Errors that include any frozen path are redacted before applying the
// shared marker, submission URI, trimming, and truncation semantics.
func SanitizeDownloadError(download Download) string {
	trimmed := strings.TrimSpace(download.LastError)
	for _, path := range []string{
		download.CloudFolder,
		download.SavePath,
		download.CloudSourcePath,
		download.ContentPath,
	} {
		if path = strings.TrimSpace(path); path != "" && strings.Contains(trimmed, path) {
			return RedactedErrorText
		}
	}
	return SanitizeErrorText(trimmed, download.SubmissionURI)
}
