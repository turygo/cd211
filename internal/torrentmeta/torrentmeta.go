// Package torrentmeta parses bounded, persistence-safe BitTorrent metadata.
package torrentmeta

import (
	"bytes"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"math"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/anacrolix/torrent/metainfo"
)

// Limits bounds untrusted magnet and torrent metadata before it is retained.
type Limits struct {
	MaxInputBytes     int
	MaxInfoBytes      int
	MaxFiles          int
	MaxNameBytes      int
	MaxPathBytes      int
	MaxComponentBytes int
	MaxTrackerCount   int
	MaxTrackerBytes   int
	MaxTotalSize      int64
}

// File is one safe, relative torrent file path.
type File struct {
	Index        int64
	RelativePath string
	Size         int64
}

// Result is normalized metadata suitable for persistence.
type Result struct {
	Hash      string
	Name      string
	Magnet    string
	TotalSize int64
	MultiFile bool
	// Private is known for parsed torrent metainfo and nil for magnets.
	Private *bool
	Files   []File
}

var (
	errInvalidLimits   = errors.New("invalid torrent metadata limits")
	errInvalidMagnet   = errors.New("invalid magnet")
	errUnsupported     = errors.New("unsupported torrent metadata")
	errInvalidTorrent  = errors.New("invalid torrent metadata")
	errInvalidName     = errors.New("invalid torrent name")
	errInvalidPath     = errors.New("invalid torrent path")
	errInvalidTracker  = errors.New("invalid tracker")
	errResourceLimit   = errors.New("torrent metadata limit exceeded")
	errInvalidContents = errors.New("invalid torrent contents")
)

// ParseMagnet parses a v1 magnet URI into canonical, bounded metadata.
func ParseMagnet(raw string, limits Limits) (Result, error) {
	if err := validateLimits(limits); err != nil {
		return Result{}, err
	}
	if len(raw) > limits.MaxInputBytes {
		return Result{}, errResourceLimit
	}

	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "magnet") {
		return Result{}, errInvalidMagnet
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return Result{}, errInvalidMagnet
	}
	for key, values := range query {
		if !utf8.ValidString(key) {
			return Result{}, errInvalidMagnet
		}
		for _, value := range values {
			if !utf8.ValidString(value) {
				return Result{}, errInvalidMagnet
			}
		}
	}

	hash, hasHash, err := magnetV1Hash(query["xt"])
	if err != nil {
		return Result{}, err
	}
	if !hasHash {
		return Result{}, errUnsupported
	}
	name, err := magnetName(query["dn"], hash, limits)
	if err != nil {
		return Result{}, err
	}
	trackers, err := normalizeTrackers(query["tr"], limits)
	if err != nil {
		return Result{}, err
	}

	return Result{
		Hash:      hash,
		Name:      name,
		Magnet:    canonicalMagnet(hash, name, trackers),
		TotalSize: 0,
		MultiFile: false,
		Files:     make([]File, 0),
	}, nil
}

// ParseTorrent parses a v1 or hybrid .torrent without constructing a torrent client.
func ParseTorrent(data []byte, limits Limits) (Result, error) {
	if err := validateLimits(limits); err != nil {
		return Result{}, err
	}
	if len(data) > limits.MaxInputBytes {
		return Result{}, errResourceLimit
	}

	mi, err := metainfo.Load(bytes.NewReader(data))
	if err != nil || len(mi.InfoBytes) == 0 {
		return Result{}, errInvalidTorrent
	}
	if len(mi.InfoBytes) > limits.MaxInfoBytes {
		return Result{}, errResourceLimit
	}
	info, err := mi.UnmarshalInfo()
	if err != nil {
		return Result{}, errInvalidTorrent
	}
	if !info.HasV1() {
		return Result{}, errUnsupported
	}
	if info.PieceLength < 0 {
		return Result{}, errInvalidContents
	}
	private := info.Private
	if private == nil {
		public := false
		private = &public
	}

	name := info.BestName()
	if err := validateName(name, limits); err != nil {
		return Result{}, err
	}
	trackers, err := torrentTrackers(mi, limits)
	if err != nil {
		return Result{}, err
	}

	if info.Files != nil && len(info.Files) == 0 {
		return Result{}, errInvalidContents
	}
	if len(info.Files) == 0 {
		file, total, err := singleFile(info, name, limits)
		if err != nil {
			return Result{}, err
		}
		return Result{
			Hash:      mi.HashInfoBytes().HexString(),
			Name:      name,
			Magnet:    canonicalMagnet(mi.HashInfoBytes().HexString(), name, trackers),
			TotalSize: total,
			MultiFile: false,
			Private:   private,
			Files:     []File{file},
		}, nil
	}

	files, total, err := multiFiles(&info, limits)
	if err != nil {
		return Result{}, err
	}
	hash := mi.HashInfoBytes().HexString()
	return Result{
		Hash:      hash,
		Name:      name,
		Magnet:    canonicalMagnet(hash, name, trackers),
		TotalSize: total,
		MultiFile: true,
		Private:   private,
		Files:     files,
	}, nil
}

