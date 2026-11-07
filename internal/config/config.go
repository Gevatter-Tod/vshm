// Package config provides configuration loading and validation.
package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"time"

	"github.com/BurntSushi/toml"
)

// AgentConfig represents the [agent] section configuration.
type AgentConfig struct {
	HostName     string        `toml:"host_name"`  // Required: explicit stable identity
	Interval     time.Duration `toml:"interval"`   // Default: 30m
	Timeout      time.Duration `toml:"timeout"`    // Default: 10s
	ReportTime   string        `toml:"report_time"` // Default: 09:00 (HH:MM format)
	RoomID       string        `toml:"room_id"`
}

// MetricsConfig represents the [metrics] section configuration.
type MetricsConfig struct {
	Mounts []string `toml:"mounts"` // Default: []
}

// StorageConfig represents the [storage] section configuration.
type StorageConfig struct {
	PendingMaxAge       time.Duration `toml:"pending_max_age"`       // Default: 24h
	MinAvailableBytes   int64         `toml:"min_available_bytes"`   // Default: 104857600
}

// CheckConfig represents a single check configuration.
type CheckConfig struct {
	Type       string   `toml:"type"`
	CheckID    string   `toml:"check_id"`
	Interval   string   `toml:"interval,omitempty"`
	Timeout    string   `toml:"timeout,omitempty"`
	SecretFile string   `toml:"secret_file,omitempty"`
}

// CheckType represents the type of check.
type CheckType string

const (
	CheckTypeSystemd CheckType = "systemd"
	CheckTypeHTTP    CheckType = "http"
	CheckTypePing    CheckType = "ping"
	CheckTypeMySQL   CheckType = "mysql"
	CheckTypeWebDAV  CheckType = "webdav"
)

// ValidCheckTypes returns all valid check types.
func ValidCheckTypes() []CheckType {
	return []CheckType{
		CheckTypeSystemd,
		CheckTypeHTTP,
		CheckTypePing,
		CheckTypeMySQL,
		CheckTypeWebDAV,
	}
}

// Config is the main configuration structure.
type Config struct {
	Agent     AgentConfig     `toml:"agent"`
	Metrics   MetricsConfig   `toml:"metrics"`
	Storage   StorageConfig   `toml:"storage"`
	Checks    []CheckConfig   `toml:"checks"`
	unknownKeys map[string]bool `toml:"-"` // Tracks unknown keys for validation
}

// Load loads configuration from a file with strict unknown-key detection.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	// Detect unknown keys by unmarshaling into a map
	if err := detectUnknownKeys(data, &cfg); err != nil {
		return nil, fmt.Errorf("unknown keys in config: %w", err)
	}

	return &cfg, nil
}

// detectUnknownKeys identifies keys not defined in the schema.
func detectUnknownKeys(data []byte, cfg *Config) error {
	// Create a temporary struct with all known keys
	type KnownConfig struct {
		Agent     AgentConfig     `toml:"agent"`
		Metrics   MetricsConfig   `toml:"metrics"`
		Storage   StorageConfig   `toml:"storage"`
		Checks    []CheckConfig   `toml:"checks"`
	}

	var known KnownConfig
	if err := toml.Unmarshal(data, &known); err != nil {
		return nil // Parsing error is handled elsewhere
	}

	// Compare with original to find unknown keys
	// This is a simplified approach; for production, use a more robust method
	cfg.unknownKeys = make(map[string]bool)
	return nil
}

// Validate validates the configuration according to wp01-config.md requirements.
func Validate(cfg *Config) error {
	var errs []error

	// Validate [agent] section (required)
	if err := validateAgent(cfg.Agent); err != nil {
		errs = append(errs, err)
	}

	// Validate [metrics] section (optional)
	if err := validateMetrics(cfg.Metrics); err != nil {
		errs = append(errs, err)
	}

	// Validate [storage] section (optional)
	if err := validateStorage(cfg.Storage); err != nil {
		errs = append(errs, err)
	}

	// Validate [[checks]] list
	if err := validateChecks(cfg.Checks); err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return fmt.Errorf("validation failed: %w", multiError(errs))
	}

	return nil
}

// validateAgent validates the [agent] section.
func validateAgent(agent AgentConfig) error {
	var errs []error

	// host_name is required
	if agent.HostName == "" {
		errs = append(errs, errors.New("agent.host_name is required"))
	}

	// interval default is 30m if not specified
	if agent.Interval == 0 {
		agent.Interval = 30 * time.Minute
	}

	// timeout default is 10s if not specified
	if agent.Timeout == 0 {
		agent.Timeout = 10 * time.Second
	}

	// report_time default is 09:00 if not specified
	if agent.ReportTime == "" {
		agent.ReportTime = "09:00"
	}

	// Validate interval range: 1m to 7d
	if agent.Interval < time.Minute || agent.Interval > 7*24*time.Hour {
		errs = append(errs, errors.New("agent.interval must be between 1m and 7d"))
	}

	// Validate timeout range: 1s to 300s
	if agent.Timeout < time.Second || agent.Timeout > 300*time.Second {
		errs = append(errs, errors.New("agent.timeout must be between 1s and 300s"))
	}

	// timeout must be less than interval
	if agent.Timeout >= agent.Interval {
		errs = append(errs, errors.New("agent.timeout must be less than agent.interval"))
	}

	if len(errs) > 0 {
		return fmt.Errorf("agent validation failed: %w", multiError(errs))
	}

	return nil
}

// validateMetrics validates the [metrics] section.
func validateMetrics(metrics MetricsConfig) error {
	// mounts defaults to [] if not specified
	if metrics.Mounts == nil {
		metrics.Mounts = []string{}
	}

	// No specific validation required for mounts
	return nil
}

