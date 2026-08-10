package web

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/turygo/cd211/internal/outbox"
)

const (
	defaultDeliveryLimit = 50
	maxDeliveryLimit     = 100
)

// webhookID parses the {id} path segment as a positive endpoint/delivery ID.
func webhookID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		return 0, false
	}
	return id, true
}

// webhooks renders the endpoint list.
func (h *handler) webhooks(w http.ResponseWriter, r *http.Request) {
	lang := requestLang(r)
	notice, success := "", false
	switch r.URL.Query().Get("updated") {
	case "1":
		notice, success = tr(lang).EndpointSaved, true
	}
	if r.URL.Query().Get("test") == "1" {
		notice, success = tr(lang).TestEnqueued, true
	}
	endpoints, err := h.webhookRepo.ListWebhookEndpoints(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	page := buildWebhooksView(endpoints, h.authSession(r).CSRFToken, lang, notice, success)
	page.Path = r.URL.RequestURI()
	h.render(w, http.StatusOK, "webhooks", page)
}

// webhookNew renders the create-endpoint form.
func (h *handler) webhookNew(w http.ResponseWriter, r *http.Request) {
	page := buildWebhookFormView(0, WebhookFormValues{Enabled: true}, h.authSession(r).CSRFToken, requestLang(r), false, "")
	h.render(w, http.StatusOK, "webhook-form", page)
}

// webhookCreate handles POST /webhooks. Success renders the one-time signing
// secret page; the secret exists only in this response. The form can carry a
// bearer token and a credential-bearing query URL, so every response from
// this handler — malformed, invalid, failed, or successful — is marked
// no-store before the form is parsed.
func (h *handler) webhookCreate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	values, errMsg, ok := parseWebhookForm(r, false)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if errMsg != "" {
		h.renderWebhookForm(w, r, http.StatusBadRequest, 0, false, values, errMsg)
		return
	}
	input := outbox.EndpointInput{
		Name: values.Name, URL: values.URL,
		SubscribeCompleted: values.Completed, SubscribeFailed: values.Failed,
		BearerToken: values.Bearer,
		Enabled:     &values.Enabled,
	}
	endpoint, err := h.webhookRepo.CreateWebhookEndpoint(r.Context(), input, h.clock.Now().UTC())
	if err != nil {
		repositoryError(w, err)
		return
	}
	h.renderWebhookSecret(w, r, endpoint)
}

// webhookEdit renders the edit-endpoint form. Query-bearing stored URLs are
// never placed into the form field; the store's redacted display form is used
// otherwise.
func (h *handler) webhookEdit(w http.ResponseWriter, r *http.Request) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	endpoint, err := h.webhookRepo.GetWebhookEndpoint(r.Context(), id)
	if err != nil {
		repositoryError(w, err)
		return
	}
	page := buildWebhookFormViewForEndpoint(endpoint, h.authSession(r).CSRFToken, requestLang(r))
	h.render(w, http.StatusOK, "webhook-form", page)
}

// webhookUpdate handles POST /webhooks/{id}. An empty URL preserves the stored
// URL; bearer semantics follow the form's keep/clear/replace contract. Like
// create, every response is marked no-store before parsing because the form
// can carry credentials.
func (h *handler) webhookUpdate(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	values, errMsg, ok := parseWebhookForm(r, true)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if errMsg != "" {
		h.renderWebhookForm(w, r, http.StatusBadRequest, id, true, values, errMsg)
		return
	}
	input := outbox.EndpointInput{
		Name: values.Name, URL: values.URL,
		SubscribeCompleted: values.Completed, SubscribeFailed: values.Failed,
		BearerToken: values.Bearer, ClearBearerToken: values.ClearBearer,
		Enabled: &values.Enabled,
	}
	if _, err := h.webhookRepo.UpdateWebhookEndpoint(r.Context(), id, input, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks?updated=1", http.StatusSeeOther)
}

// webhookEnable resumes a disabled endpoint.
func (h *handler) webhookEnable(w http.ResponseWriter, r *http.Request) {
	h.setWebhookEnabled(w, r, true)
}

// webhookDisable pauses a disabled endpoint; pending deliveries resume on
// re-enable.
func (h *handler) webhookDisable(w http.ResponseWriter, r *http.Request) {
	h.setWebhookEnabled(w, r, false)
}

func (h *handler) setWebhookEnabled(w http.ResponseWriter, r *http.Request, enabled bool) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if err := h.webhookRepo.SetWebhookEndpointEnabled(r.Context(), id, enabled, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks", http.StatusSeeOther)
}

// webhookRotateSecret replaces the signing secret and shows the replacement
// exactly once.
func (h *handler) webhookRotateSecret(w http.ResponseWriter, r *http.Request) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	endpoint, err := h.webhookRepo.RotateWebhookEndpointSecret(r.Context(), id, h.clock.Now().UTC())
	if err != nil {
		repositoryError(w, err)
		return
	}
	h.renderWebhookSecret(w, r, endpoint)
}

