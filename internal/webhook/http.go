package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/turygo/cd211/internal/outbox"
)

// HTTPClient is the narrow outbound surface used by Dispatcher; *http.Client
// satisfies it.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// NewHTTPClient builds the stdlib client the dispatcher uses in production: a
// 10-second overall timeout (the plan's fixed HTTP timeout) and redirects
// deliberately disabled, since only direct POST responses are trustworthy.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// deliver performs one signed HTTP POST attempt and classifies the outcome
// into an outbox.Result: succeeded on any 2xx, otherwise a deterministic
// retry (or a dead-letter once the next scheduled attempt would cross the
// 24-hour deadline). The endpoint URL, secrets, and payload are never logged
// or echoed into the result.
func (d *Dispatcher) deliver(ctx context.Context, claim *outbox.Claim) (outbox.Result, error) {
	now := d.clock.Now()
	timestamp := now.Unix()
	body := claim.Payload

	if !validEndpointURL(claim.URL) {
		return failureResult(claim, now, 0, outbox.ErrorCategoryRequest), nil
	}

	requestCtx, cancel := context.WithTimeout(ctx, d.config.RequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(requestCtx, http.MethodPost, claim.URL, bytes.NewReader(body))
	if err != nil {
		return failureResult(claim, now, 0, outbox.ErrorCategoryRequest), nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "CD211/"+d.config.Version)
	req.Header.Set("X-CD211-Event", claim.EventType)
	req.Header.Set("X-CD211-Event-ID", claim.EventID)
	req.Header.Set("X-CD211-Timestamp", strconv.FormatInt(timestamp, 10))
	req.Header.Set("X-CD211-Signature", "v1="+signature(claim.HMACSecret, timestamp, body))
	if claim.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+claim.BearerToken)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			// Parent shutdown: leave the lease in place; it expires and the
			// row becomes claimable again. Never commit during teardown.
			return outbox.Result{}, ctx.Err()
		}
		if requestCtx.Err() == context.DeadlineExceeded {
			return failureResult(claim, now, 0, outbox.ErrorCategoryTimeout), nil
		}
		return failureResult(claim, now, 0, classifyNetworkError(err)), nil
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, outbox.MaxResponseRead))
		_ = resp.Body.Close()
	}()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return outbox.Result{
			Status:         outbox.StatusSucceeded,
			LastHTTPStatus: int64(resp.StatusCode),
			DeliveredAt:    &now,
		}, nil
	}
	return failureResult(claim, now, int64(resp.StatusCode), fmt.Sprintf("HTTP %d", resp.StatusCode)), nil
}

// failureResult computes the bounded retry/dead-letter outcome for a failed
// attempt. The next attempt is `now + outbox.DeliveryDelay(attempt)`; once
// that would land at or after the first attempt plus outbox.RetryDeadline,
// the delivery is dead-lettered instead of retried. Between attempts the row
// is pending (never delivering): delivering marks the active lease only.
func failureResult(claim *outbox.Claim, now time.Time, status int64, category string) outbox.Result {
	next := now.Add(outbox.DeliveryDelay(claim.AttemptCount))
	result := outbox.Result{
		Status:         outbox.StatusPending,
		LastHTTPStatus: status,
		LastError:      category,
		NextAttemptAt:  &next,
	}
	if claim.FirstAttemptAt != nil && !next.Before(claim.FirstAttemptAt.Add(outbox.RetryDeadline)) {
		result.Status = outbox.StatusDead
		result.NextAttemptAt = nil
	}
	return result
}

// signature computes v1=<lowercase hex HMAC-SHA256(secret, "<ts>.<body>")>.
func signature(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write(strconv.AppendInt([]byte{}, timestamp, 10))
	_, _ = mac.Write([]byte{'.'})
	_, _ = mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// validEndpointURL accepts only absolute http/https URLs with no userinfo and
// no fragment. Private/LAN/localhost destinations are intentionally allowed.
func validEndpointURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
		return false
	}
	return parsed.User == nil && parsed.Fragment == ""
}

// classifyNetworkError maps a transport error into one of the fixed bounded
// categories from outbox. Raw url.Error strings (which embed the endpoint
// host) are never persisted.
func classifyNetworkError(err error) string {
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return outbox.ErrorCategoryTimeout
	}
	if errors.Is(err, os.ErrDeadlineExceeded) {
		return outbox.ErrorCategoryTimeout
	}
	if isTLSError(err) {
		return outbox.ErrorCategoryTLS
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Op == "dial" {
		return outbox.ErrorCategoryConnection
	}
	return outbox.ErrorCategoryRequest
}

// isTLSError reports whether the error chain contains a TLS handshake or
// certificate verification failure.
func isTLSError(err error) bool {
	var recordHeader tls.RecordHeaderError
	if errors.As(err, &recordHeader) {
		return true
	}
	var recordHeaderPtr *tls.RecordHeaderError
	if errors.As(err, &recordHeaderPtr) {
		return true
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return true
	}
	var alertPtr *tls.AlertError
	if errors.As(err, &alertPtr) {
		return true
	}
	var unknownAuthority x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthority) {
		return true
	}
	var unknownAuthorityPtr *x509.UnknownAuthorityError
	if errors.As(err, &unknownAuthorityPtr) {
		return true
	}
	var hostname x509.HostnameError
	if errors.As(err, &hostname) {
		return true
	}
	var hostnamePtr *x509.HostnameError
	if errors.As(err, &hostnamePtr) {
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		return true
	}
	var certInvalidPtr *x509.CertificateInvalidError
	return errors.As(err, &certInvalidPtr)
}
