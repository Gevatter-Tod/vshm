// Package systemd provides systemd unit monitoring.
package systemd

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Config is the systemd check configuration.
type Config struct {
	CheckID    string        `toml:"check_id"`
	Target     string        `toml:"target"`
	Interval   time.Duration `toml:"interval"`
	Timeout    time.Duration `toml:"timeout"`
	SecretFile string        `toml:"secret_file,omitempty"`
}

// Check is the systemd check adapter.
type Check struct {
	config Config
	logger *logging.Logger
}

// New creates a new systemd check.
func New(logger *logging.Logger) *Check {
	return &Check{logger: logger}
}

// Type returns the check type.
func (c *Check) Type() string {
	return "systemd"
}

// Run executes the systemd check.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()
	
	// Create check-specific config
	checkConfig := Config{
		CheckID: cfg.CheckID(),
		Target:  "multi-user.target",
		Interval: time.Duration(0),
		Timeout: time.Duration(0),
	}
	
	// Execute systemd check
	result, err := c.execute(ctx, checkConfig)
	if err != nil {
		c.logger.Printf("systemd check failed: %v", err)
		return model.CheckResult{
			CheckID:    cfg.CheckID(),
			CheckType:  cfg.CheckType(),
			Success:    false,
			StartedAt:  startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:   time.Since(startedAt),
			ReasonCode: "systemd_failed",
			Reason:     fmt.Sprintf("systemd check failed: %v", err),
		}
	}
	
	return result
}

// execute runs the systemd check.
func (c *Check) execute(ctx context.Context, cfg Config) (model.CheckResult, error) {
	// Execute systemd-is-active
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", cfg.Target)
	
	output, err := cmd.Output()
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("systemd check failed: %w", err)
	}
	
	// Check if target is active
	if string(output) != "active\n" {
		return model.CheckResult{}, fmt.Errorf("target %s is not active", cfg.Target)
	}
	
	return model.CheckResult{
		CheckID:    cfg.CheckID,
		CheckType:  "systemd",
		Success:    true,
		StartedAt:  time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:   0,
		ReasonCode: "success",
		Reason:     "systemd target is active",
	}, nil
}
