package outbox

import (
	"errors"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits for mutable endpoint identity.
const (
	MaxEndpointNameBytes = 64
	MaxEndpointURLBytes  = 2048
	MaxBearerTokenBytes  = 4096
)

// ValidateEndpointInput verifies the mutable endpoint identity: trimmed name
// of 1-64 UTF-8 bytes with no control characters, absolute http/https URL of
// at most 2048 bytes without userinfo or fragments, at least one subscription,
// and an optional trimmed bearer token of at most 4096 bytes.
func ValidateEndpointInput(input EndpointInput) error {
	name := strings.TrimSpace(input.Name)
	if name == "" || !utf8.ValidString(name) || len(name) > MaxEndpointNameBytes || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return errors.New("endpoint name is invalid")
	}
	if len(input.URL) > MaxEndpointURLBytes {
		return errors.New("endpoint URL is too long")
	}
	parsed, err := url.Parse(input.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return errors.New("endpoint URL must be an absolute http or https URL")
	}
	if parsed.User != nil {
		return errors.New("endpoint URL must not contain userinfo")
	}
	if parsed.Fragment != "" {
		return errors.New("endpoint URL must not contain a fragment")
	}
	if !input.SubscribeCompleted && !input.SubscribeFailed {
		return errors.New("at least one event subscription is required")
	}
	if len(strings.TrimSpace(input.BearerToken)) > MaxBearerTokenBytes {
		return errors.New("bearer token is too long")
	}
	return nil
}

// DisplayURL redacts the query portion of a stored endpoint URL for ordinary
// UI reads: the query is replaced by the fixed "?…" marker. URLs without a
// query are returned unchanged so edit forms may prefill them.
func DisplayURL(raw string) string {
	if index := strings.IndexByte(raw, '?'); index >= 0 {
		return raw[:index] + "?…"
	}
	return raw
}
