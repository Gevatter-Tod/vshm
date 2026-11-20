// Package cli provides the command-line interface.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/checks/http"
	"github.com/Gevatter-Tod/vshm/internal/checks/mysql"
	"github.com/Gevatter-Tod/vshm/internal/checks/ping"
	"github.com/Gevatter-Tod/vshm/internal/checks/systemd"
	"github.com/Gevatter-Tod/vshm/internal/checks/webdav"
	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/incidents"
	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/reports"
	"github.com/Gevatter-Tod/vshm/internal/scheduler"
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

// newObserver builds the WP-10 incident service that consumes scheduler
// observations and atomically records observation + incident + outbox events.
func newObserver(hostName string, store model.Store, logger *logging.Logger) (scheduler.Observer, error) {
	return incidents.New(hostName, store, storage.RealClock{}, logger)
}

// newScheduler resolves the configured checks into a scheduler.
func newScheduler(cfg *config.Config, observer scheduler.Observer, pruner scheduler.Pruner, logger *logging.Logger) (*scheduler.Scheduler, error) {
	reg := scheduler.NewRegistry()
	for _, c := range []model.Check{
		systemd.New(logger), http.New(logger), ping.New(logger), mysql.New(logger), webdav.New(logger),
	} {
		if err := reg.Register(c.Type(), c); err != nil {
			return nil, err
		}
	}
	entries, err := scheduler.EntriesFromConfig(cfg, reg)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve checks: %w", err)
	}
	return scheduler.New(scheduler.Options{
		Clock:    storage.RealClock{},
		Observer: observer,
		Pruner:   pruner,
		Logger:   logger,
	}, entries)
}

// RunOnce executes every configured check once on the scheduler worker pool.
// Observations are recorded through the WP-10 incident service into the store
// at stateDir (default /var/lib/vshm), so incidents and outbox events persist.
func RunOnce(ctx context.Context, configPath, stateDir string) error {
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

	store, err := openStore(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger := logging.New("vshm: ")
	observer, err := newObserver(cfg.Agent.HostName, store, logger)
	if err != nil {
		return err
	}
	printed := &printingObserver{inner: observer}
	sched, err := newScheduler(cfg, printed, store, logger)
	if err != nil {
		return err
	}
	if err := sched.Once(ctx); err != nil {
		return err
	}
	fmt.Printf("once: %d checks run, %d failed\n", printed.total, printed.failed)
	return nil
}

// printingObserver wraps the incident observer and prints one sanitized line
// per observation so `vshm once` shows what actually ran. Only the already
// sanitized reason code (and reason text on failure) is printed; check
// adapters guarantee these contain no secrets, SQL text or row values.
type printingObserver struct {
	inner  scheduler.Observer
	mu     sync.Mutex
	total  int
	failed int
}

func (p *printingObserver) Observe(ctx context.Context, r model.CheckResult) error {
	p.mu.Lock()
	p.total++
	if !r.Success {
		p.failed++
	}
	p.mu.Unlock()
	// Same line format as the daily report (shared formatter): leading status
	// emoticon, age, reason_code, optional sanitized reason; no duration field.
	fmt.Println(reports.FormatCheckLine(r.CheckID, r.CheckType, r.Success, false, time.Since(r.CompletedAt), r.ReasonCode, r.Reason))
	return p.inner.Observe(ctx, r)
}

// DBFileName is the SQLite database file name inside the state directory.
const DBFileName = "vshm.db"

func openStore(ctx context.Context, stateDir string) (*storage.SQLiteStore, error) {
	explicit := stateDir != ""
	if !explicit {
		stateDir = config.GetStateDir()
	}
	store, err := storage.Open(ctx, filepath.Join(stateDir, DBFileName), storage.RealClock{}, storage.Options{})
	if err != nil && !explicit && errors.Is(err, os.ErrPermission) {
		// The deployed service runs as the non-root vshm user, which owns
		// /var/lib/vshm, so this fallback never triggers there. For manual
		// runs as any other non-root user, fall back to a per-user state dir.
		fallback, ferr := userStateDir()
		if ferr == nil {
			fmt.Fprintf(os.Stderr, "vshm: %s is not writable by user %q; using per-user state dir %s (pass a state dir argument to choose explicitly)\n", stateDir, currentUser(), fallback)
			store, err = storage.Open(ctx, filepath.Join(fallback, DBFileName), storage.RealClock{}, storage.Options{})
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, fmt.Errorf("failed to open storage in %s: %w\nstate dir is not writable by user %q; run as the vshm service user (systemd unit) or pass a writable state dir, e.g.: vshm once <config> %s", stateDir, err, currentUser(), filepath.Join(os.TempDir(), "vshm-state"))
		}
		return nil, fmt.Errorf("failed to open storage: %w", err)
	}
	return store, nil
}

// userStateDir returns the per-user state directory used when the system
// default (/var/lib/vshm) is not writable by the invoking user:
// $XDG_STATE_HOME/vshm, or ~/.local/state/vshm.
func userStateDir() (string, error) {
	if xdg := os.Getenv("XDG_STATE_HOME"); xdg != "" {
		return filepath.Join(xdg, "vshm"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "vshm"), nil
}

// currentUser returns the invoking user name for error hints.
func currentUser() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
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
// until ctx is cancelled. A cleanup failure in the scheduler is returned as an
// error so the process exits nonzero and systemd restarts it.
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

	logger := logging.New("vshm: ")
	observer, err := newObserver(cfg.Agent.HostName, store, logger)
	if err != nil {
		return err
	}
	sched, err := newScheduler(cfg, observer, store, logger)
	if err != nil {
		return err
	}
	fmt.Printf("vshm running for host %s\n", cfg.Agent.HostName)
	return sched.Run(ctx)
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
