// Package checks provides base check implementations.
package checks

import (
	"context"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// BaseCheck is the base implementation for all check adapters.
type BaseCheck struct {
	logger *logging.Logger
}

// NewBaseCheck creates a new base check.
func NewBaseCheck(logger *logging.Logger) *BaseCheck {
	return &BaseCheck{logger: logger}
}

// Run executes a check.
func (c *BaseCheck) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()
	
	// Implement check-specific logic here
	// This is a placeholder
	result := model.CheckResult{
		CheckID:    cfg.CheckID(),
		CheckType:  cfg.CheckType(),
		Success:    true,
		StartedAt:  startedAt,
		CompletedAt: startedAt,
		Duration:   0,
		ReasonCode: "success",
		Reason:     "check passed",
	}
	
	return result
}

// ValidateConfig validates a check configuration.
func ValidateConfig(cfg *config.Config) error {
	if len(cfg.Checks) == 0 {
		return nil
	}
	
	for _, check := range cfg.Checks {
		if check.CheckID == "" {
			return nil // CheckID is optional for now
		}
	}
	
	return nil
}
