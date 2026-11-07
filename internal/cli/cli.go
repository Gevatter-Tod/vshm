// Package cli provides the command-line interface.
package cli

import (
	"context"
	"fmt"

	"github.com/Gevatter-Tod/vshm/internal/config"
)

// Command represents a CLI command.
type Command struct {
	Name string
	Func func() error
}

// Run executes a command.
func Run(cmd Command) error {
	return cmd.Func()
}

// ValidateConfig validates the configuration file.
func ValidateConfig(ctx context.Context, configPath string) error {
	if configPath == "" {
		configPath = config.GetConfigPath()
	}

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate configuration
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("failed to validate config: %w", err)
	}

	fmt.Printf("Configuration validated successfully at %s\n", configPath)
	fmt.Printf("Host: %s\n", cfg.Agent.HostName)
	fmt.Printf("Interval: %v\n", cfg.Agent.Interval)
	fmt.Printf("Timeout: %v\n", cfg.Agent.Timeout)
	fmt.Printf("Report Time: %s\n", cfg.Agent.ReportTime)
	fmt.Printf("Room ID: %s\n", cfg.Agent.RoomID)
	fmt.Printf("Checks: %d\n", len(cfg.Checks))
	return nil
}

// RunOnce executes a single check.
func RunOnce(ctx context.Context, configPath string) error {
	if configPath == "" {
		configPath = config.GetConfigPath()
	}

	// Load configuration
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}

	// Validate configuration
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("failed to validate config: %w", err)
	}

	fmt.Printf("Running once with config at %s\n", configPath)
	fmt.Printf("Host: %s\n", cfg.Agent.HostName)
	fmt.Printf("Checks: %d\n", len(cfg.Checks))
	return nil
}

// Version returns the version.
func Version() string {
	return "v0.0.1"
}

// GetDefaultConfigPath returns the default configuration path.
func GetDefaultConfigPath() string {
	return config.GetConfigPath()
}

// GetDefaultSecretsPath returns the default secrets directory path.
func GetDefaultSecretsPath() string {
	return config.GetSecretsPath()
}

// EnsureSecretsDir creates the secrets directory with proper permissions.
func EnsureSecretsDir(path string) error {
	return config.EnsureSecretsDir(path)
}

// ValidateSecretPermissions checks that a secret file has correct permissions.
func ValidateSecretPermissions(path string) error {
	return config.ValidateSecretPermissions(path)
}
