package web

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/turygo/cd211/internal/outbox"
)

// WebhookFilterOption is one selectable value in a delivery-history filter.
type WebhookFilterOption struct {
	Value  string
	Label  string
	Active bool
}

// WebhooksView renders the endpoint list page.
type WebhooksView struct {
	PageMeta
	Rows    []WebhookRow
	Notice  string
	Success bool
}

// WebhookRow renders one endpoint without exposing its secrets: DisplayURL is
// the store-provided redacted form, and HMACSecret/BearerToken are never
// present in list reads.
type WebhookRow struct {
	ID        int64
	Name      string
	URL       string
	HasQuery  bool
	Enabled   bool
	Completed bool
	Failed    bool
}

// WebhookFormView renders the create or edit endpoint form.
type WebhookFormView struct {
	PageMeta
	Values     WebhookFormValues
	Editing    bool
	EndpointID int64
	StoredURL  string
	Error      string
	Notice     string
}

// WebhookFormValues carries the prefilled endpoint form fields. Bearer is
// always rendered empty; an empty submission keeps the stored token and a
// checked ClearBearer removes it (edit only).
type WebhookFormValues struct {
	Name        string
	URL         string
	Bearer      string
	Enabled     bool
	Completed   bool
	Failed      bool
	ClearBearer bool
}

// WebhookSecretView renders the one-time signing-secret reveal page. It must
// only be served with Cache-Control: no-store and never linked or redirected.
type WebhookSecretView struct {
	PageMeta
	EndpointName string
	Secret       string
}

// WebhookDeliveriesView renders the delivery-history page.
type WebhookDeliveriesView struct {
	PageMeta
	Rows             []WebhookDeliveryRow
	Endpoints        []WebhookFilterOption
	Events           []WebhookFilterOption
	Statuses         []WebhookFilterOption
	SelectedEndpoint string
	SelectedEvent    string
	SelectedStatus   string
	Limit            int
	NextURL          string
	Notice           string
}

// WebhookDeliveryRow renders one delivery. LastError is already sanitized and
// bounded by the store; LastHTTPStatus is zero until an attempt recorded one.
type WebhookDeliveryRow struct {
	ID             int64
	EndpointName   string
	EventLabel     string
	Status         string
	StatusLabel    string
	AttemptCount   int64
	LastHTTPStatus string
	LastError      string
	NextAttemptAt  string
	DeliveredAt    string
	UpdatedAt      string
	CanReplay      bool
}

// deliveryFilterOptions builds the event-type filter choices. Only the three
// deliverable event types exist in delivery history.
func deliveryFilterOptions(selected string, str *Strings) []WebhookFilterOption {
	events := []struct {
		value string
		label string
	}{
		{outbox.EventTypeCompleted, str.States.Completed},
		{outbox.EventTypeFailed, str.States.Failed},
		{outbox.EventTypeTest, str.EventTest},
	}
	options := make([]WebhookFilterOption, 0, len(events))
	for _, event := range events {
		options = append(options, WebhookFilterOption{Value: event.value, Label: event.label, Active: event.value == selected})
	}
	return options
}

// deliveryStatusOptions builds the delivery-status filter choices.
func deliveryStatusOptions(selected string, str *Strings) []WebhookFilterOption {
	statuses := []struct {
		value string
		label string
	}{
		{string(outbox.StatusPending), str.DeliveryPending},
		{string(outbox.StatusDelivering), str.DeliveryDelivering},
		{string(outbox.StatusSucceeded), str.DeliverySucceeded},
		{string(outbox.StatusDead), str.DeliveryDead},
		{string(outbox.StatusCancelled), str.States.Cancelled},
	}
	options := make([]WebhookFilterOption, 0, len(statuses))
	for _, status := range statuses {
		options = append(options, WebhookFilterOption{Value: status.value, Label: status.label, Active: status.value == selected})
	}
	return options
}

