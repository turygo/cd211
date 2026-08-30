package web

import (
	"html"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/turygo/cd211/internal/logging"
)

func TestLogsRequiresAuthenticationAndUsesSafeFilters(t *testing.T) {
	fixture := newWebFixture(t)
	if response := fixture.request(http.MethodGet, "/logs", nil, false); response.Code != http.StatusSeeOther {
		t.Fatalf("unauthenticated logs = %d", response.Code)
	}
	response := fixture.request(http.MethodGet, "/logs?from=2026-08-06&to=2026-08-06&level=warn&level=warn&level_set=1", nil, true)
	if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("logs = %d cache=%q", response.Code, response.Header().Get("Cache-Control"))
	}
	body := response.Body.String()
	if !strings.Contains(body, "duplicate") {
		t.Fatalf("missing expected log record: %q", body)
	}
	start := strings.Index(body, `<summary class="log-summary">`)
	if start < 0 {
		t.Fatalf("missing log summary: %q", body)
	}
	summaryEnd := strings.Index(body[start:], "</summary>")
	if summaryEnd < 0 {
		t.Fatalf("unterminated log summary: %q", body[start:])
	}
	end := start + summaryEnd
	summary := body[start:end]
	detailsStartOffset := strings.Index(body[start:], `<div class="log-details">`)
	if detailsStartOffset < 0 {
		t.Fatalf("missing expanded log details: %q", body[start:])
	}
	detailsStart := start + detailsStartOffset
	detailsEndOffset := strings.Index(body[detailsStart:], "</div>")
	if detailsEndOffset < 0 {
		t.Fatalf("unterminated expanded log details: %q", body[detailsStart:])
	}
	details := body[detailsStart : detailsStart+detailsEndOffset]
	for _, want := range []string{"POST", "http request"} {
		if !strings.Contains(details, want) {
			t.Errorf("expanded details missing %q: %q", want, details)
		}
	}
	for _, absent := range []string{"http request", "POST"} {
		if strings.Contains(summary, absent) {
			t.Fatalf("collapsed summary contains %q: %q", absent, summary)
		}
	}
	for _, want := range []string{`datetime="2026-08-06T11:59:00Z"`, `data-local-time`, `/api/v2/torrents/add`, `log-level-warn`, `log-status-4xx`} {
		if !strings.Contains(summary, want) {
			t.Errorf("summary missing %q: %q", want, summary)
		}
	}
	for _, secret := range []string{"viewer-auth-secret", "viewer-cookie-secret", "viewer-token-secret", "viewer-password-secret"} {
		if strings.Contains(body, secret) {
			t.Fatalf("secret %q rendered in logs", secret)
		}
	}
	if invalid := fixture.request(http.MethodGet, "/logs?from=2026-08-01&to=2026-08-10", nil, true); invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid date range = %d", invalid.Code)
	}
}

func TestParseLogQueryLevels(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name, target, want string
		ok                 bool
	}{
		{name: "default", target: "/logs", want: "warn,error", ok: true},
		{name: "repeated deduplicated stable", target: "/logs?level=error&level=debug&level=error&level=info&level_set=1", want: "debug,info,error", ok: true},
		{name: "empty", target: "/logs?level=&level_set=1", ok: false},
		{name: "comma separated", target: "/logs?level=warn,error&level_set=1", ok: false},
		{name: "missing submitted levels", target: "/logs?level_set=1", ok: false},
		{name: "invalid level set", target: "/logs?level=warn&level_set=0", ok: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			query, ok := parseLogQuery(httptest.NewRequest(http.MethodGet, test.target, nil), now)
			if ok != test.ok {
				t.Fatalf("parseLogQuery ok = %v, want %v", ok, test.ok)
			}
			if ok {
				if got := strings.Join(query.levels, ","); got != test.want {
					t.Errorf("parseLogQuery levels = %q, want %q", got, test.want)
				}
			}
		})
	}
}

func TestLogsSearchMatchesSafeRecordFields(t *testing.T) {
	fixture := newWebFixture(t)
	for _, term := range []string{"POST", "/api/v2/torrents/add", "409", "duplicate"} {
		response := fixture.request(http.MethodGet, "/logs?q="+term, nil, true)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "duplicate") {
			t.Errorf("search %q = %d, want matching record", term, response.Code)
		}
	}
}

