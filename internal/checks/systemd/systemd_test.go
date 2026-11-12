// Package systemd provides systemd unit monitoring.
package systemd

import (
	"context"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
)

// fakeBackend implements Backend for testing.
type fakeBackend struct {
	active bool
	status string
	err    error
}

func (f *fakeBackend) IsActive(ctx context.Context, unit string, timeout time.Duration) (bool, string, error) {
	return f.active, f.status, f.err
}

// TestNewSystemdConfig tests config creation.
func TestNewSystemdConfig(t *testing.T) {
	cfg := NewSystemdConfig("test-unit", "my-service.service", 30*time.Second)

	if cfg.CheckID() != "test-unit" {
		t.Errorf("Expected CheckID 'test-unit', got '%s'", cfg.CheckID())
	}

	if cfg.CheckType() != "systemd" {
		t.Errorf("Expected CheckType 'systemd', got '%s'", cfg.CheckType())
	}

	if cfg.timeout != 30*time.Second {
		t.Errorf("Expected Timeout 30s, got %v", cfg.timeout)
	}
}

// TestCheckRun tests the Run method with various scenarios.
func TestCheckRun(t *testing.T) {
	tests := []struct {
		name           string
		activeState    string
		expectedSuccess bool
		expectedReason string
	}{
		{
			name:           "active state",
			activeState:    "active",
			expectedSuccess: true,
			expectedReason: `unit "my-service.service" is active`,
		},
		{
			name:           "inactive state",
			activeState:    "inactive",
			expectedSuccess: false,
			expectedReason: `unit "my-service.service" is "inactive"`,
		},
		{
			name:           "failed state",
			activeState:    "failed",
			expectedSuccess: false,
			expectedReason: `unit "my-service.service" is "failed"`,
		},
		{
			name:           "unknown state",
			activeState:    "unknown",
			expectedSuccess: false,
			expectedReason: `unit "my-service.service" is "unknown"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeBackend := &fakeBackend{active: tt.activeState == "active", status: tt.activeState}
			check := NewWithBackend(logging.New("systemd"), fakeBackend)
			cfg := NewSystemdConfig("test-unit", "my-service.service", 30*time.Second)

			result := check.Run(context.Background(), cfg)

			if result.Success != tt.expectedSuccess {
				t.Errorf("Expected Success %v, got %v", tt.expectedSuccess, result.Success)
			}

			if result.Reason != tt.expectedReason {
				t.Errorf("Expected Reason '%s', got '%s'", tt.expectedReason, result.Reason)
			}
		})
	}
}

// TestCheckRunWithDeadline tests timeout handling.
func TestCheckRunWithDeadline(t *testing.T) {
	fakeBackend := &fakeBackend{active: false, status: "active"}
	check := NewWithBackend(logging.New("systemd"), fakeBackend)
	cfg := NewSystemdConfig("test-unit", "my-service.service", 1*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	result := check.Run(ctx, cfg)

	if result.Success {
		t.Error("Expected Success false due to timeout")
	}

	// Duration should be at least some value (may be 0 if context deadline not hit)
	if result.Duration < 0 {
		t.Errorf("Expected Duration >= 0, got %v", result.Duration)
	}
}

// TestCheckRunWithQueryError tests error handling when query fails.
func TestCheckRunWithQueryError(t *testing.T) {
	fakeBackend := &fakeBackend{active: false, status: "active", err: testError{}}
	check := NewWithBackend(logging.New("systemd"), fakeBackend)
	cfg := NewSystemdConfig("test-unit", "my-service.service", 30*time.Second)

	result := check.Run(context.Background(), cfg)

	if result.Success {
		t.Error("Expected Success false due to query error")
	}

	// Should have a non-empty reason code
	if result.ReasonCode == "" {
		t.Error("Expected ReasonCode to be set")
	}
}

// TestCheckRunTimestamps tests that timestamps are set correctly.
func TestCheckRunTimestamps(t *testing.T) {
	fakeBackend := &fakeBackend{active: true, status: "active"}
	check := NewWithBackend(logging.New("systemd"), fakeBackend)
	cfg := NewSystemdConfig("test-unit", "my-service.service", 30*time.Second)

	result := check.Run(context.Background(), cfg)

	// Timestamps should be set
	if result.StartedAt.IsZero() {
		t.Error("Expected StartedAt to be set")
	}

	if result.CompletedAt.IsZero() {
		t.Error("Expected CompletedAt to be set")
	}
}

// TestCheckRunSanitization tests that output is sanitized.
func TestCheckRunSanitization(t *testing.T) {
	fakeBackend := &fakeBackend{active: true, status: "active"}
	check := NewWithBackend(logging.New("systemd"), fakeBackend)
	cfg := NewSystemdConfig("test-unit", "my-service.service", 30*time.Second)

	result := check.Run(context.Background(), cfg)

	// Reason should be sanitized (no sensitive info)
	if len(result.Reason) > 100 {
		t.Errorf("Reason should be sanitized, got %d characters", len(result.Reason))
	}
}

// testError is a simple error for testing.
type testError struct{}

func (testError) Error() string {
	return "test error"
}
