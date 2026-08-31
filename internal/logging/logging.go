// Package logging provides the process logger, safe HTTP completion records,
// and the bounded reader used by the operator log page.
package logging

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/turygo/cd211/internal/authn"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	maxString     = 512
	maxPathString = 1024
	maxPreview    = 4096
	maxDetail     = 16 << 10
	maxLine       = 256 << 10
)

type RotatingWriter struct {
	mu   sync.Mutex
	dir  string
	now  func() time.Time
	file *os.File
	date string
}

func NewRotatingWriter(dir string) (*RotatingWriter, error) {
	return NewRotatingWriterWithClock(dir, time.Now)
}
func NewRotatingWriterWithClock(dir string, now func() time.Time) (*RotatingWriter, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0750); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0750); err != nil {
		return nil, err
	}
	w := &RotatingWriter{dir: dir, now: now}
	if _, err := w.openLocked(now().UTC()); err != nil {
		return nil, err
	}
	w.cleanLocked(now().UTC())
	return w, nil
}
func (w *RotatingWriter) filename(date string) string {
	return filepath.Join(w.dir, "cd211-"+date+".jsonl")
}
func (w *RotatingWriter) openLocked(now time.Time) (bool, error) {
	date := now.UTC().Format("2006-01-02")
	if w.file != nil && date == w.date {
		return false, nil
	}
	f, err := os.OpenFile(w.filename(date), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0640)
	if err != nil {
		return false, err
	}
	_ = f.Chmod(0640)
	old := w.file
	w.file, w.date = f, date
	if old != nil {
		_ = old.Close()
	}
	return true, nil
}
func (w *RotatingWriter) cleanLocked(now time.Time) {
	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return
	}
	cutoff := now.UTC().AddDate(0, 0, -29)
	for _, entry := range entries {
		name := entry.Name()
		if len(name) != len("cd211-YYYY-MM-DD.jsonl") || !strings.HasPrefix(name, "cd211-") || !strings.HasSuffix(name, ".jsonl") {
			continue
		}
		date, err := time.Parse("2006-01-02", name[6:16])
		if err != nil {
			continue
		}
		if date.Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, name))
		}
	}
}
func (w *RotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := w.now().UTC()
	rotated, err := w.openLocked(now)
	if err != nil {
		if w.file != nil {
			n, writeErr := w.file.Write(p)
			if writeErr == nil {
				return n, err
			}
			return n, writeErr
		}
		return len(p), err
	}
	if rotated {
		w.cleanLocked(now)
	}
	return w.file.Write(p)
}
func (w *RotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}
func (w *RotatingWriter) Dir() string { return w.dir }

type dualWriter struct {
	stderr io.Writer
	files  *RotatingWriter
}

func (w dualWriter) Write(p []byte) (int, error) {
	// stderr is the operational sink and must remain available if rotation fails.
	n, stderrErr := w.stderr.Write(p)
	_, fileErr := w.files.Write(p)
	if stderrErr != nil {
		return n, stderrErr
	}
	if fileErr != nil {
		_, _ = fmt.Fprintf(w.stderr, "cd211: log file write failed: %s\n", cleanString(fileErr.Error(), maxString))
		return n, nil
	}
	return n, nil
}

type Process struct {
	Logger *slog.Logger
	Writer *RotatingWriter
}

func NewProcess(logDir string) (*Process, error) {
	writer, err := NewRotatingWriter(logDir)
	if err != nil {
		return nil, err
	}
	handler := &safeHandler{Handler: slog.NewJSONHandler(dualWriter{stderr: os.Stderr, files: writer}, &slog.HandlerOptions{Level: slog.LevelDebug})}
	return &Process{Logger: slog.New(handler), Writer: writer}, nil
}
func (p *Process) Close() error {
	if p == nil || p.Writer == nil {
		return nil
	}
	return p.Writer.Close()
}