func TestLogsRenderLocalizedChrome(t *testing.T) {
	fixture := newWebFixture(t)
	summaryPattern := regexp.MustCompile(`(?s)<summary\b[^>]*\baria-label="([^"]*)"[^>]*>(.*?)</summary>`)
	for _, item := range []struct {
		name       string
		lang       string
		title      string
		nav        string
		lede       string
		levelLabel string
		summary    string
	}{
		{name: "English", lang: "en", title: "Logs", nav: "Logs", lede: "Application and HTTP request history.", levelLabel: "Level", summary: "Warning, Error"},
		{name: "Chinese", lang: "zh", title: "日志", nav: "日志", lede: "应用与 HTTP 请求历史", levelLabel: "级别", summary: "警告、错误"},
	} {
		response := fixture.requestLang(http.MethodGet, "/logs", true, item.lang)
		if response.Code != http.StatusOK {
			t.Fatalf("logs = %d", response.Code)
		}
		body := response.Body.String()
		for _, want := range []string{
			`<title>` + item.title + ` · CD211</title>`,
			`<a class="nav-item" href="/logs" aria-current="page">` + item.nav + `</a>`,
			`<main class="content" id="main-content">`,
			`<h1>` + item.title + `</h1>`,
			`<p class="page-lede">` + item.lede + `</p>`,
			`<form class="filter-bar log-filters"`,
			`class="filter-field"`,
			`<input type="hidden" name="level_set" value="1">`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("logs missing %q in %s response", want, item.name)
			}
		}
		summaryFound := false
		for _, match := range summaryPattern.FindAllStringSubmatch(body, -1) {
			ariaLabel := html.UnescapeString(match[1])
			summaryText := strings.TrimSpace(html.UnescapeString(match[2]))
			if summaryText == item.summary &&
				strings.Contains(ariaLabel, item.levelLabel) &&
				strings.Contains(ariaLabel, item.summary) {
				summaryFound = true
				break
			}
		}
		if !summaryFound {
			t.Errorf("logs missing accessible level summary containing %q and %q in %s response", item.levelLabel, item.summary, item.name)
		}
		for _, value := range []string{"debug", "info", "warn", "error"} {
			if !strings.Contains(body, `name="level" value="`+value+`"`) {
				t.Errorf("logs missing level option %q in %s response", value, item.name)
			}
		}
		for _, value := range []string{"warn", "error"} {
			if !strings.Contains(body, `name="level" value="`+value+`" checked`) {
				t.Errorf("logs missing selected level %q in %s response", value, item.name)
			}
		}
		for _, name := range []string{"method", "path", "status", "reason", "scope"} {
			if strings.Contains(body, `name="`+name+`"`) {
				t.Errorf("logs retained removed filter %q in %s response", name, item.name)
			}
		}
	}
}

func TestBuildLogRowsProjectsSummaryAndStatusClasses(t *testing.T) {
	str := tr(LangEN)
	httpRecord := logging.Record{
		Time:    time.Date(2026, 8, 6, 11, 59, 0, 123456789, time.FixedZone("example", 8*60*60)),
		Level:   "WARN",
		Message: "http request",
		Fields: map[string]any{
			"method": "POST",
			"path":   "/api/v2/torrents/add",
			"status": float64(409),
		},
	}
	appRecord := logging.Record{Level: "info", Message: "worker started", Fields: map[string]any{}}
	statusAppRecord := logging.Record{Level: "debug", Message: "worker response", Fields: map[string]any{"status": 201}}
	rows := buildLogRows([]logging.Record{httpRecord, appRecord, statusAppRecord}, str)
	if len(rows) != 3 {
		t.Fatalf("rows = %d, want 3", len(rows))
	}
	httpRow := rows[0]
	if got, want := httpRow.Datetime, "2026-08-06T03:59:00.123456789Z"; got != want {
		t.Errorf("datetime = %q, want %q", got, want)
	}
	if httpRow.TimeFallback != "—" || httpRow.Level != "Warning" || httpRow.LevelClass != "warn" {
		t.Errorf("http row presentation = %#v", httpRow)
	}
	if httpRow.Subject != "/api/v2/torrents/add" || httpRow.Status != "409" || httpRow.StatusClass != "4xx" || !httpRow.HasStatus {
		t.Errorf("http row projection = %#v", httpRow)
	}
	if appRow := rows[1]; appRow.Subject != "worker started" || appRow.HasStatus {
		t.Errorf("application row projection = %#v", appRow)
	}
	if appRow := rows[2]; appRow.Subject != "worker response" || appRow.Status != "201" || appRow.StatusClass != "2xx" || !appRow.HasStatus {
		t.Errorf("application status projection = %#v", appRow)
	}
}

