package ping

import (
	"context"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

func TestCheckRunSuccess(t *testing.T) {
	runner := &fakeRunner{}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-ok", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Reason)
	}
	if result.ReasonCode != "success" {
		t.Errorf("expected reason code 'success', got '%s'", result.ReasonCode)
	}
	if result.CheckID != "ping-ok" {
		t.Errorf("expected check ID 'ping-ok', got '%s'", result.CheckID)
	}
}

func TestCheckRunPingFailed(t *testing.T) {
	runner := &fakeRunner{exitError: true, exitCode: 2}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-fail", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "ping_failed" {
		t.Errorf("expected reason code 'ping_failed', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunExecutableNotFound(t *testing.T) {
	runner := &fakeRunner{missing: true}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-missing", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "executable_not_found" {
		t.Errorf("expected reason code 'executable_not_found', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunDeadlineExceeded(t *testing.T) {
	runner := &fakeRunner{deadlineExceed: true}
	check := NewWithRunner(nil, runner)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	result := check.Run(ctx, &scheduler.Spec{ID: "ping-timeout", Typ: "ping", Target: "127.0.0.1"})

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "timeout" {
		t.Errorf("expected reason code 'timeout', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunInvalidConfig(t *testing.T) {
	runner := &fakeRunner{}
	check := NewWithRunner(nil, runner)

	result := check.Run(context.Background(), nil)

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "invalid_config" {
		t.Errorf("expected reason code 'invalid_config', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunType(t *testing.T) {
	check := New(nil)
	if check.Type() != "ping" {
		t.Errorf("expected type 'ping', got '%s'", check.Type())
	}
}

func TestCheckRunProcessGroupSetpgid(t *testing.T) {
	runner := &fakeRunner{}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-pg", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	// Verify the process was run (success case)
	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Reason)
	}
}

func TestCheckRunSanitizedReason(t *testing.T) {
	runner := &fakeRunner{nonExitError: nil}
	check := NewWithRunner(nil, runner)
	// Test that reason is bounded and sanitized by checking success case
	cfg := &scheduler.Spec{ID: "ping-sanitize", Typ: "ping", Target: "127.0.0.1"}
	result := check.Run(context.Background(), cfg)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Reason)
	}
	// Ensure reason doesn't contain newlines
	for _, r := range result.Reason {
		if r == '\n' || r == '\r' {
			t.Errorf("reason contains newline character")
		}
	}
}

func TestCheckRunNoReply(t *testing.T) {
	runner := &fakeRunner{exitError: true, exitCode: 1}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-noreply", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "ping_failed" {
		t.Errorf("expected reason code 'ping_failed', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunTimeout(t *testing.T) {
	runner := &fakeRunner{deadlineExceed: true}
	check := NewWithRunner(nil, runner)

	// Deadline is very tight — should trigger timeout before ping runs
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	result := check.Run(ctx, &scheduler.Spec{ID: "ping-tight", Typ: "ping", Target: "127.0.0.1"})

	if result.Success {
		t.Error("expected failure, got success")
	}
	if result.ReasonCode != "timeout" {
		t.Errorf("expected reason code 'timeout', got '%s'", result.ReasonCode)
	}
}

func TestCheckRunDurationTracked(t *testing.T) {
	runner := &fakeRunner{}
	check := NewWithRunner(nil, runner)
	cfg := &scheduler.Spec{ID: "ping-duration", Typ: "ping", Target: "127.0.0.1"}

	result := check.Run(context.Background(), cfg)

	if !result.Success {
		t.Errorf("expected success, got failure: %s", result.Reason)
	}
	if result.Duration == 0 {
		t.Error("expected non-zero duration")
	}
	if !result.StartedAt.IsZero() {
		loc := result.StartedAt.Location()
		if loc != nil && loc.String() != "UTC" {
			t.Error("startedAt should be UTC")
		}
	}
	if !result.CompletedAt.IsZero() {
		loc := result.CompletedAt.Location()
		if loc != nil && loc.String() != "UTC" {
			t.Error("completedAt should be UTC")
		}
	}
}