func buildWebhooksView(endpoints []outbox.Endpoint, csrfToken string, lang Lang, notice string, success bool) WebhooksView {
	str := tr(lang)
	rows := make([]WebhookRow, 0, len(endpoints))
	for _, endpoint := range endpoints {
		rows = append(rows, WebhookRow{
			ID:        endpoint.ID,
			Name:      endpoint.Name,
			URL:       endpoint.DisplayURL,
			HasQuery:  strings.Contains(endpoint.DisplayURL, "?…"),
			Enabled:   endpoint.Enabled,
			Completed: endpoint.SubscribeCompleted,
			Failed:    endpoint.SubscribeFailed,
		})
	}
	page := WebhooksView{
		PageMeta: pageMeta(str.TitleWebhooks, "webhooks", csrfToken, lang),
		Rows:     rows,
		Notice:   notice,
		Success:  success,
	}
	page.Path = "/webhooks"
	return page
}

func buildWebhookFormView(endpointID int64, values WebhookFormValues, csrfToken string, lang Lang, editing bool, errorText string) WebhookFormView {
	str := tr(lang)
	title := str.AddEndpoint
	if editing {
		title = str.EditEndpoint
	}
	page := WebhookFormView{
		PageMeta:   pageMeta(title, "webhooks", csrfToken, lang),
		Values:     values,
		Editing:    editing,
		EndpointID: endpointID,
		Error:      errorText,
	}
	page.Path = "/webhooks"
	return page
}

// buildWebhookFormViewForEndpoint prefills the edit form from a stored
// endpoint. Query-bearing URLs are never placed into the form field; the
// redacted display form is shown as a note instead.
func buildWebhookFormViewForEndpoint(endpoint outbox.Endpoint, csrfToken string, lang Lang) WebhookFormView {
	values := WebhookFormValues{
		Name:      endpoint.Name,
		Enabled:   endpoint.Enabled,
		Completed: endpoint.SubscribeCompleted,
		Failed:    endpoint.SubscribeFailed,
	}
	page := buildWebhookFormView(endpoint.ID, values, csrfToken, lang, true, "")
	if !strings.Contains(endpoint.DisplayURL, "?…") {
		page.Values.URL = endpoint.DisplayURL
	} else {
		page.StoredURL = endpoint.DisplayURL
	}
	return page
}

func buildWebhookSecretView(endpoint outbox.Endpoint, csrfToken string, lang Lang) WebhookSecretView {
	str := tr(lang)
	page := WebhookSecretView{
		PageMeta:     pageMeta(str.SecretTitle, "webhooks", csrfToken, lang),
		EndpointName: endpoint.Name,
		Secret:       endpoint.HMACSecret,
	}
	page.Path = "/webhooks"
	return page
}

func buildWebhookDeliveriesView(deliveries []outbox.Delivery, endpoints []outbox.Endpoint, selectedEndpoint, selectedEvent, selectedStatus, nextCursor string, hasMore bool, limit int, csrfToken string, lang Lang, notice string) WebhookDeliveriesView {
	str := tr(lang)
	rows := make([]WebhookDeliveryRow, 0, len(deliveries))
	for _, delivery := range deliveries {
		rows = append(rows, WebhookDeliveryRow{
			ID:             delivery.ID,
			EndpointName:   delivery.EndpointName,
			EventLabel:     displayEventType(delivery.EventType, str),
			Status:         string(delivery.Status),
			StatusLabel:    displayDeliveryStatus(delivery.Status, str),
			AttemptCount:   delivery.AttemptCount,
			LastHTTPStatus: displayHTTPStatus(delivery.LastHTTPStatus),
			LastError:      delivery.LastError,
			NextAttemptAt:  displayOptionalTime(delivery.NextAttemptAt, str.NotScheduled, str),
			DeliveredAt:    displayOptionalTime(delivery.DeliveredAt, str.NotCompleted, str),
			UpdatedAt:      displayTime(delivery.UpdatedAt, str),
			CanReplay:      delivery.Status == outbox.StatusDead,
		})
	}
	endpointOptions := make([]WebhookFilterOption, 0, len(endpoints))
	for _, endpoint := range endpoints {
		value := fmt.Sprintf("%d", endpoint.ID)
		endpointOptions = append(endpointOptions, WebhookFilterOption{Value: value, Label: endpoint.Name, Active: value == selectedEndpoint})
	}
	sort.Slice(endpointOptions, func(i, j int) bool {
		return strings.ToLower(endpointOptions[i].Label) < strings.ToLower(endpointOptions[j].Label)
	})
	nextURL := ""
	if hasMore && nextCursor != "" {
		nextURL = "/webhook-deliveries?" + deliveryNextURL(selectedEndpoint, selectedEvent, selectedStatus, nextCursor, limit)
	}
	page := WebhookDeliveriesView{
		PageMeta:         pageMeta(str.TitleDeliveries, "webhooks", csrfToken, lang),
		Rows:             rows,
		Endpoints:        endpointOptions,
		Events:           deliveryFilterOptions(selectedEvent, str),
		Statuses:         deliveryStatusOptions(selectedStatus, str),
		SelectedEndpoint: selectedEndpoint,
		SelectedEvent:    selectedEvent,
		SelectedStatus:   selectedStatus,
		Limit:            limit,
		NextURL:          nextURL,
		Notice:           notice,
	}
	page.Path = "/webhook-deliveries"
	return page
}