var safeNames = map[string]bool{"method": true, "path": true, "status": true, "duration_ms": true, "response_bytes": true, "client_ip": true, "user_agent": true, "content_type": true, "content_length": true, "host": true, "auth_mode": true, "auth_surface": true, "auth_attempt": true, "auth_principal": true, "reason": true, "internal_reason": true, "error": true, "msg": true, "source_kind": true, "infohash": true, "name": true, "filename": true, "size": true, "category": true, "raw_category": true, "savepath": true, "resolved_savepath": true, "stopped": true, "paused": true, "autoTMM": true, "url_fingerprint": true, "url_host": true, "preview": true, "request": true, "response": true, "details": true, "truncated": true}

var sensitiveNames = map[string]bool{
	"authorization": true,
	"cookie":        true,
	"password":      true,
	"token":         true,
	"secret":        true,
	"csrf":          true,
	"sid":           true,
	"api_key":       true,
}

func cleanString(s string, bound int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "�")
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, s)
	b := []byte(s)
	if len(b) > bound {
		b = b[:bound]
		for !utf8.Valid(b) {
			b = b[:len(b)-1]
		}
	}
	return string(b)
}
func sanitizeAny(key string, value any) any {
	if sensitive(key) {
		return "[OMITTED]"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			out[k] = sanitizeAny(k, item)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = sanitizeAny(key, item)
		}
		return out
	default:
		return safeAttr(key, value)
	}
}
func safeAttr(key string, value any) any {
	if sensitive(key) {
		return "[OMITTED]"
	}
	if !safeNames[key] {
		return "[OMITTED]"
	}
	switch v := value.(type) {
	case string:
		return cleanString(v, maxString)
	case error:
		return cleanString(v.Error(), maxString)
	case []byte:
		return "[OMITTED]"
	default:
		return value
	}
}
func sensitive(key string) bool {
	key = strings.ToLower(strings.ReplaceAll(key, "-", "_"))
	for n := range sensitiveNames {
		if strings.Contains(key, n) {
			return true
		}
	}
	return false
}

type safeHandler struct{ slog.Handler }

func (h *safeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	out := make([]slog.Attr, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, slog.Any(a.Key, sanitizeAny(a.Key, a.Value.Any())))
	}
	return &safeHandler{Handler: h.Handler.WithAttrs(out)}
}
func (h *safeHandler) WithGroup(name string) slog.Handler {
	return &safeHandler{Handler: h.Handler.WithGroup(name)}
}
func (h *safeHandler) Handle(ctx context.Context, r slog.Record) error {
	r = r.Clone()
	attrs := make([]slog.Attr, 0, r.NumAttrs())
	r.Attrs(func(a slog.Attr) bool {
		attrs = append(attrs, slog.Any(a.Key, sanitizeAny(a.Key, a.Value.Any())))
		return true
	})
	nr := slog.NewRecord(r.Time, r.Level, cleanString(r.Message, maxString), r.PC)
	nr.AddAttrs(attrs...)
	return h.Handler.Handle(ctx, nr)
}

// Endpoint details are installed once by HTTP middleware and enriched by handlers.
type requestContextKey struct{}
type RequestContext struct {
	mu            sync.Mutex
	Details       map[string]any
	Reason        string
	AuthSurface   string
	AuthAttempt   string
	AuthPrincipal string
	AuthMode      string
}

func WithRequestContext(r *http.Request) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), requestContextKey{}, &RequestContext{
		Details:       map[string]any{},
		AuthSurface:   "none",
		AuthAttempt:   "none",
		AuthPrincipal: "anonymous",
		AuthMode:      "none",
	}))
}

func RequestLogContext(r *http.Request) *RequestContext {
	if r == nil {
		return nil
	}
	c, _ := r.Context().Value(requestContextKey{}).(*RequestContext)
	return c
}
func SetAuthAttempt(r *http.Request, surface, attempt string) {
	c := RequestLogContext(r)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.AuthSurface = surface
	c.AuthAttempt = attempt
	c.AuthPrincipal = "anonymous"
	c.AuthMode = "none"
	c.mu.Unlock()
}

