package submission

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/domain"
	"github.com/turygo/cd211/internal/fsafe"
	"github.com/turygo/cd211/internal/store"
	"github.com/turygo/cd211/internal/torrentmeta"
)

type serviceClock struct{ now time.Time }

func (c serviceClock) Now() time.Time { return c.now }

type serviceWaker struct{ wakes int }

func (w *serviceWaker) Wake() { w.wakes++ }

type serviceFilesystem struct {
	content string
	size    int64
	err     error
}

func (f *serviceFilesystem) Verify(string, fsafe.ExpectedContent) (fsafe.VerifiedContent, error) {
	return fsafe.VerifiedContent{Path: f.content, Size: f.size}, f.err
}

type serviceHarness struct {
	service    *Service
	repository *store.Store
	clock      serviceClock
	waker      *serviceWaker
	filesystem *serviceFilesystem
	limits     torrentmeta.Limits
}

func newServiceHarness(t *testing.T) *serviceHarness {
	t.Helper()
	clock := serviceClock{now: time.Date(2026, 8, 6, 12, 0, 0, 0, time.UTC)}
	repository, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "submission.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repository.Close() })
	limits := torrentmeta.Limits{MaxInputBytes: 1 << 20, MaxInfoBytes: 1 << 18, MaxFiles: 16, MaxNameBytes: 255, MaxPathBytes: 1024, MaxComponentBytes: 255, MaxTrackerCount: 16, MaxTrackerBytes: 1024, MaxTotalSize: 1 << 30}
	waker := &serviceWaker{}
	filesystem := &serviceFilesystem{err: fs.ErrNotExist}
	service, err := New(Config{
		CloudRoot: "/cloud", LocalRoot: "/local",
		TorrentLimits: limits,
	}, repository, clock, waker, filesystem)
	if err != nil {
		t.Fatal(err)
	}
	return &serviceHarness{service: service, repository: repository, clock: clock, waker: waker, filesystem: filesystem, limits: limits}
}

