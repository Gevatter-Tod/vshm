// Package http provides HTTP endpoint monitoring.
package http

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Config is the HTTP check configuration.
type Config struct {
	CheckID    string        `toml:"check_id"`
	URL        string        `toml:"url"`
	Interval   time.Duration `toml:"interval"`
	Timeout    time.Duration `toml:"timeout"`
	SecretFile string        `toml:"secret_file,omitempty"`
}

// Check is the HTTP check adapter.
type Check struct {
	config Config
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
	
	// Create check-specific config
	checkConfig := Config{
		CheckID: cfg.CheckID(),
		URL:     "https://example.com/health",
		Interval: time.Duration(0),
		Timeout: time.Duration(0),
	}
	
	// Execute HTTP check
	result, err := c.execute(ctx, checkConfig)
	if err != nil {
		c.logger.Printf("HTTP check failed: %v", err)
		return model.CheckResult{
			CheckID:    cfg.CheckID(),
			CheckType:  cfg.CheckType(),
			Success:    false,
			StartedAt:  startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:   time.Since(startedAt),
			ReasonCode: "http_failed",
			Reason:     fmt.Sprintf("HTTP check failed: %v", err),
		}
	}
	
	return result
}

// execute runs the HTTP check.
func (c *Check) execute(ctx context.Context, cfg Config) (model.CheckResult, error) {
	client := &http.Client{
		Timeout: cfg.Timeout,
	}
	
	req, err := http.NewRequestWithContext(ctx, "GET", cfg.URL, nil)
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("failed to create request: %w", err)
	}
	
	resp, err := client.Do(req)
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("HTTP request failed: %w", err)
	}
	defer resp.Body.Close()
	
	// Check status code
	if resp.StatusCode != http.StatusOK {
		return model.CheckResult{}, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}
	
	return model.CheckResult{
		CheckID:    cfg.CheckID,
		CheckType:  "http",
		Success:    true,
		StartedAt:  time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:   0,
		ReasonCode: "success",
		Reason:     "HTTP endpoint returned 200 OK",
	}, nil
}