func SetAuthSuccess(r *http.Request, principal authn.Principal) {
	c := RequestLogContext(r)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.AuthPrincipal = string(principal.Kind)
	c.AuthMode = string(principal.Method)
	c.mu.Unlock()
}
func Enrich(r *http.Request, fields map[string]any) {
	c := RequestLogContext(r)
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, value := range fields {
		if !addDetail(c.Details, key, value) {
			break
		}
	}
}
func addDetail(details map[string]any, key string, value any) bool {
	key = cleanString(key, maxString)
	value = sanitizeAny(key, value)
	candidate := make(map[string]any, len(details)+1)
	for existingKey, existingValue := range details {
		candidate[existingKey] = existingValue
	}
	candidate[key] = value
	if len(mustJSON(candidate)) > maxDetail {
		markTruncated(details)
		return false
	}
	details[key] = value
	return true
}
func markTruncated(details map[string]any) {
	details["truncated"] = true
	for len(mustJSON(details)) > maxDetail {
		for key := range details {
			if key != "truncated" {
				delete(details, key)
				break
			}
		}
	}
}
func SetReason(r *http.Request, reason string) {
	c := RequestLogContext(r)
	if c == nil {
		return
	}
	c.mu.Lock()
	c.Reason = cleanString(reason, maxString)
	c.mu.Unlock()
}
func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

func SanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	return cleanString(filepath.Base(name), maxString)
}
func SanitizeURL(raw string) map[string]any {
	parsed, err := url.Parse(raw)
	out := map[string]any{"source_kind": "url"}
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return out
	}
	out["url_host"] = cleanString(parsed.Scheme+"://"+parsed.Host+"/…", maxString)
	out["url_fingerprint"] = urlFingerprint(raw)
	return out
}
func urlFingerprint(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:12]
}
func SanitizeMagnet(raw string) map[string]any {
	out := map[string]any{"source_kind": "magnet"}
	parsed, err := url.Parse(raw)
	if err != nil {
		return out
	}
	query := parsed.Query()
	const prefix = "urn:btih:"
	xt := query.Get("xt")
	if len(xt) >= len(prefix) && strings.EqualFold(xt[:len(prefix)], prefix) {
		if hash, ok := canonicalInfohash(xt[len(prefix):]); ok {
			out["infohash"] = hash
		}
	}
	if name := query.Get("dn"); name != "" {
		out["name"] = cleanString(name, maxString)
	}
	return out
}
func canonicalInfohash(value string) (string, bool) {
	if len(value) == 40 {
		decoded, err := hex.DecodeString(value)
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded), true
		}
	}
	if len(value) == 32 {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(value))
		if err == nil && len(decoded) == 20 {
			return hex.EncodeToString(decoded), true
		}
	}
	return "", false
}

// ResponseWriter records completion metadata and a deliberately small safe preview.
type responseWriter struct {
	http.ResponseWriter
	status, bytes int
	wrote         bool
	preview       []byte
	capture       bool
}

func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *responseWriter) WriteHeader(status int) {
	if w.wrote {
		return
	}
	w.wrote = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
func (w *responseWriter) Write(p []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	n, err := w.ResponseWriter.Write(p)
	w.bytes += n
	if w.capture && len(w.preview) < maxPreview {
		limit := maxPreview - len(w.preview)
		if limit > n {
			limit = n
		}
		w.preview = append(w.preview, p[:limit]...)
	}
	return n, err
}
func (w *responseWriter) Flush() {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w *responseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("hijacking unsupported")
	}
	return h.Hijack()
}
func (w *responseWriter) Push(target string, opts *http.PushOptions) error {
	if p, ok := w.ResponseWriter.(http.Pusher); ok {
		return p.Push(target, opts)
	}
	return http.ErrNotSupported
}
func (w *responseWriter) ReadFrom(r io.Reader) (int64, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK)
	}
	return io.Copy(struct{ io.Writer }{w}, r)
}
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return cleanString(r.RemoteAddr, maxString)
}