func (h *serviceHarness) upsertCategory(t *testing.T, name string, enabled bool) {
	t.Helper()
	now := h.clock.now
	if _, err := h.repository.UpsertCategory(context.Background(), domain.Category{
		Name: name, CloudPath: "/cloud/" + name, SavePath: "/local/" + name, Enabled: enabled, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
}

func magnet(hash, name string) string {
	return "magnet:?xt=urn:btih:" + hash + "&dn=" + name
}

func TestSubmitMagnetCreatesAndWakesOnce(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	hash := "0123456789abcdef0123456789abcdef01234567"
	created, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Example"), "", false)
	if err != nil || !inserted {
		t.Fatalf("SubmitMagnet() = (%+v, %t, %v), want created", created, inserted, err)
	}
	if created.Hash != hash || created.Name != "Example" || created.Category != "" ||
		created.CloudFolder != "/cloud" || created.SavePath != "/local" ||
		created.State != domain.StateAccepted || created.SourceKind != domain.SourceMagnet ||
		created.SubmissionURI == "" || created.NextRunAt == nil {
		t.Fatalf("created download = %+v", created)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("wakes = %d, want 1", harness.waker.wakes)
	}

	duplicate, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Example"), "", false)
	if err != nil || inserted || duplicate.Hash != hash {
		t.Fatalf("duplicate SubmitMagnet() = (%+v, %t, %v), want existing", duplicate, inserted, err)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("duplicate wakes = %d, want still 1", harness.waker.wakes)
	}
}

func TestSubmitMagnetStoppedOverridesToStopped(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	hash := "1111111111111111111111111111111111111111"
	created, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Stopped"), "", true)
	if err != nil || !inserted {
		t.Fatalf("SubmitMagnet(stopped) = (%+v, %t, %v)", created, inserted, err)
	}
	if created.State != domain.StateStopped || created.NextRunAt != nil {
		t.Fatalf("stopped download = %+v, want STOPPED with no next run", created)
	}
}

func TestSubmitMagnetCategoryRules(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	harness.upsertCategory(t, "movies", true)
	harness.upsertCategory(t, "disabled", false)

	// Canonicalization applies before lookup: uppercase matches the lowercase
	// stored name and the canonical name is persisted.
	hash := "2222222222222222222222222222222222222222"
	created, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Categorized"), "TV", false)
	if !errors.Is(err, ErrCategoryInvalid) {
		t.Fatalf("missing category error = %v, want ErrCategoryInvalid", err)
	}
	if inserted {
		t.Fatal("missing category created a download")
	}
	created, inserted, err = harness.service.SubmitMagnet(context.Background(), magnet(hash, "Categorized"), "MOVIES", false)
	if err != nil || !inserted {
		t.Fatalf("categorized SubmitMagnet() = (%+v, %t, %v)", created, inserted, err)
	}
	if created.Category != "movies" || created.CloudFolder != "/cloud/movies" || created.SavePath != "/local/movies" {
		t.Fatalf("categorized download = %+v", created)
	}

	// Disabled and syntactically invalid categories are the same typed error.
	for _, category := range []string{"disabled", "bad/category", ".."} {
		if _, _, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Categorized"), category, false); !errors.Is(err, ErrCategoryInvalid) {
			t.Errorf("category %q error = %v, want ErrCategoryInvalid", category, err)
		}
	}
}

func TestSubmitMagnetInvalidSourceErrors(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	for _, raw := range []string{
		"",
		"not a magnet",
		"magnet:?xt=urn:btih:invalid",
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567\nmagnet:?xt=urn:btih:1111111111111111111111111111111111111111",
	} {
		if _, _, err := harness.service.SubmitMagnet(context.Background(), raw, "", false); !errors.Is(err, ErrInvalidSource) {
			t.Errorf("magnet %q error = %v, want ErrInvalidSource", raw, err)
		}
	}
	if harness.waker.wakes != 0 {
		t.Fatalf("invalid submissions woke %d times, want 0", harness.waker.wakes)
	}
}

func TestSubmitTorrentPersistsFiles(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	torrent := []byte("d4:infod6:lengthi3e4:name4:demo12:piece lengthi16384e6:pieces20:01234567890123456789ee")
	metadata, err := torrentmeta.ParseTorrent(torrent, harness.limits)
	if err != nil {
		t.Fatal(err)
	}
	created, inserted, err := harness.service.SubmitTorrent(context.Background(), torrent, "", false)
	if err != nil || !inserted {
		t.Fatalf("SubmitTorrent() = (%+v, %t, %v)", created, inserted, err)
	}
	if created.Hash != metadata.Hash || created.SourceKind != domain.SourceTorrent ||
		created.IsMultiFile == nil || *created.IsMultiFile || created.TotalSize != metadata.TotalSize {
		t.Fatalf("torrent download = %+v", created)
	}
	files, err := harness.repository.ListDownloadFiles(context.Background(), created.Hash)
	if err != nil || len(files) != 1 || files[0].RelativePath != "demo" || files[0].Size != 3 {
		t.Fatalf("torrent files = %+v, %v", files, err)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("wakes = %d, want 1", harness.waker.wakes)
	}
}

func TestSubmitRevivesRetainedContent(t *testing.T) {
	t.Parallel()
	harness := newServiceHarness(t)
	harness.upsertCategory(t, "movies", true)
	hash := "3333333333333333333333333333333333333333"
	now := harness.clock.now

	// Drive the download to DELETED with retained content evidence.
	_, _, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Retained"), "movies", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	claim, err := harness.repository.ClaimDue(context.Background(), "service-test", now.Add(time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("ClaimDue() = (%+v, %v)", claim, err)
	}
	deleted := claim.Download
	deleted.State = domain.StateDeleted
	deleted.NextRunAt = nil
	deleted.UpdatedAt = now.Add(time.Minute)
	multiFile := false
	deleted.IsMultiFile = &multiFile
	deleted.ContentPath = "/local/movies/Retained"
	deleted.CloudSourcePath = "/cloud/movies/Retained"
	if err := harness.repository.CommitClaim(context.Background(), *claim, deleted); err != nil {
		t.Fatal(err)
	}

	// A matching submission verifies the retained tree and revives it.
	harness.filesystem.content = "/local/movies/Retained"
	harness.filesystem.size = 42
	harness.filesystem.err = nil
	revived, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Ignored"), "movies", false)
	if err != nil || !inserted {
		t.Fatalf("revive SubmitMagnet() = (%+v, %t, %v)", revived, inserted, err)
	}
	if revived.State != domain.StateVerifyingLocal || revived.ContentPath != "/local/movies/Retained" ||
		revived.CloudSourcePath != "/cloud/movies/Retained" || revived.Name != "Retained" ||
		revived.TotalSize != 42 || revived.LastUpstreamStatus != domain.UpstreamRetainedContent ||
		revived.QbitProgress != 0.99 || revived.CopyProgress != 1 {
		t.Fatalf("revived download lost retained evidence: %+v", revived)
	}
	// The revived download also wakes the reconciler.
	if harness.waker.wakes != 2 {
		t.Fatalf("wakes = %d, want 2 (create + revive)", harness.waker.wakes)
	}

	// Delete again; when verification then fails, the submission is revived as
	// a plain accepted row without retained evidence.
	if err := harness.repository.RequestDelete(context.Background(), []string{hash}, false, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	claim, err = harness.repository.ClaimDue(context.Background(), "service-test", now.Add(2*time.Minute), time.Minute)
	if err != nil || claim == nil {
		t.Fatalf("second ClaimDue() = (%+v, %v)", claim, err)
	}
	deleted = claim.Download
	deleted.State = domain.StateDeleted
	deleted.NextRunAt = nil
	deleted.UpdatedAt = now.Add(2 * time.Minute)
	if err := harness.repository.CommitClaim(context.Background(), *claim, deleted); err != nil {
		t.Fatal(err)
	}
	harness.filesystem.err = fs.ErrNotExist
	plain, inserted, err := harness.service.SubmitMagnet(context.Background(), magnet(hash, "Plain"), "movies", false)
	if err != nil || !inserted {
		t.Fatalf("plain revive SubmitMagnet() = (%+v, %t, %v)", plain, inserted, err)
	}
	if plain.State != domain.StateAccepted || plain.ContentPath != "" || plain.TotalSize != 0 {
		t.Fatalf("unverified submission = %+v, want plain accepted row", plain)
	}
	if harness.waker.wakes != 3 {
		t.Fatalf("wakes = %d, want 3", harness.waker.wakes)
	}
}

func TestNewRejectsInvalidConfiguration(t *testing.T) {
	clock := serviceClock{}
	waker := &serviceWaker{}
	files := &serviceFilesystem{}
	valid := Config{CloudRoot: "/cloud", LocalRoot: "/local", TorrentLimits: torrentmeta.Limits{MaxInputBytes: 1, MaxInfoBytes: 1, MaxFiles: 1, MaxNameBytes: 1, MaxPathBytes: 1, MaxComponentBytes: 1, MaxTrackerCount: 1, MaxTrackerBytes: 1, MaxTotalSize: 1}}
	if service, err := New(valid, (*store.Store)(nil), clock, waker, files); err == nil || service != nil {
		t.Errorf("New(typed-nil repo) = (%v, %v), want error", service, err)
	}
	if service, err := New(valid, stubRepository{}, nil, waker, files); err == nil || service != nil {
		t.Errorf("New(nil clock) = (%v, %v), want error", service, err)
	}
	if service, err := New(Config{CloudRoot: "relative", LocalRoot: "/local", TorrentLimits: valid.TorrentLimits}, stubRepository{}, clock, waker, files); err == nil || service != nil {
		t.Errorf("New(relative cloud root) = (%v, %v), want error", service, err)
	}
	badLimits := valid.TorrentLimits
	badLimits.MaxInputBytes = 0
	if service, err := New(Config{CloudRoot: "/cloud", LocalRoot: "/local", TorrentLimits: badLimits}, stubRepository{}, clock, waker, files); err == nil || service != nil {
		t.Errorf("New(invalid limits) = (%v, %v), want error", service, err)
	}
}

type stubRepository struct{}

func (stubRepository) GetCategory(context.Context, string) (domain.Category, error) {
	return domain.Category{}, nil
}
func (stubRepository) GetDownload(context.Context, string) (domain.Download, error) {
	return domain.Download{}, nil
}
func (stubRepository) CreateSubmission(context.Context, domain.Submission) (domain.Download, bool, error) {
	return domain.Download{}, false, nil
}

func TestCanonicalCategory(t *testing.T) {
	for _, test := range []struct {
		raw        string
		allowEmpty bool
		want       string
		wantOK     bool
	}{
		{"", true, "", true},
		{"  ", true, "", true},
		{"", false, "", false},
		{"TV", true, "tv", true},
		{"  Movies  ", true, "movies", true},
		{".", true, "", false},
		{"..", true, "", false},
		{"bad/category", true, "", false},
		{"bad\\category", true, "", false},
		{"bad\x00category", true, "", false},
		{"\xff", true, "", false},
	} {
		got, ok := CanonicalCategory(test.raw, test.allowEmpty)
		if got != test.want || ok != test.wantOK {
			t.Errorf("CanonicalCategory(%q, %t) = (%q, %t), want (%q, %t)", test.raw, test.allowEmpty, got, ok, test.want, test.wantOK)
		}
	}
	if _, ok := CanonicalCategory(strings.Repeat("a", 1<<20), true); !ok {
		t.Error("long valid category rejected")
	}
}