func displayEventType(eventType string, str *Strings) string {
	switch eventType {
	case outbox.EventTypeCompleted:
		return str.States.Completed
	case outbox.EventTypeFailed:
		return str.States.Failed
	case outbox.EventTypeTest:
		return str.EventTest
	default:
		return eventType
	}
}

func displayDeliveryStatus(status outbox.DeliveryStatus, str *Strings) string {
	switch status {
	case outbox.StatusPending:
		return str.DeliveryPending
	case outbox.StatusDelivering:
		return str.DeliveryDelivering
	case outbox.StatusSucceeded:
		return str.DeliverySucceeded
	case outbox.StatusDead:
		return str.DeliveryDead
	case outbox.StatusCancelled:
		return str.States.Cancelled
	default:
		return string(status)
	}
}

// displayHTTPStatus renders a delivery's last HTTP status; zero means no
// attempt has recorded one.
func displayHTTPStatus(status int64) string {
	if status == 0 {
		return "—"
	}
	return fmt.Sprintf("%d", status)
}

// validWebhookEventType reports whether the value is an exact deliverable
// event type that can appear in delivery history.
func validWebhookEventType(value string) bool {
	switch value {
	case outbox.EventTypeCompleted, outbox.EventTypeFailed, outbox.EventTypeTest:
		return true
	default:
		return false
	}
}

// validWebhookDeliveryStatus reports whether the value is an exact delivery
// status used by the history filter.
func validWebhookDeliveryStatus(value string) bool {
	switch outbox.DeliveryStatus(value) {
	case outbox.StatusPending, outbox.StatusDelivering, outbox.StatusSucceeded, outbox.StatusDead, outbox.StatusCancelled:
		return true
	default:
		return false
	}
}

// validOpaqueCursor reports whether a submitted history cursor is plausibly
// the opaque token issued by the store: URL-safe base64 within a sane bound.
// The store performs the full structural validation via DecodeCursor.
func validOpaqueCursor(value string) bool {
	if value == "" || len(value) > 256 {
		return false
	}
	_, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil
}

// deliveryNextURL preserves the active filters and limit while advancing to
// the next cursor page. Values are percent-escaped manually so the URL stays
// valid inside the template attribute.
func deliveryNextURL(selectedEndpoint, selectedEvent, selectedStatus, cursor string, limit int) string {
	query := make([]string, 0, 8)
	if selectedEndpoint != "" {
		query = append(query, "endpoint="+urlQueryEscape(selectedEndpoint))
	}
	if selectedEvent != "" {
		query = append(query, "event="+urlQueryEscape(selectedEvent))
	}
	if selectedStatus != "" {
		query = append(query, "status="+urlQueryEscape(selectedStatus))
	}
	if limit != 50 {
		query = append(query, fmt.Sprintf("limit=%d", limit))
	}
	query = append(query, "cursor="+urlQueryEscape(cursor))
	return strings.Join(query, "&")
}

func urlQueryEscape(value string) string {
	var builder strings.Builder
	for _, character := range value {
		switch {
		case character >= 'a' && character <= 'z',
			character >= 'A' && character <= 'Z',
			character >= '0' && character <= '9',
			character == '-', character == '_', character == '.', character == '~':
			builder.WriteByte(byte(character))
		default:
			_, _ = fmt.Fprintf(&builder, "%%%02X", character)
		}
	}
	return builder.String()
}