var safeQueryNames = map[string]bool{"category": true, "savepath": true, "stopped": true, "paused": true, "autoTMM": true, "rename": true, "tags": true, "contentLayout": true, "hash": true, "hashes": true, "filter": true, "sort": true, "reverse": true, "limit": true, "offset": true, "deleteFiles": true, "value": true, "priority": true, "indexes": true, "after": true, "before": true, "timeout": true, "wait": true, "types": true, "status": true, "event": true, "lang": true, "back": true, "from": true, "to": true, "level": true, "method": true, "path": true, "reason": true, "scope": true, "q": true}

func requestDetails(r *http.Request) map[string]any {
	out := map[string]any{}
	for key, values := range r.URL.Query() {
		var value any = "[OMITTED]"
		if sensitive(key) {
			value = "[OMITTED]"
		} else if len(values) > 0 {
			switch {
			case key == "url" || key == "urls" || key == "magnet":
				if strings.HasPrefix(strings.ToLower(values[0]), "magnet:") {
					value = SanitizeMagnet(values[0])
				} else {
					value = SanitizeURL(values[0])
				}
			case safeQueryNames[key]:
				value = cleanString(values[0], maxString)
			}
		}
		if !addDetail(out, key, value) {
			break
		}
	}
	return out
}
func Middleware(logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if logger == nil {
			next.ServeHTTP(w, r)
			return
		}
		r = WithRequestContext(r)
		rw := &responseWriter{ResponseWriter: w, capture: isPreviewPath(r.URL.Path)}
		started := time.Now()
		emit := func(panicked bool) {
			if !rw.wrote {
				rw.status = http.StatusOK
			}
			if panicked {
				rw.status = http.StatusInternalServerError
			}
			details := requestDetails(r)
			reason := ""
			authSurface, authAttempt, authPrincipal, authMode := "none", "none", "anonymous", "none"
			if c := RequestLogContext(r); c != nil {
				c.mu.Lock()
				for key, value := range c.Details {
					if !addDetail(details, key, value) {
						break
					}
				}
				reason = c.Reason
				authSurface, authAttempt, authPrincipal, authMode = c.AuthSurface, c.AuthAttempt, c.AuthPrincipal, c.AuthMode
				c.mu.Unlock()
			}
			level := slog.LevelDebug
			if panicked || rw.status >= 500 {
				level = slog.LevelError
			} else if rw.status >= 400 || r.Context().Err() != nil {
				level = slog.LevelWarn
			} else if r.Method != http.MethodGet && r.Method != http.MethodHead {
				level = slog.LevelInfo
			}
			attrs := []any{"method", r.Method, "path", cleanString(r.URL.EscapedPath(), maxPathString), "status", rw.status, "duration_ms", time.Since(started).Seconds() * 1000, "response_bytes", rw.bytes, "client_ip", clientIP(r), "user_agent", cleanString(r.UserAgent(), maxPathString), "content_type", cleanString(rw.ResponseWriter.Header().Get("Content-Type"), maxString), "content_length", r.Header.Get("Content-Length"), "host", cleanString(r.Host, maxString), "auth_surface", authSurface, "auth_attempt", authAttempt, "auth_principal", authPrincipal, "auth_mode", authMode, "request", details}
			if reason != "" {
				attrs = append(attrs, "internal_reason", reason)
			}
			if panicked {
				attrs = append(attrs, "internal_reason", "internal_error")
			}
			if len(rw.preview) > 0 && safeResponseType(rw.ResponseWriter.Header().Get("Content-Type")) {
				attrs = append(attrs, "response", map[string]any{"content_type": rw.ResponseWriter.Header().Get("Content-Type"), "preview": cleanString(string(rw.preview), maxPreview)})
			}
			logger.Log(r.Context(), level, "http request", attrs...)
		}
		defer func() {
			if recovered := recover(); recovered != nil {
				emit(true)
				panic(recovered)
			}
			emit(false)
		}()
		next.ServeHTTP(rw, r)
	})
}
func isPreviewPath(path string) bool {
	switch path {
	case "/api/v2/torrents/add", "/healthz", "/readyz", "/api/v2/app/version", "/api/v2/app/webapiVersion":
		return true
	}
	return false
}
func safeResponseType(ct string) bool {
	mt, _, _ := strings.Cut(ct, ";")
	return mt == "application/json" || mt == "text/plain"
}

