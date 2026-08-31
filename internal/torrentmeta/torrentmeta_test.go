package torrentmeta

import (
	"crypto/sha1"
	"encoding/base32"
	"encoding/hex"
	"math"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func TestParseMagnetNormalizesV1HexAndBase32(t *testing.T) {
	limits := testLimits()
	hexHash := "0123456789abcdef0123456789abcdef01234567"
	bytesHash, err := hex.DecodeString(hexHash)
	if err != nil {
		t.Fatal(err)
	}
	base32Hash := strings.ToLower(base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(bytesHash))

	for _, raw := range []string{
		"magnet:?tr=udp%3A%2F%2Ft.example%2Fa&dn=Example+File&xt=urn%3Abtih%3A" + strings.ToUpper(hexHash) + "&xt=urn%3Abtih%3A" + hexHash,
		"magnet:?xt=URN%3ABTIH%3A" + base32Hash + "&dn=Example+File",
	} {
		result, err := ParseMagnet(raw, limits)
		if err != nil {
			t.Fatalf("ParseMagnet(%q): %v", raw, err)
		}
		if result.Hash != hexHash {
			t.Fatalf("Hash = %q, want %q", result.Hash, hexHash)
		}
		if result.Name != "Example File" {
			t.Fatalf("Name = %q", result.Name)
		}
		if result.MultiFile || result.TotalSize != 0 || result.Files == nil || len(result.Files) != 0 {
			t.Fatalf("unexpected magnet file result: %#v", result)
		}
		if !strings.HasPrefix(result.Magnet, "magnet:?xt=urn%3Abtih%3A"+hexHash+"&dn=Example+File") {
			t.Fatalf("Magnet = %q", result.Magnet)
		}
	}
}

func TestParseMagnetRejectsAmbiguousAndUnsafeInputWithoutSecrets(t *testing.T) {
	limits := testLimits()
	valid := "0123456789abcdef0123456789abcdef01234567"
	secret := "passkey=do-not-leak"
	for _, raw := range []string{
		"magnet:?xt=urn:btih:" + valid + "&xt=urn:btih:ffffffffffffffffffffffffffffffffffffffff",
		"magnet:?xt=urn:btmh:1220deadbeef",
		"magnet:?xt=urn:btih:" + valid + "&xt=urn:btih:not-a-v1-hash",
		"magnet:?xt=urn:btih:" + valid + "&dn=..",
		"magnet:?xt=urn:btih:" + valid + "&tr=https%3A%2F%2Fuser%3A" + url.QueryEscape(secret) + "%40tracker.example%2Fannounce",
		"magnet:?xt=urn:btih:" + valid + "&bad=%zz",
	} {
		_, err := ParseMagnet(raw, limits)
		if err == nil {
			t.Fatalf("ParseMagnet(%q) unexpectedly succeeded", raw)
		}
		if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), raw) {
			t.Fatalf("error leaked sensitive input: %q", err)
		}
	}
}

func TestParseTorrentHashesCapturedRawInfoBytes(t *testing.T) {
	info := singleInfo("raw-name", 3, "")
	data := torrent(info, "")
	result, err := ParseTorrent(data, testLimits())
	if err != nil {
		t.Fatal(err)
	}
	expected := sha1.Sum([]byte(info))
	if result.Hash != hex.EncodeToString(expected[:]) {
		t.Fatalf("Hash = %q, want SHA-1 of raw info bytes %x", result.Hash, expected)
	}
	if result.Name != "raw-name" || result.MultiFile || result.TotalSize != 3 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(result.Files) != 1 || result.Files[0] != (File{Index: 0, RelativePath: "raw-name", Size: 3}) {
		t.Fatalf("Files = %#v", result.Files)
	}
}
func TestParseTorrentCapturesPrivateFlag(t *testing.T) {
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{name: "private", raw: singleInfo("private", 1, "7:privatei1e"), want: true},
		{name: "public", raw: singleInfo("public", 1, "7:privatei0e"), want: false},
		{name: "default public", raw: singleInfo("default-public", 1, ""), want: false},
	} {
		result, err := ParseTorrent(torrent(test.raw, ""), testLimits())
		if err != nil {
			t.Fatalf("%s ParseTorrent: %v", test.name, err)
		}
		if result.Private == nil || *result.Private != test.want {
			t.Fatalf("%s private = %#v, want %t", test.name, result.Private, test.want)
		}
	}
	magnet, err := ParseMagnet("magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567", testLimits())
	if err != nil {
		t.Fatal(err)
	}
	if magnet.Private != nil {
		t.Fatalf("magnet private = %#v, want nil", magnet.Private)
	}
}

