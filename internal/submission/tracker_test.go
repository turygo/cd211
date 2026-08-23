package submission

import (
	"testing"

	"github.com/turygo/cd211/internal/torrentmeta"
)

func TestGlobalTrackerMergePreservesSourceOrder(t *testing.T) {
	limits := torrentmeta.Limits{MaxInputBytes: 1024, MaxInfoBytes: 1024, MaxFiles: 4, MaxNameBytes: 255, MaxPathBytes: 1024, MaxComponentBytes: 255, MaxTrackerCount: 4, MaxTrackerBytes: 256, MaxTotalSize: 1 << 20}
	magnet, err := torrentmeta.AddTrackers("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&dn=demo&tr=https%3A%2F%2Fsource.example%2Fa", []string{"https://global.example/b", "https://source.example/a"}, limits)
	if err != nil {
		t.Fatal(err)
	}
	result, err := torrentmeta.ParseMagnet(magnet, limits)
	if err != nil {
		t.Fatal(err)
	}
	if result.Magnet == "" || result.Hash != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("merged magnet = %q", result.Magnet)
	}
}
