// Package systemd provides systemd unit monitoring.
package systemd

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

// Backend is the interface for querying systemd unit state.
// Allows mocking for testing.
type Backend interface {
	IsActive(ctx context.Context, unit string, timeout time.Duration) (bool, string, error)
}

// Config is the systemd check configuration.
type Config struct {
	CheckID string        `toml:"check_id"`
	Target  string        `toml:"target"`
	Timeout time.Duration `toml:"timeout"`
}

// Check is the systemd check adapter.
type Check struct {
	logger  *logging.Logger
	backend Backend
}

// New creates a new systemd check with real backend.
func New(logger *logging.Logger) *Check {
	return &Check{
		logger:  logger,
		backend: &realBackend{},
	}
}

// NewWithBackend creates a systemd check with a custom backend (for testing).
func NewWithBackend(logger *logging.Logger, backend Backend) *Check {
	return &Check{
		logger:  logger,
		backend: backend,
	}
}

// Type returns the check type.
func (c *Check) Type() string {
	return "systemd"
}

// Run executes the systemd check.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()

	// Parse check-specific config from CheckConfig
	checkConfig, ok := cfg.(*scheduler.Spec)
	if !ok {
		return model.CheckResult{
			CheckID:    cfg.CheckID(),
			CheckType:  cfg.CheckType(),
			Success:    false,
			StartedAt:  startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:   time.Since(startedAt),
			ReasonCode: "invalid_config",
			Reason:     "check config is not spec",
		}
	}
	if checkConfig == nil {
		return model.CheckResult{
			CheckID:    cfg.CheckID(),
			CheckType:  cfg.CheckType(),
			Success:    false,
			StartedAt:  startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:   time.Since(startedAt),
			ReasonCode: "invalid_config",
			Reason:     "check config is nil",
		}
	}

	// Execute systemd check
	result, err := c.execute(ctx, Config{
		CheckID: checkConfig.ID,
		Target:  checkConfig.Target,
		Timeout: 10 * time.Second,
	})
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
			Reason:     fmt.Sprintf("systemd check failed: %v", sanitizeReason(err.Error())),
		}
	}

	return result
}

// execute runs the systemd check.
func (c *Check) execute(ctx context.Context, cfg Config) (model.CheckResult, error) {
	// Query systemd unit state
	active, status, err := c.backend.IsActive(ctx, cfg.Target, cfg.Timeout)
	if err != nil {
		return model.CheckResult{}, fmt.Errorf("query failed: %w", err)
	}

	// Success only if state is exactly "active"
	if !active {
		return model.CheckResult{
			CheckID:    cfg.CheckID,
			CheckType:  "systemd",
			Success:    false,
			StartedAt:  time.Now().UTC(),
			CompletedAt: time.Now().UTC(),
			Duration:   0,
			ReasonCode: reasonCodeForStatus(status),
			Reason:     fmt.Sprintf("unit %q is %q", cfg.Target, status),
		}, nil
	}

	return model.CheckResult{
		CheckID:    cfg.CheckID,
		CheckType:  "systemd",
		Success:    true,
		StartedAt:  time.Now().UTC(),
		CompletedAt: time.Now().UTC(),
		Duration:   0,
		ReasonCode: "success",
		Reason:     fmt.Sprintf("unit %q is active", cfg.Target),
	}, nil
}

// realBackend implements Backend using systemctl is-active.
type realBackend struct{}

// IsActive queries the active state of a systemd unit.
func (b *realBackend) IsActive(ctx context.Context, unit string, timeout time.Duration) (bool, string, error) {
	cmd := exec.CommandContext(ctx, "systemctl", "is-active", unit)

	output, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return false, "", fmt.Errorf("deadline exceeded")
		}
		return false, "", fmt.Errorf("query failed: %w", err)
	}

	status := strings.TrimSpace(string(output))
	if status == "active" {
		return true, status, nil
	}
	return false, status, nil
}

// reasonCodeForStatus returns a reason code based on the systemd status.
func reasonCodeForStatus(status string) string {
	switch status {
	case "active":
		return "success"
	case "inactive":
		return "inactive"
	case "failed":
		return "failed"
	case "unknown":
		return "unknown"
	default:
		return "query_error"
	}
}

// sanitizeReason sanitizes a reason string for logging/output.
func sanitizeReason(s string) string {
	// Limit length and remove sensitive patterns
	const maxLen = 256
	if len(s) > maxLen {
		s = s[:maxLen]
	}
	// Remove newlines and excessive whitespace
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.Join(strings.Fields(s), " ")
	return s
}
