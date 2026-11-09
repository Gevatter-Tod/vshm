// Package cli provides the command-line interface.
package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/storage"
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

// DBFileName is the SQLite database file name inside the state directory.
const DBFileName = "vshm.db"

func openStore(ctx context.Context, stateDir string) (*storage.SQLiteStore, error) {
	if stateDir == "" {
		stateDir = config.GetStateDir()
	}
	store, err := storage.Open(ctx, filepath.Join(stateDir, DBFileName), storage.RealClock{}, storage.Options{})
	if err != nil {
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	return store, nil
}

// Status prints storage status for the given state directory.
func Status(ctx context.Context, stateDir string) error {
	store, err := openStore(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()

	status, err := store.Status(ctx, time.Now())
	if err != nil {
		return fmt.Errorf("failed to get status: %w", err)
	}

	fmt.Printf("Storage Status:\n")
	fmt.Printf("  Healthy: %v\n", status.Healthy)
	fmt.Printf("  Schema Version: %s\n", status.SchemaVersion)
	fmt.Printf("  Database Size: %d bytes\n", status.DBSizeBytes)
	fmt.Printf("  Lock File: %s\n", status.LockPath)
	fmt.Printf("  Observations: %d\n", status.RecordsCount)
	fmt.Printf("  Outbox Counts:\n")
	states := make([]string, 0, len(status.OutboxCounts))
	for k := range status.OutboxCounts {
		states = append(states, string(k))
	}
	sort.Strings(states)
	for _, k := range states {
		fmt.Printf("    %s: %d\n", k, status.OutboxCounts[model.DeliveryState(k)])
	}
	return nil
}

// RunAgent validates the configuration, takes the state-directory lock and runs
// until ctx is cancelled. Scheduling is added by WP-03.
func RunAgent(ctx context.Context, configPath, stateDir string) error {
	if configPath == "" {
		configPath = config.GetConfigPath()
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	if err := config.Validate(cfg); err != nil {
		return fmt.Errorf("failed to validate config: %w", err)
	}

	store, err := openStore(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()

	fmt.Printf("vshm running for host %s\n", cfg.Agent.HostName)
	<-ctx.Done()
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
