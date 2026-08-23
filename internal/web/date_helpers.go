package web

import (
	"html"
	"html/template"
	"strings"
	"time"
)

// localTime renders an ISO timestamp as a time element. The browser replaces
// its fallback text with the operator's local date and time format.
func localTime(value string) template.HTML {
	escaped := html.EscapeString(value)
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return template.HTML(escaped)
	}
	return template.HTML(`<time class="local-time" data-local-time datetime="` + escaped + `">` + escaped + `</time>`)
}

// localTimeFormat preserves translated text around one timestamp while
// allowing the timestamp itself to be localized in the browser.
func localTimeFormat(format, value string) template.HTML {
	const placeholder = "%s"
	index := strings.Index(format, placeholder)
	if index < 0 {
		return template.HTML(html.EscapeString(format))
	}
	prefix := html.EscapeString(format[:index])
	suffix := html.EscapeString(format[index+len(placeholder):])
	return template.HTML(prefix + string(localTime(value)) + suffix)
}
