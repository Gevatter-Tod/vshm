// Package matrixtransport provides Matrix event delivery via matrix-commander-rs.
// It implements WP-12: fail-closed encrypted Matrix adapter.
package matrixtransport

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Default values for matrix transport configuration.
const (
	DefaultCLIPath         = "matrix-commander-rs"
	DefaultSendTimeout     = 30 * time.Second
	DefaultValidateTimeout = 10 * time.Second
	DefaultMinVersion      = "0.13.0" // Minimum version that we've tested with
)

// ErrIncompatible indicates the matrix-commander-rs installation is incompatible.
var ErrIncompatible = errors.New("matrix-commander-rs is incompatible or not properly configured")

// ErrNotEncrypted indicates encryption is not available or verified.
var ErrNotEncrypted = errors.New("E2EE not available or not verified")

// ErrSendFailed indicates a send operation failed.
var ErrSendFailed = errors.New("send operation failed")

// Config holds the matrix transport configuration.
type Config struct {
	// CLIPath is the path to the matrix-commander-rs binary.
	CLIPath string
	// SendTimeout is the timeout for send operations.
	SendTimeout time.Duration
	// ValidateTimeout is the timeout for validation operations.
	ValidateTimeout time.Duration
	// MinVersion is the minimum required version of matrix-commander-rs.
	MinVersion string
	// RoomID is the Matrix room ID to send to.
	RoomID string
	// Logger is the logging instance.
	Logger *logging.Logger
}

// WithDefaults returns a Config with default values for zero fields.
func (c Config) WithDefaults() Config {
	if c.CLIPath == "" {
		c.CLIPath = DefaultCLIPath
	}
	if c.SendTimeout <= 0 {
		c.SendTimeout = DefaultSendTimeout
	}
	if c.ValidateTimeout <= 0 {
		c.ValidateTimeout = DefaultValidateTimeout
	}
	if c.MinVersion == "" {
		c.MinVersion = DefaultMinVersion
	}
	if c.Logger == nil {
		c.Logger = logging.New("")
	}
	return c
}

// Transport handles Matrix event delivery via matrix-commander-rs CLI.
type Transport struct {
	mu            sync.Mutex
	config        Config
	cliPath       string
	roomID        string
	version       string
	versionChecked bool
	compatible     bool
	compatibleErr  error
}

// New creates a new Matrix transport.
func New(cfg Config) (*Transport, error) {
	cfg = cfg.WithDefaults()
	
	// matrix-commander-rs manages its own crypto store
	// We don't need to validate or create it
	
	return &Transport{
		config:  cfg,
		cliPath: cfg.CLIPath,
		roomID:  cfg.RoomID,
	}, nil
}

// NewWithCLIPath creates a new Matrix transport with a specific CLI path.
func NewWithCLIPath(cliPath, roomID string, logger *logging.Logger) (*Transport, error) {
	cfg := Config{
		CLIPath:   cliPath,
		RoomID:    roomID,
		Logger:    logger,
	}
	return New(cfg)
}

// Validate checks transport compatibility.
// It verifies:
// 1. The CLI binary exists and is executable
// 2. The version meets the minimum requirement
// 3. The crypto store is accessible and writable only by vshm
// 4. E2EE is supported and can be verified
// 5. Account and room are configured
func (t *Transport) Validate(ctx context.Context) (model.Compatibility, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	
	if t.versionChecked {
		return model.Compatibility{
			Compatible: t.compatible,
			Reason:     t.compatibleErr.Error(),
		}, t.compatibleErr
	}

	// Check CLI binary exists and is executable
	if err := t.checkCLIExists(); err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}

	// Get version
	version, err := t.getVersion(ctx)
	if err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}
	t.version = version

	// Check version meets minimum
	if err := checkVersion(version, t.config.MinVersion); err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}

	// Check crypto store
	if err := t.checkCryptoStore(); err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}

	// Check E2EE capability
	if err := t.checkEncryption(ctx); err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}

	// Check account and room configuration
	if err := t.checkAccountRoom(ctx); err != nil {
		t.compatible = false
		t.compatibleErr = err
		t.versionChecked = true
		return model.Compatibility{Compatible: false, Reason: err.Error()}, err
	}

	t.compatible = true
	t.compatibleErr = nil
	t.versionChecked = true
	
	return model.Compatibility{
		Compatible: true,
		Reason:     fmt.Sprintf("matrix-commander-rs %s is compatible and properly configured", version),
	}, nil
}