func TestBuildLogRowEmptyHTTPPathUsesEmDash(t *testing.T) {
	row := buildLogRow(logging.Record{
		Level:   "error",
		Message: "http request",
		Fields:  map[string]any{"method": "GET", "path": ""},
	}, tr(LangEN))
	if row.Subject != "—" {
		t.Fatalf("empty HTTP path subject = %q, want em dash", row.Subject)
	}
}

func TestBuildLogRowHTTPMessageWithoutRequestFieldsUsesEmDash(t *testing.T) {
	row := buildLogRow(logging.Record{
		Level:   "error",
		Message: "http request",
	}, tr(LangEN))
	if row.Subject != "—" {
		t.Fatalf("HTTP message without request fields subject = %q, want em dash", row.Subject)
	}
}

func TestLogRowLevelAndStatusClassMappings(t *testing.T) {
	for _, level := range []string{"debug", "info", "warn", "error"} {
		if got := logLevelClass(level); got != level {
			t.Errorf("level %q class = %q", level, got)
		}
	}
	if got := logLevelClass("TRACE"); got != "neutral" {
		t.Errorf("unknown level class = %q, want neutral", got)
	}
	for _, item := range []struct {
		value any
		class string
		ok    bool
	}{
		{float64(204), "2xx", true},
		{float64(302), "3xx", true},
		{float64(404), "4xx", true},
		{float64(503), "5xx", true},
		{float64(99), "neutral", true},
		{float64(0), "", false},
		{nil, "", false},
	} {
		_, gotClass, got := logStatus(item.value)
		if got != item.ok || (got && gotClass != item.class) {
			t.Errorf("status %#v = (%q, %v), want (%q, %v)", item.value, gotClass, got, item.class, item.ok)
		}
	}
}
func TestLogsFilterLayoutContract(t *testing.T) {
	css, err := assets.ReadFile("static/app.css")
	if err != nil {
		t.Fatalf("read app.css: %v", err)
	}
	body := string(css)
	for _, want := range []string{
		".log-filters {\n  display: grid;\n  grid-template-columns: repeat(auto-fit, minmax(160px, 1fr));",
		".log-filters > * {\n  min-width: 0;",
		".log-filters input,\n.log-filters select,\n.log-filters .log-level-menu,\n.log-filters .log-level-menu summary {\n  width: 100%;",
		".log-level-menu summary {\n  display: flex;\n  min-height: 30px;",
		".log-summary {\n  display: grid;",
		"grid-template-columns: minmax(160px, 180px) minmax(72px, 96px) minmax(0, 1fr) minmax(72px, 96px) 16px;",
		".log-level-badge,\n.log-status-badge {",
		"max-width: 100%;\n  overflow: hidden;",
		"text-overflow: ellipsis;",
		".log-level-debug",
		".log-level-info",
		".log-level-warn",
		".log-level-error",
		".log-status-2xx",
		".log-status-3xx",
		".log-status-4xx",
		".log-status-5xx",
		".log-status-neutral",
		"@media (max-width: 640px)",
		"grid-template-areas:",
		"grid-template-columns: minmax(0, 1fr) minmax(72px, 96px) 16px;",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.css missing logs layout contract %q", want)
		}
	}
}

func TestLogsLocalTimeScriptContract(t *testing.T) {
	js, err := assets.ReadFile("static/app.js")
	if err != nil {
		t.Fatalf("read app.js: %v", err)
	}
	body := string(js)
	for _, want := range []string{
		`new Intl.DateTimeFormat(document.documentElement.lang || undefined`,
		`year: "numeric"`,
		`month: "2-digit"`,
		`day: "2-digit"`,
		`hour: "2-digit"`,
		`minute: "2-digit"`,
		`second: "2-digit"`,
		`data-local-time`,
		`catch`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("app.js missing local time contract %q", want)
		}
	}
}