func validateLimits(limits Limits) error {
	if limits.MaxInputBytes <= 0 || limits.MaxInfoBytes <= 0 || limits.MaxFiles <= 0 ||
		limits.MaxNameBytes <= 0 || limits.MaxPathBytes <= 0 || limits.MaxComponentBytes <= 0 ||
		limits.MaxTrackerCount <= 0 || limits.MaxTrackerBytes <= 0 || limits.MaxTotalSize <= 0 {
		return errInvalidLimits
	}
	return nil
}

func magnetV1Hash(values []string) (string, bool, error) {
	var hash string
	for _, value := range values {
		if len(value) < len("urn:btih:") || !strings.EqualFold(value[:len("urn:btih:")], "urn:btih:") {
			continue
		}
		decoded, err := decodeV1Hash(value[len("urn:btih:"):])
		if err != nil {
			return "", false, errInvalidMagnet
		}
		if hash != "" && hash != decoded {
			return "", false, errInvalidMagnet
		}
		hash = decoded
	}
	return hash, hash != "", nil
}

func decodeV1Hash(value string) (string, error) {
	if len(value) == 40 {
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded), nil
		}
	}
	if len(value) == 32 {
		for _, r := range value {
			if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '2' && r <= '7')) {
				return "", errInvalidMagnet
			}
		}
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded), nil
		}
	}
	return "", errInvalidMagnet
}

func magnetName(values []string, fallback string, limits Limits) (string, error) {
	name := ""
	for _, value := range values {
		if value == "" {
			continue
		}
		if name != "" && name != value {
			return "", errInvalidMagnet
		}
		name = value
	}
	if name == "" {
		name = fallback
	}
	if err := validateName(name, limits); err != nil {
		return "", err
	}
	return name, nil
}

func torrentTrackers(mi *metainfo.MetaInfo, limits Limits) ([]string, error) {
	announceList := mi.UpvertedAnnounceList()
	raw := make([]string, 0)
	for _, tier := range announceList {
		raw = append(raw, tier...)
	}
	return normalizeTrackers(raw, limits)
}

func normalizeTrackers(raw []string, limits Limits) ([]string, error) {
	if len(raw) > limits.MaxTrackerCount {
		return nil, errResourceLimit
	}
	trackers := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for _, tracker := range raw {
		if !utf8.ValidString(tracker) || len(tracker) == 0 {
			return nil, errInvalidTracker
		}
		if len(tracker) > limits.MaxTrackerBytes {
			return nil, errResourceLimit
		}
		u, err := url.Parse(tracker)
		if err != nil || !u.IsAbs() || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
			return nil, errInvalidTracker
		}
		switch strings.ToLower(u.Scheme) {
		case "http", "https", "udp", "ws", "wss":
		default:
			return nil, errInvalidTracker
		}
		if _, ok := seen[tracker]; ok {
			continue
		}
		seen[tracker] = struct{}{}
		trackers = append(trackers, tracker)
	}
	return trackers, nil
}

