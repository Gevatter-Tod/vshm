// Package ping provides an unprivileged single-probe ping check adapter.
package ping

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

// executableRunner abstracts ping execution for testability.
type executableRunner interface {
	run(ctx context.Context, host string) (*exec.ExitError, error)
}

// realRunner executes the real ping binary.
type realRunner struct{}

func (realRunner) run(ctx context.Context, host string) (*exec.ExitError, error) {
	cmd := exec.CommandContext(ctx, "ping", "-c", "1", "-W", "1", host)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	_, err := cmd.CombinedOutput()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return exitErr, err
		}
		// Non-exit error (e.g. context deadline exceeded, ENOENT)
		return nil, err
	}
	return nil, nil
}

// exitError is a lightweight wrapper that satisfies *exec.ExitError behavior
// for testing without constructing os.ProcessState.
type exitError struct {
	code int
}

func (e *exitError) Error() string   { return fmt.Sprintf("exit code %d", e.code) }
func (e *exitError) ExitCode() int   { return e.code }
func (e *exitError) Exited() bool    { return true }

// fakeRunner is a test double that injects controlled behaviour.
type fakeRunner struct {
	mu             sync.Mutex
	exitError      bool
	exitCode       int
	nonExitError   error
	missing        bool
	deadlineExceed bool
}

func (f *fakeRunner) run(ctx context.Context, host string) (*exec.ExitError, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.missing {
		return nil, fmt.Errorf("exec: %w", os.ErrNotExist)
	}
	if f.nonExitError != nil {
		return nil, f.nonExitError
	}
	if f.deadlineExceed {
		return nil, context.DeadlineExceeded
	}
	if f.exitError {
		return &exec.ExitError{}, nil
	}
	return nil, nil
}

// Check is the ping check adapter.
type Check struct {
	logger *logging.Logger
	runner executableRunner
}

// New creates a new ping check with a real executable runner.
func New(logger *logging.Logger) *Check {
	return &Check{
		logger: logger,
		runner: realRunner{},
	}
}

// NewWithRunner creates a ping check with an injectable runner (for tests).
func NewWithRunner(logger *logging.Logger, runner executableRunner) *Check {
	return &Check{
		logger: logger,
		runner: runner,
	}
}

// Type returns the check type identifier.
func (c *Check) Type() string {
	return "ping"
}

// Run executes a single-probe unprivileged ping check.
func (c *Check) Run(ctx context.Context, cfg model.CheckConfig) model.CheckResult {
	startedAt := time.Now().UTC()

	if cfg == nil {
		return model.CheckResult{
			CheckID:     "",
			CheckType:   "",
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: time.Now().UTC(),
			Duration:    0,
			ReasonCode:  "invalid_config",
			Reason:      "check config is nil",
		}
	}

	pcfg, ok := cfg.(*scheduler.Spec)
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
	if pcfg == nil {
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

	// Set a slightly tighter deadline for the ping process than the overall
	// context deadline to ensure the process group is killed cleanly.
	deadline, hasDeadline := ctx.Deadline()
	var runCtx context.Context
	if hasDeadline {
		margin := time.Millisecond * 50
		if deadline.Sub(startedAt) <= margin {
			// Deadline already elapsed or very tight.
			return model.CheckResult{
				CheckID:     cfg.CheckID(),
				CheckType:   cfg.CheckType(),
				Success:     false,
				StartedAt:   startedAt,
				CompletedAt: time.Now().UTC(),
				Duration:    time.Since(startedAt),
				ReasonCode:  "timeout",
				Reason:      "context deadline reached",
			}
		}
		var cancel context.CancelFunc
		runCtx, cancel = context.WithDeadline(ctx, deadline.Add(-margin))
		defer cancel()
	} else {
		runCtx = ctx
	}

	exitErr, err := c.runner.run(runCtx, pcfg.PingHost())
	completedAt := time.Now().UTC()
	duration := completedAt.Sub(startedAt)

	// Check for exit error (ping returned non-zero) first, before other errors.
	if exitErr != nil {
		reason := fmt.Sprintf("ping returned exit code %d", exitErr.ExitCode())
		return model.CheckResult{
			CheckID:     cfg.CheckID(),
			CheckType:   cfg.CheckType(),
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			Duration:    duration,
			ReasonCode:  "ping_failed",
			Reason:      reason,
		}
	}

	if err != nil {
		// Classify the error for a precise reason code.
		reasonCode, reason := classifyError(err, startedAt, duration)
		return model.CheckResult{
			CheckID:     cfg.CheckID(),
			CheckType:   cfg.CheckType(),
			Success:     false,
			StartedAt:   startedAt,
			CompletedAt: completedAt,
			Duration:    duration,
			ReasonCode:  reasonCode,
			Reason:      reason,
		}
	}

	return model.CheckResult{
		CheckID:     cfg.CheckID(),
		CheckType:   "ping",
		Success:     true,
		StartedAt:   startedAt,
		CompletedAt: completedAt,
		Duration:    duration,
		ReasonCode:  "success",
		Reason:      fmt.Sprintf("ping %s OK", sanitizeReason(pcfg.PingHost())),
	}
}

// classifyError maps ping runner errors to structured reason codes.
func classifyError(err error, startedAt time.Time, duration time.Duration) (string, string) {
	// Check for ENOENT — ping binary not found.
	if errors.Is(err, os.ErrNotExist) {
		reason := fmt.Sprintf("ping executable not found")
		return "executable_not_found", reason
	}

	// Check for context deadline exceeded (timeout).
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", fmt.Sprintf("ping timed out after %v", duration)
	}

	// Check for an exit error (non-zero exit code from ping).
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		code := exitErr.ExitCode()
		reason := fmt.Sprintf("ping returned exit code %d", code)
		return "ping_failed", reason
	}

	// Generic error — includes permission errors on unprivileged ping socket.
	reason := fmt.Sprintf("ping check failed: %v", sanitizeReason(err.Error()))
	return "ping_failed", reason
}

// sanitizeReason truncates and cleans a reason string for safe logging.
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
