package matrixtransport

import (
	"context"
	"errors"
	"testing"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

func TestConfigWithDefaults(t *testing.T) {
	cfg := Config{}
	got := cfg.WithDefaults()

	if got.CLIPath != DefaultCLIPath {
		t.Errorf("CLIPath = %s, want %s", got.CLIPath, DefaultCLIPath)
	}
	if got.SendTimeout != DefaultSendTimeout {
		t.Errorf("SendTimeout = %v, want %v", got.SendTimeout, DefaultSendTimeout)
	}
	if got.ValidateTimeout != DefaultValidateTimeout {
		t.Errorf("ValidateTimeout = %v, want %v", got.ValidateTimeout, DefaultValidateTimeout)
	}
	if got.MinVersion != DefaultMinVersion {
		t.Errorf("MinVersion = %s, want %s", got.MinVersion, DefaultMinVersion)
	}

	if got.Logger == nil {
		t.Error("Logger should not be nil")
	}
}

func TestNewTransport(t *testing.T) {
	logger := logging.New("")
	
	// Test with defaults
	transport, err := New(Config{
		Logger: logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}
	if transport == nil {
		t.Fatal("New returned nil")
	}
	if transport.cliPath != DefaultCLIPath {
		t.Errorf("cliPath = %s, want %s", transport.cliPath, DefaultCLIPath)
	}
}

func TestNewTransportWithCLIPath(t *testing.T) {
	logger := logging.New("")
	
	cfg := Config{
		CLIPath: "custom-path",
		Logger:  logger,
	}
	transport, err := New(cfg)
	if err != nil {
		t.Fatalf("NewWithCLIPath failed: %v", err)
	}
	if transport == nil {
		t.Fatal("NewWithCLIPath returned nil")
	}
	// Note: roomID is set via SetRoomID or in the config, but NewWithCLIPath doesn't set it
	// So we just check cliPath
	if transport.cliPath != "custom-path" {
		t.Errorf("cliPath = %s, want custom-path", transport.cliPath)
	}
}

func TestParseVersion(t *testing.T) {
	tests := []struct {
		input    string
		want     string
	}{
		{"matrix-commander-rs v1.2.3", "1.2.3"},
		{"v1.2.3", "1.2.3"},
		{"1.2.3", "1.2.3"},
		{"version: 1.2.3", "1.2.3"},
		{"Version: 1.2.3", "1.2.3"},
		{"matrix-commander-rs version 1.2.3", "1.2.3"},
		{"something else", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := parseVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestCheckVersion(t *testing.T) {
	tests := []struct {
		version    string
		minVersion string
		wantErr    bool
	}{
		{"1.2.3", "1.0.0", false},
		{"1.2.3", "1.2.3", false},
		{"1.2.3", "1.2.4", true},
		{"1.2.3", "2.0.0", true},
		{"1.2.3", "0.9.0", false},
		{"1.2.3", "1.2.2", false},
		{"1.2.3", "1.2", false},
		{"1.2", "1.2.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.version+"_vs_"+tt.minVersion, func(t *testing.T) {
			err := checkVersion(tt.version, tt.minVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkVersion(%q, %q) error = %v, wantErr %v", tt.version, tt.minVersion, err, tt.wantErr)
			}
		})
	}
}

func TestEnsureCryptoStore(t *testing.T) {
	// matrix-commander-rs manages its own store
	// We just check if ensureCryptoStore doesn't fail with empty path
	if err := ensureCryptoStore(""); err != nil {
		t.Fatalf("ensureCryptoStore failed with empty path: %v", err)
	}
}

func TestFormatMessage(t *testing.T) {
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	event := model.OutboxEvent{
		ID:      "test-1",
		Payload: "test payload",
	}

	got := transport.formatMessage(event)
	if got != "test payload" {
		t.Errorf("formatMessage = %q, want %q", got, "test payload")
	}
}

func TestGetVersion(t *testing.T) {
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Initially, version should be empty
	if transport.GetVersion() != "" {
		t.Errorf("GetVersion() = %q, want empty", transport.GetVersion())
	}

	// Set version
	transport.version = "1.2.3"
	if transport.GetVersion() != "1.2.3" {
		t.Errorf("GetVersion() = %q, want 1.2.3", transport.GetVersion())
	}
}

func TestIsCompatible(t *testing.T) {
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// Initially, should not be compatible (not checked yet)
	if transport.IsCompatible() {
		t.Error("IsCompatible() should be false before validation")
	}

	// Set compatible
	transport.compatible = true
	if !transport.IsCompatible() {
		t.Error("IsCompatible() should be true after setting compatible")
	}
}

func TestSetRoomID(t *testing.T) {
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	if transport.GetRoomID() != "" {
		t.Errorf("GetRoomID() = %q, want empty", transport.GetRoomID())
	}

	transport.SetRoomID("!newroom:matrix.org")
	if transport.GetRoomID() != "!newroom:matrix.org" {
		t.Errorf("GetRoomID() = %q, want !newroom:matrix.org", transport.GetRoomID())
	}
}

func TestValidateMatrixCLI(t *testing.T) {
	// This test will fail if matrix-commander-rs is not installed
	// We'll test with a non-existent CLI path to verify error handling
	
	logger := logging.New("")
	ctx := context.Background()

	// Test with non-existent CLI
	_, err := ValidateMatrixCLI(ctx, "nonexistent-cli", "", "", "", logger)
	if err == nil {
		t.Error("ValidateMatrixCLI should fail with non-existent CLI")
	}

	// Test with empty room ID (should fail room check if CLI exists)
	// We can't test this without matrix-commander-rs installed
}

func TestValidationResult(t *testing.T) {
	// Test that ValidationResult has all expected fields
	result := ValidationResult{
		Compatible:    true,
		CLIFound:      true,
		Version:       "1.2.3",
		VersionOK:     true,
		CryptoStoreOK: true,
		EncryptionOK:  true,
		RoomOK:        true,
		RoomID:        "!room:matrix.org",
		Reason:        "all checks passed",
	}

	if !result.Compatible {
		t.Error("Compatible should be true")
	}
	if result.Version != "1.2.3" {
		t.Errorf("Version = %q, want 1.2.3", result.Version)
	}
	if result.RoomID != "!room:matrix.org" {
		t.Errorf("RoomID = %q, want !room:matrix.org", result.RoomID)
	}
}

// Mock for testing Send without actual matrix-commander-rs
func TestSendWithMock(t *testing.T) {
	// This test would require mocking exec.CommandContext
	// For now, we'll just verify the method signature
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// We can't actually test Send without matrix-commander-rs
	// But we can verify it doesn't panic with nil context
	// (though it will fail with context.DeadlineExceeded)
	
	// Just verify the method exists and has the right signature
	var _ func(context.Context, model.OutboxEvent) (model.DeliveryOutcome, error) = transport.Send
}

func TestTestSend(t *testing.T) {
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New failed: %v", err)
	}

	// We can't actually test TestSend without matrix-commander-rs
	// But we can verify it doesn't panic
	var _ func(context.Context, string) (model.DeliveryOutcome, error) = transport.TestSend
}

// Ensure we implement the model.Transport interface
var _ model.Transport = (*Transport)(nil)

// Test that our fake implementations work for testing
func TestFakeImplementations(t *testing.T) {
	// This is just to ensure our test helpers compile
	logger := logging.New("")
	
	// Use a temp directory for crypto store
	
	// Create a transport with a non-existent CLI path
	// This should not fail during creation, only during validation
	transport, err := New(Config{
		CLIPath:         "nonexistent",
		
		Logger:          logger,
	})
	if err != nil {
		t.Fatalf("New should not fail with non-existent CLI path: %v", err)
	}
	
	// Verify it was created
	if transport == nil {
		t.Fatal("transport should not be nil")
	}
	
	// Validation should fail
	_, err = transport.Validate(context.Background())
	if err == nil {
		t.Error("Validate should fail with non-existent CLI")
	}
}

// Test version parsing edge cases
func TestParseVersionEdgeCases(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"empty", "", ""},
		{"no version", "no version info here", ""},
		{"version in middle", "some text v1.2.3 more text", "1.2.3"},
		{"multiple versions", "v1.0.0 and v2.0.0", "1.0.0"}, // Should get first
		{"version with suffix", "v1.2.3-beta", "1.2.3"},
		{"version with prefix", "version: v1.2.3", "1.2.3"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parseVersion(tt.input)
			if got != tt.want {
				t.Errorf("parseVersion(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// Test checkVersion edge cases
func TestCheckVersionEdgeCases(t *testing.T) {
	tests := []struct {
		name       string
		version    string
		minVersion string
		wantErr    bool
	}{
		{"equal", "1.0.0", "1.0.0", false},
		{"major less", "0.9.0", "1.0.0", true},
		{"major greater", "2.0.0", "1.0.0", false},
		{"minor less", "1.9.0", "1.10.0", true},
		{"minor greater", "1.11.0", "1.10.0", false},
		{"patch less", "1.10.9", "1.10.10", true},
		{"patch greater", "1.10.11", "1.10.10", false},
		{"shorter version", "1.10", "1.10.0", false},
		{"longer min", "1.10.0", "1.10.0.0", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkVersion(tt.version, tt.minVersion)
			if (err != nil) != tt.wantErr {
				t.Errorf("checkVersion(%q, %q) error = %v, wantErr %v", tt.version, tt.minVersion, err, tt.wantErr)
			}
		})
	}
}

// Test that we can create a transport with all default values
func TestTransportWithAllDefaults(t *testing.T) {
	// Use a temp directory for crypto store
	
	transport, err := New(Config{
		
	})
	if err != nil {
		t.Fatalf("New with all defaults failed: %v", err)
	}
	
	if transport.cliPath != DefaultCLIPath {
		t.Errorf("cliPath = %s, want %s", transport.cliPath, DefaultCLIPath)
	}
	
	if transport.roomID != "" {
		t.Errorf("roomID = %s, want empty", transport.roomID)
	}
	
	if transport.config.SendTimeout != DefaultSendTimeout {
		t.Errorf("SendTimeout = %v, want %v", transport.config.SendTimeout, DefaultSendTimeout)
	}
}

// Test error types
func TestErrorTypes(t *testing.T) {
	// Verify our error types are properly defined
	if ErrIncompatible == nil {
		t.Error("ErrIncompatible should not be nil")
	}
	if ErrNotEncrypted == nil {
		t.Error("ErrNotEncrypted should not be nil")
	}
	if ErrSendFailed == nil {
		t.Error("ErrSendFailed should not be nil")
	}
	
	// Verify they are different errors
	if errors.Is(ErrIncompatible, ErrNotEncrypted) {
		t.Error("ErrIncompatible and ErrNotEncrypted should be different errors")
	}
}