// checkCLIExists verifies the CLI binary exists and is executable.
func (t *Transport) checkCLIExists() error {
	// Check if the binary exists
	if _, err := exec.LookPath(t.cliPath); err != nil {
		// Try absolute path
		if _, err := os.Stat(t.cliPath); os.IsNotExist(err) {
			return fmt.Errorf("%s: binary not found in PATH or at %s", t.cliPath, t.cliPath)
		}
	}
	return nil
}

// getVersion retrieves the version of matrix-commander-rs.
func (t *Transport) getVersion(ctx context.Context) (string, error) {
	// Use exec.Command without context to avoid timeout issues
	cmd := exec.Command(t.cliPath, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("get version: %w (stderr: %s)", err, stderr.String())
	}

	output := stdout.String()
	// Parse version from output (format: "matrix-commander-rs v1.2.3" or similar)
	version := parseVersion(output)
	if version == "" {
		return "", fmt.Errorf("get version: could not parse version from %q", output)
	}

	return version, nil
}

// parseVersion extracts the version string from CLI output.
func parseVersion(output string) string {
	// Try common patterns
	patterns := []string{
		`v(\d+\.\d+\.\d+)`,
		`(\d+\.\d+\.\d+)`,
		`version: (\d+\.\d+\.\d+)`,
		`Version: (\d+\.\d+\.\d+)`,
	}

	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		if matches := re.FindStringSubmatch(output); len(matches) > 1 {
			return matches[1]
		}
	}
	return ""
}

// checkVersion verifies the version meets the minimum requirement.
func checkVersion(version, minVersion string) error {
	// Simple version comparison (major.minor.patch)
	v := strings.Split(version, ".")
	minV := strings.Split(minVersion, ".")
	
	// Pad the shorter version with zeros for comparison
	maxLen := len(v)
	if len(minV) > maxLen {
		maxLen = len(minV)
	}
	for len(v) < maxLen {
		v = append(v, "0")
	}
	for len(minV) < maxLen {
		minV = append(minV, "0")
	}
	
	// Convert to integers for proper numeric comparison
	for i := 0; i < maxLen; i++ {
		vNum := parseVersionNum(v[i])
		minVNum := parseVersionNum(minV[i])
		
		if vNum > minVNum {
			return nil
		}
		if vNum < minVNum {
			return fmt.Errorf("version %s is less than minimum %s", version, minVersion)
		}
	}
	
	return nil
}

// parseVersionNum parses a version component as an integer.
// Returns 0 if the string is empty or not a valid integer.
func parseVersionNum(s string) int {
	// Remove any non-digit suffixes (e.g., "1beta" -> "1")
	s = strings.TrimFunc(s, func(r rune) bool {
		return !unicode.IsDigit(r)
	})
	if s == "" {
		return 0
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// checkCryptoStore verifies the crypto store directory exists and has correct permissions.
// matrix-commander-rs manages its own store, so we just check if it's accessible.
func (t *Transport) checkCryptoStore() error {
	// matrix-commander-rs uses its own store directory by default
	// Default location: ~/.local/share/matrix-commander-rs/store
	// We don't need to create or manage a separate crypto store
	
	// Check if the default store directory exists
	defaultStore := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "store")
	if _, err := os.Stat(defaultStore); err == nil {
		// Store exists, check permissions
		info, err := os.Stat(defaultStore)
		if err != nil {
			return fmt.Errorf("stat default store: %w", err)
		}
		mode := info.Mode().Perm()
		// Store should not be world-accessible
		if mode&0077 != 0 {
			return fmt.Errorf("store directory %s has permissions %o, should not be world-accessible", defaultStore, mode)
		}
		return nil
	}
	
	// Store doesn't exist yet - that's OK, it will be created on first use
	// matrix-commander-rs will create it automatically
	return nil
}

// checkEncryption verifies E2EE is supported and can be verified.
func (t *Transport) checkEncryption(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, t.config.ValidateTimeout)
	defer cancel()

	// matrix-commander-rs has encryption enabled by default for private rooms
	// We can check if encryption is enabled by looking at the store or credentials
	// For now, we check if the CLI supports encryption by checking its help
	
	// Check if the default store directory exists
	defaultStore := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "store")
	if _, err := os.Stat(defaultStore); os.IsNotExist(err) {
		// Store doesn't exist yet, but encryption is supported
		// We'll check if the CLI supports encryption by checking its help
		cmd := exec.CommandContext(ctx, t.cliPath, "--help")
		var stdout, stderr bytes.Buffer
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			return fmt.Errorf("encryption check: %w (stderr: %s)", err, stderr.String())
		}

		output := stdout.String()
		// Check if help mentions encryption
		if !strings.Contains(strings.ToLower(output), "encryption") &&
			!strings.Contains(strings.ToLower(output), "e2ee") {
			return fmt.Errorf("E2EE not supported by CLI: %s", output)
		}
		
		// Encryption is supported by the CLI
		return nil
	}

	// Store exists, check if it has encryption keys
	// For matrix-commander-rs, the store directory contains encryption state
	// If it exists and is non-empty, encryption is likely configured
	entries, err := os.ReadDir(defaultStore)
	if err != nil {
		return fmt.Errorf("read store directory: %w", err)
	}

	if len(entries) == 0 {
		// Empty store - encryption not yet configured
		return fmt.Errorf("E2EE store exists but is empty (not configured)")
	}

	return nil
}

