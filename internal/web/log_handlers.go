package web

import (
	"encoding/json"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/turygo/cd211/internal/logging"
)

const emptyLogSubject = "—"

type LogLevelOption struct {
	Value    string
	Label    string
	Selected bool
}

type LogRow struct {
	Datetime     string
	TimeFallback string
	Level        string
	LevelClass   string
	Subject      string
	Status       string
	StatusClass  string
	HasStatus    bool
	Message      string
	Fields       map[string]any
}

type LogsView struct {
	PageMeta
	From, To, Search string
	Levels           []LogLevelOption
	LevelSummary     string
	Rows             []LogRow
	Invalid          bool
}

func (h *handler) logs(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	query, ok := parseLogQuery(r, h.clock.Now().UTC())
	str := tr(lang)
	view := LogsView{
		PageMeta: PageMeta{Title: str.TitleLogs, ActiveNav: "logs", CSRFToken: h.authSession(r).CSRFToken, Lang: lang, OtherLang: otherLang(lang), Path: r.URL.RequestURI(), Str: str},
		From:     query.from, To: query.to, Search: query.search, Levels: logLevelOptions(str, query.levels), LevelSummary: logLevelSummary(str, query.levels), Invalid: !ok,
	}
	if ok {
		records, err := h.logReader.Query(query.query)
		if err == nil {
			view.Rows = buildLogRows(records, str)
		}
	}
	status := http.StatusOK
	if !ok {
		status = http.StatusBadRequest
	}
	w.Header().Set("Cache-Control", "no-store")
	h.render(w, status, "logs", view)
}

type parsedLogQuery struct {
	query    logging.Query
	from, to string
	levels   []string
	search   string
}

var logLevelValues = []string{"debug", "info", "warn", "error"}

func parseLogQuery(r *http.Request, now time.Time) (parsedLogQuery, bool) {
	values := r.URL.Query()
	day := now.UTC().Format("2006-01-02")
	out := parsedLogQuery{from: values.Get("from"), to: values.Get("to"), search: values.Get("q")}
	if out.from == "" {
		out.from = day
	}
	if out.to == "" {
		out.to = day
	}
	rawLevels, levelsPresent := values["level"]
	levelSet, levelSetPresent := values["level_set"]
	if levelSetPresent && (len(levelSet) != 1 || levelSet[0] != "1") {
		return out, false
	}
	if levelSetPresent && !levelsPresent {
		return out, false
	}
	if !levelsPresent {
		rawLevels = []string{"warn", "error"}
	}
	selected := map[string]bool{}
	for _, level := range rawLevels {
		if level == "" {
			return out, false
		}
		switch level {
		case "debug", "info", "warn", "error":
			selected[level] = true
		default:
			return out, false
		}
	}
	for _, level := range logLevelValues {
		if selected[level] {
			out.levels = append(out.levels, level)
		}
	}
	from, err1 := time.Parse("2006-01-02", out.from)
	to, err2 := time.Parse("2006-01-02", out.to)
	if err1 != nil || err2 != nil || from.After(to) || to.Sub(from) > 6*24*time.Hour || len(out.search) > 256 {
		return out, false
	}
	levels := make(map[string]bool, len(out.levels))
	for _, level := range out.levels {
		levels[level] = true
	}
	q := logging.Query{From: from.UTC(), To: to.UTC(), Levels: levels, Search: out.search, Max: 200}
	out.query = q
	return out, true
}

func logLevelOptions(str *Strings, selected []string) []LogLevelOption {
	chosen := make(map[string]bool, len(selected))
	for _, level := range selected {
		chosen[level] = true
	}
	labels := map[string]string{"debug": str.LogDebug, "info": str.LogInfo, "warn": str.LogWarning, "error": str.LogError}
	options := make([]LogLevelOption, 0, len(logLevelValues))
	for _, level := range logLevelValues {
		options = append(options, LogLevelOption{Value: level, Label: labels[level], Selected: chosen[level]})
	}
	return options
}

func logLevelSummary(str *Strings, selected []string) string {
	labels := map[string]string{"debug": str.LogDebug, "info": str.LogInfo, "warn": str.LogWarning, "error": str.LogError}
	values := make([]string, 0, len(selected))
	for _, level := range logLevelValues {
		if labels[level] == "" {
			continue
		}
		for _, chosen := range selected {
			if chosen == level {
				values = append(values, labels[level])
				break
			}
		}
	}
	return strings.Join(values, str.LogLevelSeparator)
}

func buildLogRows(records []logging.Record, str *Strings) []LogRow {
	rows := make([]LogRow, 0, len(records))
	for _, record := range records {
		rows = append(rows, buildLogRow(record, str))
	}
	return rows
}

func buildLogRow(record logging.Record, str *Strings) LogRow {
	levelClass := logLevelClass(record.Level)
	level := record.Level
	if levelClass == "warn" {
		level = str.LogWarning
	}
	subject := record.Message
	_, hasPath := record.Fields["path"]
	_, hasMethod := record.Fields["method"]
	if record.Message == "http request" || hasPath || hasMethod {
		subject = emptyLogSubject
		if path, ok := record.Fields["path"].(string); ok && path != "" {
			subject = path
		}
	}
	status, statusClass, hasStatus := logStatus(record.Fields["status"])
	return LogRow{
		Datetime:     record.Time.UTC().Format(time.RFC3339Nano),
		TimeFallback: emptyLogSubject,
		Level:        level,
		LevelClass:   levelClass,
		Subject:      subject,
		Status:       status,
		StatusClass:  statusClass,
		HasStatus:    hasStatus,
		Message:      record.Message,
		Fields:       record.Fields,
	}
}

func logLevelClass(level string) string {
	switch strings.ToLower(level) {
	case "debug", "info", "warn", "error":
		return strings.ToLower(level)
	default:
		return "neutral"
	}
}

func logStatus(value any) (string, string, bool) {
	var number float64
	var text string
	switch value := value.(type) {
	case int:
		number, text = float64(value), strconv.Itoa(value)
	case int64:
		number, text = float64(value), strconv.FormatInt(value, 10)
	case uint:
		number, text = float64(value), strconv.FormatUint(uint64(value), 10)
	case uint64:
		number, text = float64(value), strconv.FormatUint(value, 10)
	case float32:
		number, text = float64(value), strconv.FormatFloat(float64(value), 'f', -1, 32)
	case float64:
		number, text = value, strconv.FormatFloat(value, 'f', -1, 64)
	default:
		return "", "", false
	}
	if number <= 0 || math.IsNaN(number) || math.IsInf(number, 0) {
		return "", "", false
	}
	class := "neutral"
	switch {
	case number >= 200 && number < 300:
		class = "2xx"
	case number >= 300 && number < 400:
		class = "3xx"
	case number >= 400 && number < 500:
		class = "4xx"
	case number >= 500 && number < 600:
		class = "5xx"
	}
	return text, class, true
}

func logFieldsJSON(fields map[string]any) string { b, _ := json.Marshal(fields); return string(b) }