func TestParseTorrentAcceptsHybridAndRejectsV2Only(t *testing.T) {
	hybridInfo := singleInfo("hybrid", 1, "12:meta versioni2e")
	hybrid, err := ParseTorrent(torrent(hybridInfo, ""), testLimits())
	if err != nil {
		t.Fatalf("hybrid ParseTorrent: %v", err)
	}
	expected := sha1.Sum([]byte(hybridInfo))
	if hybrid.Hash != hex.EncodeToString(expected[:]) || hybrid.Files[0].RelativePath != "hybrid" {
		t.Fatalf("hybrid = %#v", hybrid)
	}

	v2Only := "d12:meta versioni2e4:name4:only12:piece lengthi16384ee"
	if _, err := ParseTorrent(torrent(v2Only, ""), testLimits()); err == nil {
		t.Fatal("v2-only torrent unexpectedly succeeded")
	}
}

func TestParseTorrentRejectsMalformedTrailingAndResourceLimits(t *testing.T) {
	limits := testLimits()
	valid := torrent(singleInfo("file", 0, ""), "")
	for _, data := range [][]byte{[]byte("not-bencode"), append(append([]byte(nil), valid...), 'x')} {
		if _, err := ParseTorrent(data, limits); err == nil {
			t.Fatalf("ParseTorrent(%q) unexpectedly succeeded", data)
		}
	}

	shortInput := limits
	shortInput.MaxInputBytes = len(valid) - 1
	if _, err := ParseTorrent(valid, shortInput); err == nil {
		t.Fatal("MaxInputBytes was not enforced")
	}
	shortInfo := limits
	shortInfo.MaxInfoBytes = len(singleInfo("file", 0, "")) - 1
	if _, err := ParseTorrent(valid, shortInfo); err == nil {
		t.Fatal("MaxInfoBytes was not enforced")
	}
	for _, mutate := range []func(*Limits){
		func(value *Limits) { value.MaxFiles = 0 },
		func(value *Limits) { value.MaxTotalSize = 0 },
	} {
		invalid := limits
		mutate(&invalid)
		if _, err := ParseTorrent(valid, invalid); err == nil {
			t.Fatal("invalid limits were accepted")
		}
	}
}

func TestParseTorrentRejectsNegativeOverflowAndFileLimits(t *testing.T) {
	limits := testLimits()
	for _, info := range []string{
		singleInfo("negative", -1, ""),
		"d6:lengthi0e4:name14:negative-piece12:piece lengthi-1e6:pieces20:01234567890123456789e",
		multiInfo([]torrentFile{{path: []string{"a"}, length: math.MaxInt64}, {path: []string{"b"}, length: 1}}),
	} {
		if _, err := ParseTorrent(torrent(info, ""), limits); err == nil {
			t.Fatalf("invalid size torrent unexpectedly succeeded: %q", info)
		}
	}

	tooMany := multiInfo([]torrentFile{{path: []string{"a"}, length: 0}, {path: []string{"b"}, length: 0}})
	fileLimit := limits
	fileLimit.MaxFiles = 1
	if _, err := ParseTorrent(torrent(tooMany, ""), fileLimit); err == nil {
		t.Fatal("MaxFiles was not enforced")
	}

	totalLimit := limits
	totalLimit.MaxTotalSize = 1
	if _, err := ParseTorrent(torrent(singleInfo("large", 2, ""), ""), totalLimit); err == nil {
		t.Fatal("MaxTotalSize was not enforced")
	}
}