// webhookDelete soft-deletes the endpoint; pending and dead deliveries are
// cancelled by the store while history is retained.
func (h *handler) webhookDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if err := h.webhookRepo.DeleteWebhookEndpoint(r.Context(), id, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks", http.StatusSeeOther)
}

// webhookTest enqueues a durable webhook.test delivery through the outbox so
// it follows the same lease/retry/dead-letter path as domain events. No HTTP
// request is made from this handler.
func (h *handler) webhookTest(w http.ResponseWriter, r *http.Request) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if _, err := h.webhookRepo.EnqueueTestDelivery(r.Context(), id, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/webhooks?test=1", http.StatusSeeOther)
}

// webhookDeliveries renders the filtered delivery-history page. Invalid
// filters, cursors, and limits render 400 rather than broadening results.
func (h *handler) webhookDeliveries(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()

	selectedEndpoint := ""
	var endpointID *int64
	if values, has := query["endpoint"]; has {
		if len(values) != 1 {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		id, err := strconv.ParseInt(values[0], 10, 64)
		if err != nil || id <= 0 {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		endpointID = &id
		selectedEndpoint = values[0]
	}

	selectedEvent := ""
	if values, has := query["event"]; has {
		if len(values) != 1 || !validWebhookEventType(values[0]) {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		selectedEvent = values[0]
	}

	selectedStatus := ""
	if values, has := query["status"]; has {
		if len(values) != 1 || !validWebhookDeliveryStatus(values[0]) {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		selectedStatus = values[0]
	}

	limit := defaultDeliveryLimit
	if values, has := query["limit"]; has {
		if len(values) != 1 {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		parsed, err := strconv.Atoi(values[0])
		if err != nil || parsed < 1 || parsed > maxDeliveryLimit {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		limit = parsed
	}

	cursor := ""
	if values, has := query["cursor"]; has {
		if len(values) != 1 {
			plain(w, http.StatusBadRequest, "Bad Request\n")
			return
		}
		if values[0] != "" {
			if !validOpaqueCursor(values[0]) {
				plain(w, http.StatusBadRequest, "Bad Request\n")
				return
			}
			if _, _, err := outbox.DecodeCursor(values[0]); err != nil {
				plain(w, http.StatusBadRequest, "Bad Request\n")
				return
			}
			cursor = values[0]
		}
	}

	endpoints, err := h.webhookRepo.ListWebhookEndpoints(r.Context())
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	deliveries, page, err := h.webhookRepo.ListWebhookDeliveries(r.Context(), outbox.DeliveryFilter{
		EndpointID: endpointID,
		EventType:  selectedEvent,
		Status:     outbox.DeliveryStatus(selectedStatus),
		Cursor:     cursor,
		Limit:      limit,
	})
	if err != nil {
		plain(w, http.StatusInternalServerError, "Internal Server Error\n")
		return
	}
	notice := ""
	if r.URL.Query().Get("replayed") == "1" {
		notice = tr(requestLang(r)).ReplayEnqueued
	}
	view := buildWebhookDeliveriesView(deliveries, endpoints, selectedEndpoint, selectedEvent, selectedStatus, page.NextCursor, page.HasMore, limit, h.authSession(r).CSRFToken, requestLang(r), notice)
	view.Path = r.URL.RequestURI()
	h.render(w, http.StatusOK, "webhook-deliveries", view)
}

// webhookReplay reopens a dead-letter delivery through the outbox.
func (h *handler) webhookReplay(w http.ResponseWriter, r *http.Request) {
	id, ok := webhookID(r)
	if !ok {
		plain(w, http.StatusBadRequest, "Bad Request\n")
		return
	}
	if _, err := h.webhookRepo.ReplayWebhookDelivery(r.Context(), id, h.clock.Now().UTC()); err != nil {
		repositoryError(w, err)
		return
	}
	http.Redirect(w, r, "/webhook-deliveries?replayed=1", http.StatusSeeOther)
}

// renderWebhookForm re-renders the endpoint form with a validation notice.
// Error-state values are scrubbed before rendering: the bearer is always
// cleared and the submitted URL is reduced to its query-redacted display form
// (or cleared entirely), so credentials never echo back into the page.
func (h *handler) renderWebhookForm(w http.ResponseWriter, r *http.Request, status int, endpointID int64, editing bool, values WebhookFormValues, errorText string) {
	values.Bearer = ""
	values.URL = webhookURLErrorDisplay(values.URL)
	page := buildWebhookFormView(endpointID, values, h.authSession(r).CSRFToken, requestLang(r), editing, errorText)
	h.render(w, status, "webhook-form", page)
}

// renderWebhookSecret renders the one-time signing-secret page. The response
// must never be cached or replayed, so Cache-Control: no-store is mandatory.
func (h *handler) renderWebhookSecret(w http.ResponseWriter, r *http.Request, endpoint outbox.Endpoint) {
	w.Header().Set("Cache-Control", "no-store")
	page := buildWebhookSecretView(endpoint, h.authSession(r).CSRFToken, requestLang(r))
	h.render(w, http.StatusOK, "webhook-secret", page)
}

// parseWebhookForm extracts and validates the endpoint form. On edit an empty
// URL means "keep the stored value" and is not an error here; the store
// resolves it. Validation failures return a translated notice and ok=true so
// the caller can re-render the form with 400; a malformed submission returns
// ok=false.
func parseWebhookForm(r *http.Request, editing bool) (WebhookFormValues, string, bool) {
	str := tr(requestLang(r))
	var form WebhookFormValues
	var ok bool
	if form.Name, ok = exactPostValue(r, "name"); !ok {
		return form, "", false
	}
	if form.URL, ok = exactPostValue(r, "url"); !ok {
		return form, "", false
	}
	// The bearer input is optional: absent, empty, or clear all mean "no new
	// token", which the store resolves to keep/clear/replace on update.
	form.Bearer = r.PostForm.Get("bearer")
	// The enabled select is required and accepts exactly the two values the
	// form renders. Missing or any other value is malformed and never
	// defaults, so the store always receives an explicit pointer.
	enabledValue, enabledOK := exactPostValue(r, "enabled")
	enabled, validEnabled := parseWebhookEnabled(enabledValue)
	if !enabledOK || !validEnabled {
		return form, "", false
	}
	form.Enabled = enabled
	form.Completed = r.PostForm.Get("completed") == "true"
	form.Failed = r.PostForm.Get("failed") == "true"
	if editing {
		form.ClearBearer = r.PostForm.Get("clear_bearer") == "true"
	}
	form.Name = strings.TrimSpace(form.Name)
	form.URL = strings.TrimSpace(form.URL)
	form.Bearer = strings.TrimSpace(form.Bearer)
	switch {
	case !validEndpointName(form.Name):
		return form, str.EndpointNameInvalid, true
	case !editing && form.URL == "":
		return form, str.EndpointURLInvalid, true
	case form.URL != "" && !validWebhookURL(form.URL):
		return form, str.EndpointURLInvalid, true
	case !form.Completed && !form.Failed:
		return form, str.SubscriptionRequired, true
	case len([]byte(form.Bearer)) > 4096:
		return form, str.BearerTooLong, true
	}
	return form, "", true
}

// parseWebhookEnabled accepts exactly the two enabled values the form
// renders: "true" and "false". The select is required, so missing or any
// other value is malformed and never defaults to a boolean.
func parseWebhookEnabled(raw string) (bool, bool) {
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	default:
		return false, false
	}
}

// webhookURLErrorDisplay reduces a submitted URL to its display-only form for
// error re-renders. A URL that parses as an otherwise valid absolute http or
// https URL without userinfo or a fragment keeps scheme+host+path, with any
// non-empty query replaced by "?…"; an empty query renders without the
// marker. Anything else renders empty, so userinfo credentials and malformed
// input are never echoed. Persisted input is never changed by this transform.
func webhookURLErrorDisplay(raw string) string {
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return ""
	}
	display := parsed.Scheme + "://" + parsed.Host + parsed.Path
	if parsed.RawQuery != "" {
		display += "?…"
	}
	return display
}

// validEndpointName mirrors the persisted endpoint-name rules: trimmed,
// required, 1-64 UTF-8 bytes, and free of control characters.
func validEndpointName(raw string) bool {
	if !utf8.ValidString(raw) {
		return false
	}
	name := strings.TrimSpace(raw)
	if name == "" || len([]byte(name)) > 64 || strings.ContainsFunc(name, unicode.IsControl) {
		return false
	}
	return true
}

// validWebhookURL mirrors the persisted endpoint-URL rules: absolute http or
// https, at most 2048 bytes, without userinfo or a fragment.
func validWebhookURL(raw string) bool {
	if raw == "" || len(raw) > 2048 || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) {
		return false
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return false
	}
	return true
}
