package web

import "testing"

func TestLocalTimeHelpers(t *testing.T) {
	const value = "2026-08-06T12:00:00Z"
	if got, want := string(localTime(value)), `<time class="local-time" data-local-time datetime="2026-08-06T12:00:00Z">2026-08-06T12:00:00Z</time>`; got != want {
		t.Fatalf("localTime() = %q, want %q", got, want)
	}
	if got, want := string(localTime("Not recorded")), "Not recorded"; got != want {
		t.Fatalf("localTime(non-ISO) = %q, want %q", got, want)
	}
	if got, want := string(localTimeFormat("Updated %s", value)), `Updated <time class="local-time" data-local-time datetime="2026-08-06T12:00:00Z">2026-08-06T12:00:00Z</time>`; got != want {
		t.Fatalf("localTimeFormat() = %q, want %q", got, want)
	}
}
