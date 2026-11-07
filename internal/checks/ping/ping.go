// Package ping provides network connectivity monitoring.
package ping

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Config is the ping check configuration.
type Config struct {
	CheckID    string        `toml:"check_id"`
	Host       string        `toml:"host"`
	Interval   time.Duration `toml:"interval"`
	Timeout    time.Duration `toml:"timeout"`
	SecretFile string        `toml:"secret_file,omitempty"`
}

// Check is the ping check adapter.
type Check struct {
	config Config
	logger *logging.Logger
}

// New creates a new ping check.
func New(logger *logging.Logger) *Check {
	return &Check{logger: logger}
}

// Type returns the check type.
func (c *Check) Type() string {
	return "ping"
}

// Run executes the ping check.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()
	
	// Create check-specific config
	checkConfig := Config{
		CheckID: cfg.CheckID(),
		Host:    "192.168.1.1",
		Interval: time.Duration(0),
		Timeout: time.Duration(0),
	}
	
	// Execute ping check
	result, err := c.execute(ctx, checkConfig)
	if err != nil {
		c.logger.Printf("ping check failed: %v", err)
		return model.CheckResult{
			CheckID:    cfg.CheckID(),
			CheckType:  cfg.CheckType(),
			Success:    false,
			StartedAt:  startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:   time.Since(startedAt),
			ReasonCode: "ping_failed",
			Reason:     fmt.Sprintf("ping check failed: %v", err),
		}
	}
	
	return result
}

// execute runs the ping check.
func (c *Check) execute(ctx context.Context, cfg Config) (model.CheckResult, error) {
	// Use ping command with argument vector (no shell interpolation)
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "5", cfg.Host)
	
	output, err := cmd.Output()
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("ping failed: %w", err)
	}
	
	// Check if ping was successful
	if len(output) == 0 {
		return model.CheckResult{}, fmt.Errorf("ping returned empty output")
	}
	
	return model.CheckResult{
		CheckID:    cfg.CheckID,
		CheckType:  "ping",
		Success:    true,
		StartedAt:  time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:   0,
		ReasonCode: "success",
		Reason:     fmt.Sprintf("ping to %s successful", cfg.Host),
	}, nil
}