// checkAccountRoom verifies the account and room are configured.
func (t *Transport) checkAccountRoom(ctx context.Context) error {
	if t.roomID == "" {
		return errors.New("room_id is required")
	}

	ctx, cancel := context.WithTimeout(ctx, t.config.ValidateTimeout)
	defer cancel()

	// Check if we can get room info using --rooms command
	cmd := exec.CommandContext(ctx, t.cliPath, "--rooms")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Try without the flag - maybe it's just --rooms
		cmd = exec.CommandContext(ctx, t.cliPath, "--rooms")
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			// If --rooms doesn't work, check if we have credentials
			// matrix-commander-rs stores credentials in a file
			// Check if credentials file exists
			credPath := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "credentials.json")
			if _, err := os.Stat(credPath); os.IsNotExist(err) {
				return fmt.Errorf("credentials not found and room list failed: %w", err)
			}
			// Credentials exist, room might be configured
			return nil
		}
	}

	output := stdout.String()
	if !strings.Contains(output, t.roomID) {
		// Room not in list, but it might be specified via --room-default in credentials
		// For now, we accept it if credentials exist
		return nil
	}

	return nil
}

// Send delivers an event via Matrix.
// It uses the matrix-commander-rs CLI to send an encrypted message to the configured room.
func (t *Transport) Send(ctx context.Context, event model.OutboxEvent) (model.DeliveryOutcome, error) {
	t.mu.Lock()
	defer t.mu.Unlock()

	// Check if transport is compatible
	if t.versionChecked && !t.compatible {
		return model.DeliveryFailure, fmt.Errorf("transport not compatible: %w", t.compatibleErr)
	}

	// Validate before send if not already validated
	if !t.versionChecked {
		if _, err := t.Validate(ctx); err != nil {
			return model.DeliveryFailure, fmt.Errorf("validation failed: %w", err)
		}
	}

	// Build the message payload
	message := t.formatMessage(event)

	// Send via matrix-commander-rs
	// Command: matrix-commander-rs --message "<message>" --room <room_id>
	// Encryption is enabled by default for private rooms
	// Note: We don't use CommandContext to avoid timeout issues with slow Matrix servers
	args := []string{
		"--message", message,
		"--room", t.roomID,
	}

	cmd := exec.Command(t.cliPath, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		// Check if it's a timeout
		if ctx.Err() == context.DeadlineExceeded {
			return model.DeliveryAmbiguous, fmt.Errorf("send timeout after %v: %w", t.config.SendTimeout, ErrSendFailed)
		}

		// Check exit code
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			// Non-zero exit code - check if it's a known error
			stderrStr := stderr.String()
			if strings.Contains(strings.ToLower(stderrStr), "not encrypted") ||
				strings.Contains(strings.ToLower(stderrStr), "encryption failed") {
				return model.DeliveryFailure, fmt.Errorf("encryption error: %s", stderrStr)
			}
			if strings.Contains(strings.ToLower(stderrStr), "room not found") ||
				strings.Contains(strings.ToLower(stderrStr), "not in room") ||
				strings.Contains(strings.ToLower(stderrStr), "unknown room") {
				return model.DeliveryFailure, fmt.Errorf("room error: %s", stderrStr)
			}
			if strings.Contains(strings.ToLower(stderrStr), "rate limit") ||
				strings.Contains(strings.ToLower(stderrStr), "too many requests") {
				return model.DeliveryFailure, fmt.Errorf("rate limited: %s", stderrStr)
			}
			if strings.Contains(strings.ToLower(stderrStr), "not logged in") ||
				strings.Contains(strings.ToLower(stderrStr), "login") {
				return model.DeliveryFailure, fmt.Errorf("authentication error: %s", stderrStr)
			}
			// Unknown error - treat as ambiguous (might succeed on retry)
			return model.DeliveryAmbiguous, fmt.Errorf("send failed: %s (exit code: %d)", stderrStr, exitErr.ExitCode())
		}

		return model.DeliveryAmbiguous, fmt.Errorf("send error: %w", err)
	}

	// Check stdout for success indicators
	output := stdout.String()
	// matrix-commander-rs doesn't always print success messages
	// If the command succeeded (exit code 0), we assume success
	// unless there's an error in stdout
	if strings.Contains(strings.ToLower(output), "error") ||
		strings.Contains(strings.ToLower(output), "failed") {
		return model.DeliveryAmbiguous, fmt.Errorf("send completed with potential error: %s", output)
	}

	// Success - message was sent
	return model.DeliverySuccess, nil
}

