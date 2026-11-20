// Package webdav provides the read-only bounded-depth WebDAV PROPFIND adapter (WP-08).
//
// Security properties (docs/wp08-check-webdav.md §5):
//   - Only the read-only PROPFIND method is ever sent (never PUT/DELETE/MOVE/
//     MKCOL/COPY); the Depth header is always exactly "0" or "1", never
//     "infinity".
//   - Success requires HTTP 207 and a well-formed XML body whose root is a DAV
//     multistatus with at least one response containing an href, where every
//     response has at least one propstat with a 2xx status.
//   - TLS verification is mandatory (InsecureSkipVerify false, TLS >= 1.2).
//     Redirect policy for WebDAV is "follow" (user decision on the WP-08 §7 open
//     question, recorded in docs/wp08-completion.md); Go's client strips the
//     Authorization header on cross-host redirects.
//   - Optional credentials come from an INI [basic] file and are only sent over
//     verified HTTPS; Basic credentials are never sent over plain HTTP.
//   - The response body is capped at 1 MiB (user-confirmed §7 answer, same cap
//     as the HTTP check) and response processing is bounded.
//   - Reasons are short and sanitized: they never contain the response body,
//     credentials, secret-bearing paths, or the request URL (which could embed
//     userinfo). Network/XML processing all run under the single absolute
//     deadline carried by ctx plus the configured client timeout.
package webdav

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

// maxBodySize is the 1 MiB response-body cap (user-confirmed WP-08 §7 answer:
// same safe cap as the HTTP check).
const maxBodySize = 1024 * 1024

// defaultTimeout applies when the config carries no usable timeout.
const defaultTimeout = 10 * time.Second

// propfindAllProp is the small, constant, read-only PROPFIND request body
// (RFC 4918 allprop). Sending it is optional per the RFC but keeps the request
// unambiguous across servers.
const propfindAllProp = `<?xml version="1.0" encoding="utf-8"?><D:propfind xmlns:D="DAV:"><D:allprop/></D:propfind>`

// Reason codes returned in CheckResult.ReasonCode. They are stable, short and
// never contain secrets, body content, URLs or secret-bearing paths.
const (
	reasonSuccess             = "success"
	reasonInvalidConfig       = "invalid_config"
	reasonInvalidURL          = "invalid_url"
	reasonCredentialNotHTTPS  = "credential_not_https"
	reasonCredentialError     = "credential_error"
	reasonTimeout             = "timeout"
	reasonRequestFailed       = "request_failed"
	reasonBadStatus           = "bad_status"
	reasonBodyTooLarge        = "body_too_large"
	reasonMalformedXML        = "malformed_xml"
	reasonWrongRoot           = "wrong_root"
	reasonNoResponse          = "no_response"
	reasonNoHref              = "no_href"
	reasonNoSuccessfulPropsta = "no_successful_propstat"
	reasonTooManyResponses    = "too_many_responses"
)

// Check is the WebDAV check adapter.
type Check struct {
	logger *logging.Logger
	// transport is nil in production (verified-TLS default transport is
	// built per run); tests inject a RoundTripper (e.g. a TLS test server's)
	// while the client wrapper still enforces the timeout.
	transport http.RoundTripper
}

// New creates a new WebDAV check.
func New(logger *logging.Logger) *Check {
	return &Check{logger: logger}
}

// NewWithTransport creates a WebDAV check with a custom RoundTripper (tests
// only; e.g. httptest.NewTLSServer's client transport).
func NewWithTransport(logger *logging.Logger, rt http.RoundTripper) *Check {
	return &Check{logger: logger, transport: rt}
}

// Type returns the check type.
func (c *Check) Type() string { return "webdav" }

// Run executes the WebDAV check exactly once and returns one normalized
// observation with UTC timestamps and a sanitized reason.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()

	var wc *Config
	switch cc := cfg.(type) {
	case *Config:
		wc = cc
	case *scheduler.Spec:
		var err error
		wc, err = fromSpec(cc)
		if err != nil {
			return c.result(cfg, startedAt, false, reasonInvalidConfig, "webdav check config missing")
		}
	default:
		return c.result(cfg, startedAt, false, reasonInvalidConfig, "unsupported check config type")
	}
	if wc == nil {
		return c.result(cfg, startedAt, false, reasonInvalidConfig, "check config is nil")
	}

	return c.execute(ctx, wc, cfg, startedAt)
}