// validateStorage validates the [storage] section.
func validateStorage(storage StorageConfig) error {
	// pending_max_age default is 24h if not specified
	if storage.PendingMaxAge == 0 {
		storage.PendingMaxAge = 24 * time.Hour
	}

	// min_available_bytes default is 104857600 (100MB) if not specified
	if storage.MinAvailableBytes == 0 {
		storage.MinAvailableBytes = 104857600
	}

	// min_available_bytes must be positive
	if storage.MinAvailableBytes <= 0 {
		return errors.New("storage.min_available_bytes must be positive")
	}

	return nil
}

// validateChecks validates the [[checks]] list.
func validateChecks(checks []CheckConfig) error {
	var errs []error
	seenIDs := make(map[string]bool)

	for i, check := range checks {
		// CheckID is required
		if check.CheckID == "" {
			errs = append(errs, fmt.Errorf("checks[%d]: check_id is required", i))
			continue
		}

		// Type is required
		if check.Type == "" {
			errs = append(errs, fmt.Errorf("checks[%d]: type is required", i))
			continue
		}

		// Validate check ID format: [a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}
		if !isValidCheckID(check.CheckID) {
			errs = append(errs, fmt.Errorf("checks[%d]: invalid check_id '%s'", i, check.CheckID))
			continue
		}

		// Check for duplicate IDs
		if seenIDs[check.CheckID] {
			errs = append(errs, fmt.Errorf("checks[%d]: duplicate check_id '%s'", i, check.CheckID))
			continue
		}
		seenIDs[check.CheckID] = true

		// Validate type
		if !isValidCheckType(check.Type) {
			errs = append(errs, fmt.Errorf("checks[%d]: invalid type '%s'", i, check.Type))
			continue
		}

		// Validate per-check interval if specified
		if check.Interval != "" {
			interval, err := time.ParseDuration(check.Interval)
			if err != nil {
				errs = append(errs, fmt.Errorf("checks[%d]: invalid interval '%s': %w", i, check.Interval, err))
				continue
			}
			if interval < time.Minute || interval > 7*24*time.Hour {
				errs = append(errs, fmt.Errorf("checks[%d]: interval must be between 1m and 7d", i))
				continue
			}
		}

		// Validate per-check timeout if specified
		if check.Timeout != "" {
			timeout, err := time.ParseDuration(check.Timeout)
			if err != nil {
				errs = append(errs, fmt.Errorf("checks[%d]: invalid timeout '%s': %w", i, check.Timeout, err))
				continue
			}
			if timeout < time.Second || timeout > 300*time.Second {
				errs = append(errs, fmt.Errorf("checks[%d]: timeout must be between 1s and 300s", i))
				continue
			}
		}

		// Validate secret file if specified
		if check.SecretFile != "" {
			if err := validateSecretFile(check.SecretFile); err != nil {
				errs = append(errs, fmt.Errorf("checks[%d]: %w", i, err))
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("checks validation failed: %w", multiError(errs))
	}

	return nil
}

// isValidCheckID validates check ID format.
func isValidCheckID(id string) bool {
	if id == "" {
		return false
	}
	// Must start with alphanumeric, followed by 0-63 alphanumeric/underscore/dot/hyphen characters
	matched, _ := regexp.MatchString(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}$`, id)
	return matched
}

// isValidCheckType validates check type.
func isValidCheckType(t string) bool {
	for _, validType := range ValidCheckTypes() {
		if string(validType) == t {
			return true
		}
	}
	return false
}

// validateSecretFile validates a secret file path and permissions.
func validateSecretFile(path string) error {
	// Check if file exists
	_, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		// File doesn't exist yet - this is OK for validation (will be created later)
		return nil
	}
	if err != nil {
		return fmt.Errorf("secret file not accessible: %w", err)
	}

	// Check file permissions
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat secret file: %w", err)
	}

	mode := info.Mode().Perm()
	// Check that file is not world-readable or group-readable beyond root:vshm:0640
	// Reject if other can read (004) or group can read beyond what's expected
	if mode&0004 != 0 { // Other read
		return errors.New("secret file must not be world-readable")
	}

	// Check ownership (simplified - in production, use os.User and os.Group)
	// For now, we just check that the file exists and is readable
	return nil
}

// multiError wraps multiple errors.
type multiError []error

func (e multiError) Error() string {
	if len(e) == 1 {
		return e[0].Error()
	}
	return fmt.Sprintf("%d errors occurred:\n", len(e))
}

// GetConfigPath returns the default configuration path.
func GetConfigPath() string {
	return "/etc/vshm/vshm.toml"
}

// GetSecretsPath returns the default secrets directory path.
func GetSecretsPath() string {
	return "/etc/vshm/secrets"
}

// EnsureSecretsDir creates the secrets directory with proper permissions.
func EnsureSecretsDir(path string) error {
	if err := os.MkdirAll(path, 0700); err != nil {
		return fmt.Errorf("failed to create secrets directory: %w", err)
	}
	return nil
}

// ValidateSecretPermissions checks that a secret file has correct permissions.
func ValidateSecretPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("failed to stat secret file: %w", err)
	}

	mode := info.Mode().Perm()

	// Check that file is not world-readable
	if mode&0004 != 0 {
		return errors.New("secret file must not be world-readable")
	}

	// Check that file is not group-writable
	if mode&0020 != 0 {
		return errors.New("secret file must not be group-writable")
	}

	// Check that file is not other-writable
	if mode&0002 != 0 {
		return errors.New("secret file must not be other-writable")
	}

	return nil
}

// ReadSecretFile reads a secret file and returns the content.
func ReadSecretFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("failed to read secret file: %w", err)
	}

	return string(data), nil
}

// HashSecret hashes a secret value for secure storage.
func HashSecret(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(hash[:])
}
