package submission

import (
	"context"
	"testing"
)

func TestSubmitMagnetOptionsPersistBeforeWake(t *testing.T) {
	harness := newServiceHarness(t)
	hash := "abcdefabcdefabcdefabcdefabcdefabcdefabcd"
	download, inserted, err := harness.service.SubmitMagnetWithOptions(context.Background(), magnet(hash, "Original"), "", false, Options{RenameSet: true, Rename: "Renamed", TagsSet: true, Tags: "anime, drama", AutoTMMSet: true})
	if err != nil || !inserted {
		t.Fatalf("submit options = (%+v, %t, %v)", download, inserted, err)
	}
	if download.Name != "Renamed" || download.Tags != "anime,drama" || download.AutoTMM {
		t.Fatalf("download options = %+v", download)
	}
	if harness.waker.wakes != 1 {
		t.Fatalf("wake count = %d", harness.waker.wakes)
	}
	stored, err := harness.repository.GetDownload(context.Background(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Name != "Renamed" || !stored.NameOverridden || stored.Tags != "anime,drama" || stored.AutoTMM {
		t.Fatalf("stored options = %+v", stored)
	}
}