// NormalizeTrackers validates, normalizes, and de-duplicates tracker URLs.
func NormalizeTrackers(raw []string, limits Limits) ([]string, error) {
	return normalizeTrackers(raw, limits)
}

// AddTrackers appends validated trackers to a magnet while preserving all
// existing query parameters and tracker order.
func AddTrackers(raw string, additional []string, limits Limits) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || !strings.EqualFold(u.Scheme, "magnet") {
		return "", errInvalidMagnet
	}
	query := u.Query()
	source := append([]string(nil), query["tr"]...)
	combined := append(source, additional...)
	trackers, err := normalizeTrackers(combined, limits)
	if err != nil {
		return "", err
	}
	query["tr"] = trackers
	u.RawQuery = query.Encode()
	return u.String(), nil
}

func validateName(name string, limits Limits) error {
	if len(name) > limits.MaxNameBytes || len(name) > limits.MaxComponentBytes || len(name) > limits.MaxPathBytes {
		return errResourceLimit
	}
	if !safeComponent(name) {
		return errInvalidName
	}
	return nil
}

func validateRelativePath(parts []string, limits Limits) (string, error) {
	if len(parts) == 0 {
		return "", errInvalidPath
	}
	pathBytes := 0
	for index, part := range parts {
		if len(part) > limits.MaxComponentBytes {
			return "", errResourceLimit
		}
		if !safeComponent(part) {
			return "", errInvalidPath
		}
		pathBytes += len(part)
		if index != 0 {
			pathBytes++
		}
	}
	if pathBytes > limits.MaxPathBytes {
		return "", errResourceLimit
	}
	return strings.Join(parts, "/"), nil
}

func safeComponent(value string) bool {
	if value == "" || value == "." || value == ".." || !utf8.ValidString(value) ||
		strings.HasPrefix(value, "/") || strings.HasPrefix(value, "\\") ||
		strings.ContainsAny(value, "/\\\x00") {
		return false
	}
	return !strings.ContainsFunc(value, unicode.IsControl)
}

func singleFile(info metainfo.Info, name string, limits Limits) (File, int64, error) {
	if info.Length < 0 {
		return File{}, 0, errInvalidContents
	}
	if info.Length > limits.MaxTotalSize {
		return File{}, 0, errResourceLimit
	}
	return File{Index: 0, RelativePath: name, Size: info.Length}, info.Length, nil
}

func multiFiles(info *metainfo.Info, limits Limits) ([]File, int64, error) {
	if len(info.Files) > limits.MaxFiles {
		return nil, 0, errResourceLimit
	}
	files := make([]File, 0, len(info.Files))
	seenPaths := make(map[string]struct{}, len(info.Files))
	var total int64
	for fileInfo := range info.UpvertedV1Files() {
		if len(files) >= limits.MaxFiles {
			return nil, 0, errResourceLimit
		}
		if fileInfo.Length < 0 {
			return nil, 0, errInvalidContents
		}
		path, err := validateRelativePath(fileInfo.BestPath(), limits)
		if err != nil {
			return nil, 0, err
		}
		if _, ok := seenPaths[path]; ok {
			return nil, 0, errInvalidPath
		}
		if total > math.MaxInt64-fileInfo.Length {
			return nil, 0, errResourceLimit
		}
		total += fileInfo.Length
		if total > limits.MaxTotalSize {
			return nil, 0, errResourceLimit
		}
		seenPaths[path] = struct{}{}
		files = append(files, File{Index: int64(len(files)), RelativePath: path, Size: fileInfo.Length})
	}
	if len(files) != len(info.Files) || len(files) == 0 {
		return nil, 0, errInvalidContents
	}
	return files, total, nil
}

func canonicalMagnet(hash, name string, trackers []string) string {
	values := make([]string, 0, 2+len(trackers))
	values = append(values, "xt="+url.QueryEscape("urn:btih:"+hash))
	values = append(values, "dn="+url.QueryEscape(name))
	for _, tracker := range trackers {
		values = append(values, "tr="+url.QueryEscape(tracker))
	}
	return "magnet:?" + strings.Join(values, "&")
}