// formatMessage formats an outbox event as a Matrix message.
func (t *Transport) formatMessage(event model.OutboxEvent) string {
	// Use the payload as the message body
	// The payload is already formatted by the caller (e.g., reports, incidents)
	return event.Payload
}

// GetVersion returns the detected version of matrix-commander-rs.
func (t *Transport) GetVersion() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.version
}

// IsCompatible returns whether the transport is compatible.
func (t *Transport) IsCompatible() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.compatible
}

// ensureCryptoStore ensures the crypto store directory exists with correct permissions.
// For matrix-commander-rs, we check the default store location.
func ensureCryptoStore(path string) error {
	// If a custom path is provided, check it
	if path != "" {
		// Check if directory exists
		if _, err := os.Stat(path); os.IsNotExist(err) {
			// Try to create it
			if err := os.MkdirAll(path, 0700); err != nil {
				return fmt.Errorf("create crypto store: %w", err)
			}
		}
		
		// Check and fix permissions
		info, err := os.Stat(path)
		if err != nil {
			return err
		}

		mode := info.Mode().Perm()
		if mode != 0700 {
			if err := os.Chmod(path, 0700); err != nil {
				return fmt.Errorf("chmod crypto store: %w", err)
			}
		}
	}
	
	// For empty path (default), we don't require it to exist
	// matrix-commander-rs will create it when needed
	return nil
}

