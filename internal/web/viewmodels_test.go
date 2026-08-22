package web

import (
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
)

func TestDisplayAgeUsesLocalizedUnits(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	updated := now.Add(-9 * 24 * time.Hour)
	if got := displayAge(now, updated, tr(LangZH)); got != "9天" {
		t.Fatalf("Chinese age = %q, want 9天", got)
	}
	if got := displayAge(now, updated, tr(LangEN)); got != "9d" {
		t.Fatalf("English age = %q, want 9d", got)
	}
}

func TestDisplayDownloadDurationExcludesQueueAndStopsAtCopyCompletion(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	started := now.Add(-9*24*time.Hour - 3*time.Hour - 12*time.Minute - 5*time.Second)
	completed := now
	download := domain.Download{OfflineStartedAt: &started, CopyCompletedAt: &completed}
	if got := displayDownloadDuration(download, now, tr(LangZH)); got != "9天 3小时 12分 5秒" {
		t.Fatalf("duration = %q, want 9天 3小时 12分 5秒", got)
	}
}

func TestDisplayDownloadDurationReportsUnavailableWithoutOfflineStart(t *testing.T) {
	if got := displayDownloadDuration(domain.Download{}, time.Now(), tr(LangZH)); got != "暂无数据" {
		t.Fatalf("missing duration = %q, want 暂无数据", got)
	}
}