// Reader is a bounded, owned-file JSONL query source.
type Reader struct{ Dir string }
type Query struct {
	From, To time.Time
	Levels   map[string]bool
	Search   string
	Max      int
}
type Record struct {
	Time           time.Time
	Level, Message string
	Fields         map[string]any
}

func (r Reader) Query(q Query) ([]Record, error) {
	if q.Max <= 0 || q.Max > 200 {
		q.Max = 200
	}
	entries, err := os.ReadDir(r.Dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && ownedName(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))
	out := make([]Record, 0, q.Max)
	for _, name := range names {
		date, _ := time.Parse("2006-01-02", name[6:16])
		if !q.From.IsZero() && date.Before(q.From.UTC().Truncate(24*time.Hour)) {
			continue
		}
		if !q.To.IsZero() && date.After(q.To.UTC().Truncate(24*time.Hour)) {
			continue
		}
		f, e := os.Open(filepath.Join(r.Dir, name))
		if e != nil {
			continue
		}
		reader := bufio.NewReaderSize(f, 64<<10)
		rows := make([]Record, 0, q.Max)
		for {
			line, complete := readPhysicalLine(reader)
			if !complete {
				break
			}
			line = bytes.TrimSpace(line)
			if len(line) == 0 {
				continue
			}
			var raw map[string]any
			if json.Unmarshal(line, &raw) != nil {
				continue
			}
			rec := recordFrom(raw)
			if !match(rec, q) {
				continue
			}
			if len(rows) < q.Max {
				rows = append(rows, rec)
			} else {
				copy(rows, rows[1:])
				rows[len(rows)-1] = rec
			}
		}
		_ = f.Close()
		for i := len(rows) - 1; i >= 0 && len(out) < q.Max; i-- {
			out = append(out, rows[i])
		}
		if len(out) >= q.Max {
			break
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Time.After(out[j].Time) })
	return out, nil
}
func readPhysicalLine(reader *bufio.Reader) ([]byte, bool) {
	var line []byte
	oversized := false
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if !oversized && len(fragment) <= maxLine-len(line) {
				line = append(line, fragment...)
			} else {
				oversized = true
			}
		}
		if err == bufio.ErrBufferFull {
			continue
		}
		if err != nil {
			return nil, false
		}
		if oversized {
			return nil, true
		}
		return line, true
	}
}
func ownedName(name string) bool {
	if len(name) != len("cd211-YYYY-MM-DD.jsonl") || !strings.HasPrefix(name, "cd211-") || !strings.HasSuffix(name, ".jsonl") {
		return false
	}
	_, err := time.Parse("2006-01-02", name[6:16])
	return err == nil
}
func recordFrom(raw map[string]any) Record {
	rec := Record{Fields: map[string]any{}}
	if s, _ := raw["time"].(string); s != "" {
		rec.Time, _ = time.Parse(time.RFC3339Nano, s)
	}
	rec.Level = strings.ToLower(cleanString(fmt.Sprint(raw["level"]), maxString))
	rec.Message = cleanString(fmt.Sprint(raw["msg"]), maxString)
	for k, v := range raw {
		if k != "time" && k != "level" && k != "msg" {
			rec.Fields[k] = sanitizeAny(k, v)
		}
	}
	return rec
}
func match(rec Record, q Query) bool {
	level := strings.ToLower(rec.Level)
	if len(q.Levels) > 0 {
		found := false
		for wanted := range q.Levels {
			if strings.ToLower(wanted) == level {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if q.Search != "" {
		encoded, err := json.Marshal(rec)
		if err != nil {
			return false
		}
		if !strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(q.Search)) {
			return false
		}
	}
	return true
}
