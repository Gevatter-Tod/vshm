// Package http provides HTTP(S) GET health-check adapter.
package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

const (
	// maxBodySize is 1 MiB response body cap
	maxBodySize = 1024 * 1024
	// defaultExpectedStatus is the default expected HTTP status code
	defaultExpectedStatus = 200
	// defaultTimeout is the default request timeout
	defaultTimeout = 10 * time.Second
	// defaultInterval is the default check interval
	defaultInterval = 30 * time.Second
)

// Config is the HTTP check configuration.
type Config struct {
	CheckID          string        `toml:"check_id"`
	URL              string        `toml:"url"`
	Interval         time.Duration `toml:"interval"`
	Timeout          time.Duration `toml:"timeout"`
	SecretFile       string        `toml:"secret_file,omitempty"`
	ExpectedStatus   int           `toml:"expected_status,omitempty"`
	BodyContains     string        `toml:"body_contains,omitempty"`
	CredentialFile   string        `toml:"credential_file,omitempty"`
}

// HTTPCheck is the HTTP check adapter.
type Check struct {
	logger *logging.Logger
}

// New creates a new HTTP check.
func New(logger *logging.Logger) *Check {
	return &Check{logger: logger}
}

// Type returns the check type.
func (c *Check) Type() string {
	return "http"
}

// Run executes the HTTP check.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()

	// Parse check-specific config from CheckConfig
	checkConfig, ok := cfg.(*scheduler.Spec)
	if !ok {
		return model.CheckResult{
			CheckID:     cfg.CheckID(),
			CheckType:   cfg.CheckType(),
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:    time.Since(startedAt),
			ReasonCode:  "invalid_config",
			Reason:      "check config is not spec",
		}
	}
	if checkConfig == nil {
		return model.CheckResult{
			CheckID:     cfg.CheckID(),
			CheckType:   cfg.CheckType(),
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:    time.Since(startedAt),
			ReasonCode:  "invalid_config",
			Reason:      "check config is nil",
		}
	}

	// Build internal config from spec
	internalCfg := Config{
		CheckID:        checkConfig.ID,
		URL:            checkConfig.URL,
		ExpectedStatus: checkConfig.ExpectedStatus,
		Timeout:        checkConfig.Timeout,
		Interval:       30 * time.Second,
		CredentialFile: checkConfig.CredentialFile,
		BodyContains:   checkConfig.BodyContains,
	}

	// Execute HTTP check
	result, err := c.execute(ctx, internalCfg)
	if err != nil {
		return model.CheckResult{
			CheckID:     cfg.CheckID(),
			CheckType:   cfg.CheckType(),
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:    time.Since(startedAt),
			ReasonCode:  "http_failed",
			Reason:      fmt.Sprintf("HTTP check failed: %v", sanitizeReason(err.Error())),
		}
	}

	return result
}

// execute runs the HTTP check.
func (c *Check) execute(ctx context.Context, cfg Config) (model.CheckResult, error) {
	// Parse and normalize URL
	parsedURL, err := url.Parse(cfg.URL)
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("invalid URL: %w", err)
	}

	// Check scheme (http:// or https://)
	scheme := strings.ToLower(parsedURL.Scheme)
	if scheme != "http" && scheme != "https" {
		return model.CheckResult{}, fmt.Errorf("invalid scheme '%s' (http:// or https:// required)", scheme)
	}

	// Check credentials for HTTPS requirement
	var creds *credentials
	if cfg.CredentialFile != "" {
		if scheme == "http" {
			return model.CheckResult{}, fmt.Errorf("credential_file only permitted with HTTPS, got %s", scheme)
		}
		creds, err = loadCredentials(cfg.CredentialFile)
		if err != nil {
			return model.CheckResult{}, fmt.Errorf("failed to load credentials: %w", err)
		}
	}

	// Create HTTP client with redirect rejection
	client := &http.Client{
		Timeout: cfg.Timeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				InsecureSkipVerify: false,
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse // Reject redirects
		},
	}

	// Create request
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.URL, nil)
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("failed to create request: %w", err)
	}

	// Add basic auth if credentials provided
	if creds != nil {
		req.SetBasicAuth(creds.Username, creds.Password)
	}

	// Execute request
	resp, err := client.Do(req)
	if err != nil {
		// Check for TLS errors (self-signed or hostname mismatch)
		if isTLSError(err) {
			return model.CheckResult{
				CheckID:     cfg.CheckID,
				CheckType:   "http",
				Success:     true,
				StartedAt:   time.Now().UTC(),
				CompletedAt: time.Now().UTC(),
				Duration:    0,
				ReasonCode:  "tls_cert_issue",
				Reason:      "TLS certificate verification warning (self-signed or hostname mismatch)",
			}, nil
		}
		return model.CheckResult{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check status code
	expectedStatus := cfg.ExpectedStatus
	if expectedStatus == 0 {
		expectedStatus = defaultExpectedStatus
	}
	if resp.StatusCode != expectedStatus {
		return model.CheckResult{}, fmt.Errorf("unexpected status code: expected %d, got %d", expectedStatus, resp.StatusCode)
	}

	// Read response body with size cap (detect >cap by reading one extra byte)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("failed to read response body: %w", err)
	}

	// Check if body exceeds cap (read one extra byte to detect)
	if int64(len(body)) > maxBodySize {
		return model.CheckResult{}, fmt.Errorf("response body exceeds 1 MiB cap")
	}

	// Check body contains if specified
	if cfg.BodyContains != "" && !strings.Contains(string(body), cfg.BodyContains) {
		return model.CheckResult{}, fmt.Errorf("body does not contain expected substring '%s'", cfg.BodyContains)
	}

	return model.CheckResult{
		CheckID:     cfg.CheckID,
		CheckType:   "http",
		Success:     true,
		StartedAt:   time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:    0,
		ReasonCode:  "success",
		Reason:      fmt.Sprintf("HTTP %d OK", resp.StatusCode),
	}, nil
}

// credentials holds HTTP basic auth credentials.
type credentials struct {
	Username string
	Password string
}

// loadCredentials loads HTTP basic auth credentials from an INI file.
func loadCredentials(path string) (*credentials, error) {
	var data map[string]map[string]string
	if _, err := toml.DecodeFile(path, &data); err != nil {
		return nil, fmt.Errorf("failed to decode INI file: %w", err)
	}

	basic, ok := data["basic"]
	if !ok {
		return nil, fmt.Errorf("INI file missing [basic] section")
	}

	username, ok := basic["username"]
	if !ok {
		return nil, fmt.Errorf("INI file missing [basic] username")
	}

	password, ok := basic["password"]
	if !ok {
		return nil, fmt.Errorf("INI file missing [basic] password")
	}

	return &credentials{
		Username: username,
		Password: password,
	}, nil
}

// isTLSError checks if the error is a TLS certificate verification error.
func isTLSError(err error) bool {
	if err == nil {
		return false
	}
	errStr := err.Error()
	// Check for specific TLS/x509 errors
	if strings.Contains(errStr, "x509:") {
		return true
	}
	if strings.Contains(errStr, "certificate") && strings.Contains(errStr, "verify") {
		return true
	}
	if strings.Contains(errStr, "TLS") || strings.Contains(errStr, "tls") {
		return true
	}
	return false
}

// sanitizeReason sanitizes a reason string for logging/output.
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