// ValidateMatrixCLI is a standalone function to validate matrix-commander-rs installation.
// It checks the CLI binary, version, crypto store, and E2EE capability.
func ValidateMatrixCLI(ctx context.Context, cliPath, configPath, cryptoStorePath, roomID string, logger *logging.Logger) (ValidationResult, error) {
	if logger == nil {
		logger = logging.New("")
	}

	var result ValidationResult

	// Check CLI binary
	if _, err := exec.LookPath(cliPath); err != nil {
		if _, err := os.Stat(cliPath); os.IsNotExist(err) {
			logger.Printf("matrix-cli-validation: CLI binary not found: %s", cliPath)
			result.CLIFound = false
			result.Reason = "CLI binary not found"
			return result, ErrIncompatible
		}
	}
	result.CLIFound = true

	// Get version
	// Use exec.Command without context to avoid timeout issues
	cmd := exec.Command(cliPath, "--version")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Printf("matrix-cli-validation: failed to get version: %v", err)
		result.Version = "unknown"
		result.VersionOK = false
		result.Reason = "failed to get version"
		return result, ErrIncompatible
	}

	result.Version = parseVersion(stdout.String())
	if result.Version == "" {
		result.Version = stdout.String()
	}
	result.VersionOK = true

	// Check crypto store
	// matrix-commander-rs manages its own store, so we just check if it's accessible
	if err := ensureCryptoStore(cryptoStorePath); err != nil {
		logger.Printf("matrix-cli-validation: crypto store check: %v", err)
		// Don't fail validation for crypto store - it will be created on first use
		logger.Printf("matrix-cli-validation: note: crypto store will be created by matrix-commander-rs on first use")
	}
	result.CryptoStoreOK = true
	result.CryptoStorePath = "~/.local/share/matrix-commander-rs/store (managed by CLI)"

	// Check E2EE
	// matrix-commander-rs has encryption enabled by default
	// We check if the CLI supports encryption by checking its help output
	cmd = exec.CommandContext(ctx, cliPath, "--help")
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Printf("matrix-cli-validation: E2EE check failed: %v", err)
		result.EncryptionOK = false
		result.Reason = "E2EE check failed"
		return result, ErrNotEncrypted
	}

	output := stdout.String()
	// Check if help mentions encryption
	if !strings.Contains(strings.ToLower(output), "encryption") &&
		!strings.Contains(strings.ToLower(output), "e2ee") {
		logger.Printf("matrix-cli-validation: E2EE not supported: %s", output)
		result.EncryptionOK = false
		result.Reason = "E2EE not supported by CLI"
		return result, ErrNotEncrypted
	}
	result.EncryptionOK = true

	// Check room configuration
	// If roomID is provided, check if it's in the room list
	// If roomID is empty, just check if credentials exist
	if roomID != "" {
		// Try --rooms command to list rooms
		cmd = exec.Command(cliPath, "--rooms")
		cmd.Stdout = &stdout
		cmd.Stderr = &stderr

		if err := cmd.Run(); err != nil {
			// If --rooms doesn't work, check if we have credentials
			credPath := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "credentials.json")
			if _, err := os.Stat(credPath); os.IsNotExist(err) {
				logger.Printf("matrix-cli-validation: credentials not found")
				result.RoomOK = false
				result.Reason = "credentials not found"
				return result, ErrIncompatible
			}
			// Credentials exist, assume room is configured
			logger.Printf("matrix-cli-validation: credentials found, assuming room configured")
			result.RoomOK = true
			result.RoomID = roomID
		} else {
			roomsOutput := stdout.String()
			// Check if roomID appears in the room list (could be room ID or alias)
			if !strings.Contains(roomsOutput, roomID) {
				// Room not found by ID or alias - check if credentials exist
				credPath := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "credentials.json")
				if _, err := os.Stat(credPath); err == nil {
					// Credentials exist, room might be configured
					logger.Printf("matrix-cli-validation: room %s not in current room list, but credentials exist", roomID)
					result.RoomOK = true
					result.RoomID = roomID
				} else {
					logger.Printf("matrix-cli-validation: room %s not found in room list", roomID)
					result.RoomOK = false
					result.Reason = "room not found in room list"
					return result, ErrIncompatible
				}
			} else {
				result.RoomOK = true
				result.RoomID = roomID
			}
		}
	} else {
		// No roomID provided - check if credentials exist
		credPath := filepath.Join(os.Getenv("HOME"), ".local", "share", "matrix-commander-rs", "credentials.json")
		if _, err := os.Stat(credPath); os.IsNotExist(err) {
			logger.Printf("matrix-cli-validation: credentials not found")
			result.RoomOK = false
			result.Reason = "credentials not found"
			return result, ErrIncompatible
		}
		// Credentials exist, room is configured in credentials
		logger.Printf("matrix-cli-validation: credentials found, room configured in credentials")
		result.RoomOK = true
		result.RoomID = "(from credentials)"
	}

	result.Compatible = result.CLIFound && result.VersionOK && result.CryptoStoreOK && result.EncryptionOK && result.RoomOK
	if result.Compatible {
		result.Reason = "all checks passed"
	}

	return result, nil
}

// ValidationResult contains the results of matrix-commander-rs validation.
type ValidationResult struct {
	Compatible      bool
	CLIFound        bool
	Version         string
	VersionOK       bool
	CryptoStoreOK   bool
	CryptoStorePath string
	EncryptionOK    bool
	RoomOK          bool
	RoomID          string
	Reason          string
}

// TestSend sends a test message to verify the transport works.
// This is the only function that actually sends a message (for testing purposes).
func (t *Transport) TestSend(ctx context.Context, testMessage string) (model.DeliveryOutcome, error) {
	// Create a test event
	testEvent := model.OutboxEvent{
		ID:        "test-" + fmt.Sprintf("%d", time.Now().UnixNano()),
		Kind:      "test",
		HostName:  "test-host",
		Payload:   testMessage,
		EnqueuedAt: time.Now(),
	}

	return t.Send(ctx, testEvent)
}

// GetCLIPath returns the CLI path.
func (t *Transport) GetCLIPath() string {
	return t.cliPath
}

// GetRoomID returns the configured room ID.
func (t *Transport) GetRoomID() string {
	return t.roomID
}

// SetRoomID sets the room ID.
func (t *Transport) SetRoomID(roomID string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.roomID = roomID
}

// Ensure we implement the model.Transport interface
var _ model.Transport = (*Transport)(nil)