func TestParseTorrentValidatesV1PathsAndProducesContiguousFiles(t *testing.T) {
	limits := testLimits()
	validInfo := multiInfo([]torrentFile{
		{path: []string{"directory", "first"}, length: 0},
		{path: []string{"directory", "second"}, length: 2},
	})
	result, err := ParseTorrent(torrent(validInfo, ""), limits)
	if err != nil {
		t.Fatal(err)
	}
	if !result.MultiFile || result.TotalSize != 2 {
		t.Fatalf("unexpected multi-file result: %#v", result)
	}
	wantFiles := []File{{Index: 0, RelativePath: "directory/first", Size: 0}, {Index: 1, RelativePath: "directory/second", Size: 2}}
	if len(result.Files) != len(wantFiles) {
		t.Fatalf("Files = %#v", result.Files)
	}
	for index := range wantFiles {
		if result.Files[index] != wantFiles[index] {
			t.Fatalf("Files[%d] = %#v, want %#v", index, result.Files[index], wantFiles[index])
		}
	}

	for _, files := range [][]torrentFile{
		{{path: []string{".."}, length: 1}},
		{{path: []string{"dir/file"}, length: 1}},
		{{path: []string{"bad\x00name"}, length: 1}},
		{{path: []string{"same"}, length: 1}, {path: []string{"same"}, length: 1}},
	} {
		if _, err := ParseTorrent(torrent(multiInfo(files), ""), limits); err == nil {
			t.Fatalf("unsafe files %#v unexpectedly succeeded", files)
		}
	}

	pathLimit := limits
	pathLimit.MaxPathBytes = len("directory/first") - 1
	if _, err := ParseTorrent(torrent(validInfo, ""), pathLimit); err == nil {
		t.Fatal("MaxPathBytes was not enforced")
	}
	componentLimit := limits
	componentLimit.MaxComponentBytes = len("directory") - 1
	if _, err := ParseTorrent(torrent(validInfo, ""), componentLimit); err == nil {
		t.Fatal("MaxComponentBytes was not enforced")
	}
}

func TestTrackersAreBoundedOrderedDeduplicatedAndRedacted(t *testing.T) {
	limits := testLimits()
	first := "udp://one.example/announce?passkey=secret"
	second := "https://two.example/announce"
	announceList := bList(bList(bString(first), bString(first)), bList(bString(second)))
	data := "d13:announce-list" + announceList + "4:info" + singleInfo("tracked", 0, "") + "e"
	result, err := ParseTorrent([]byte(data), limits)
	if err != nil {
		t.Fatal(err)
	}
	wantSuffix := "&tr=" + url.QueryEscape(first) + "&tr=" + url.QueryEscape(second)
	if !strings.HasSuffix(result.Magnet, wantSuffix) {
		t.Fatalf("Magnet = %q, want tracker order suffix %q", result.Magnet, wantSuffix)
	}

	countLimit := limits
	countLimit.MaxTrackerCount = 1
	if _, err := ParseTorrent([]byte(data), countLimit); err == nil {
		t.Fatal("raw duplicate trackers bypassed MaxTrackerCount")
	}
	trackerLimit := limits
	trackerLimit.MaxTrackerBytes = len(second) - 1
	if _, err := ParseTorrent([]byte(data), trackerLimit); err == nil {
		t.Fatal("MaxTrackerBytes was not enforced")
	}

	for _, raw := range []string{
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=" + url.QueryEscape("https://tracker.example/#"+"secret"),
		"magnet:?xt=urn:btih:0123456789abcdef0123456789abcdef01234567&tr=" + url.QueryEscape("ftp://tracker.example/"+"secret"),
	} {
		_, err := ParseMagnet(raw, limits)
		if err == nil {
			t.Fatalf("unsafe tracker magnet unexpectedly succeeded: %q", raw)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("tracker secret leaked in error: %q", err)
		}
	}
}

func testLimits() Limits {
	return Limits{
		MaxInputBytes:     1 << 20,
		MaxInfoBytes:      1 << 19,
		MaxFiles:          100,
		MaxNameBytes:      100,
		MaxPathBytes:      200,
		MaxComponentBytes: 100,
		MaxTrackerCount:   10,
		MaxTrackerBytes:   200,
		MaxTotalSize:      math.MaxInt64,
	}
}

type torrentFile struct {
	path   []string
	length int64
}

func torrent(info, rootFields string) []byte {
	return []byte("d" + rootFields + "4:info" + info + "e")
}

func singleInfo(name string, length int64, extra string) string {
	return "d6:lengthi" + strconv.FormatInt(length, 10) + "e" + bString("name") + bString(name) + extra + "12:piece lengthi16384e6:pieces20:01234567890123456789e"
}

func multiInfo(files []torrentFile) string {
	encoded := make([]string, 0, len(files))
	for _, file := range files {
		path := make([]string, 0, len(file.path))
		for _, component := range file.path {
			path = append(path, bString(component))
		}
		encoded = append(encoded, "d6:lengthi"+strconv.FormatInt(file.length, 10)+"e4:path"+bList(path...)+"e")
	}
	return "d5:files" + bList(encoded...) + bString("name") + bString("root") + "12:piece lengthi16384e6:pieces20:01234567890123456789e"
}

func bString(value string) string {
	return strconv.Itoa(len(value)) + ":" + value
}

func bList(values ...string) string {
	return "l" + strings.Join(values, "") + "e"
}