// execute sends one read-only PROPFIND with Depth 0/1 under the ctx deadline
// and validates the 207 multistatus response.
func (c *Check) execute(ctx context.Context, wc *Config, cfg model.CheckConfig, startedAt time.Time) model.CheckResult {
	fail := func(code, msg string) model.CheckResult {
		c.logger.Printf("webdav check %q failed: %s", cfg.CheckID(), code)
		return c.result(cfg, startedAt, false, code, msg)
	}

	// 1. Validate the typed config. Depth must be exactly 0 or 1.
	if wc.depth != 0 && wc.depth != 1 {
		return fail(reasonInvalidConfig, "depth must be 0 or 1")
	}
	parsed, err := url.Parse(wc.url)
	if err != nil || parsed.Host == "" {
		return fail(reasonInvalidURL, "invalid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return fail(reasonInvalidURL, "URL scheme must be http or https")
	}

	// 2. Credentials: INI [basic] file, only over verified HTTPS. Basic
	//    credentials are never sent over plain HTTP.
	var creds *basicCredentials
	if wc.credentialFile != "" {
		if scheme != "https" {
			return fail(reasonCredentialNotHTTPS, "credential_file is only permitted with HTTPS")
		}
		creds, err = loadBasicCredentials(wc.credentialFile)
		if err != nil {
			return fail(reasonCredentialError, "failed to load credentials")
		}
	}

	// 3. HTTP client: mandatory TLS verification on the default transport,
	//    one timeout covering the whole request. Redirects are followed
	//    (user-confirmed WP-08 §7 answer; Go strips Authorization on
	//    cross-host redirects).
	timeout := wc.timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	transport := c.transport
	if transport == nil {
		transport = &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false, // verified TLS is mandatory
				MinVersion:         tls.VersionTLS12,
			},
		}
	}
	client := &http.Client{Timeout: timeout, Transport: transport}

	// 4. Build the read-only PROPFIND request. The method is hardcoded; the
	//    target is never modified. Depth is always exactly 0 or 1.
	req, err := http.NewRequestWithContext(ctx, "PROPFIND", wc.url, strings.NewReader(propfindAllProp))
	if err != nil {
		return fail(reasonInvalidURL, "failed to build request")
	}
	req.Header.Set("Depth", strconv.Itoa(wc.depth))
	req.Header.Set("Content-Type", "application/xml; charset=utf-8")
	if creds != nil {
		req.SetBasicAuth(creds.username, creds.password)
	}

	resp, err := client.Do(req)
	if err != nil {
		if isTimeoutErr(ctx, err) {
			return fail(reasonTimeout, "PROPFIND timed out")
		}
		// Error text may embed the URL (possibly with userinfo): never surface it.
		return fail(reasonRequestFailed, "PROPFIND request failed")
	}
	defer resp.Body.Close()

	// 5. HTTP status must be exactly 207 Multi-Status (separate from XML
	//    structure errors). The status code is the only response detail that
	//    may appear in a reason.
	if resp.StatusCode != http.StatusMultiStatus {
		return fail(reasonBadStatus, fmt.Sprintf("unexpected HTTP status %d, expected 207", resp.StatusCode))
	}

	// 6. Read the body with the 1 MiB cap (read one extra byte to detect an
	//    over-cap body without buffering it).
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return fail(reasonRequestFailed, "failed to read response body")
	}
	if len(raw) > maxBodySize {
		return fail(reasonBodyTooLarge, "response body exceeds 1 MiB cap")
	}

	// 7. Validate the multistatus structure (bounded: body already capped).
	n, verr := validateMultistatus(raw)
	if verr != nil {
		switch {
		case errors.Is(verr, errWrongRoot):
			return fail(reasonWrongRoot, "response root is not a DAV multistatus")
		case errors.Is(verr, errNoResponse):
			return fail(reasonNoResponse, "multistatus contains no response")
		case errors.Is(verr, errNoHref):
			return fail(reasonNoHref, "no response contains an href")
		case errors.Is(verr, errNoSuccessfulPropstat):
			return fail(reasonNoSuccessfulPropsta, "a response has no propstat with a 2xx status")
		case errors.Is(verr, errTooManyResponses):
			return fail(reasonTooManyResponses, "too many responses in multistatus")
		default:
			return fail(reasonMalformedXML, "response is not well-formed XML")
		}
	}

	return c.result(cfg, startedAt, true, reasonSuccess,
		fmt.Sprintf("PROPFIND depth=%d returned 207 multistatus with %d response(s)", wc.depth, n))
}

// result builds the normalized CheckResult with UTC timestamps.
func (c *Check) result(cfg model.CheckConfig, startedAt time.Time, success bool, code, reason string) model.CheckResult {
	completed := time.Now().UTC()
	return model.CheckResult{
		CheckID:     cfg.CheckID(),
		CheckType:   cfg.CheckType(),
		Success:     success,
		StartedAt:   startedAt,
		CompletedAt: completed,
		Duration:    completed.Sub(startedAt),
		ReasonCode:  code,
		Reason:      sanitizeReason(reason),
	}
}

// isTimeoutErr reports whether the request failed due to a deadline (ctx or
// client timeout).
func isTimeoutErr(ctx context.Context, err error) bool {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrDeadlineExceeded) {
		return true
	}
	if ctx.Err() != nil {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return true
	}
	return false
}

// sanitizeReason keeps reasons short and single-line: no newlines, collapsed
// whitespace, capped length. Reasons built by this package never contain
// secrets, body content, URLs or secret paths, so sanitizing is a second layer.
func sanitizeReason(s string) string {
	const maxLen = 256
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}
