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
	"strings"
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
	"github.com/Gevatter-Tod/vshm/internal/matrixtransport"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/outbox"
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

// buildEntries registers all check adapters and resolves the configured
// checks into scheduler entries (effective interval/timeout applied).
func buildEntries(cfg *config.Config, logger *logging.Logger) ([]scheduler.Entry, error) {
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
	return entries, nil
}

// newScheduler resolves the configured checks into a scheduler.
func newScheduler(cfg *config.Config, observer scheduler.Observer, pruner scheduler.Pruner, logger *logging.Logger) (*scheduler.Scheduler, error) {
	entries, err := buildEntries(cfg, logger)
	if err != nil {
		return nil, err
	}
	return newSchedulerFromEntries(entries, observer, pruner, logger)
}

// newSchedulerFromEntries builds a scheduler from pre-resolved entries.
func newSchedulerFromEntries(entries []scheduler.Entry, observer scheduler.Observer, pruner scheduler.Pruner, logger *logging.Logger) (*scheduler.Scheduler, error) {
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
// When the pass is done, pending events are delivered immediately (fail-closed
// if the transport is not validated).
func RunOnce(ctx context.Context, configPath, stateDir string, opts ...Option) error {
	o := resolveOptions(opts)
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

	// The pass is done: deliver the pending events (failure/recovery messages)
	// now. Fail-closed: if the Matrix transport is not validated on this host,
	// nothing is sent and the events stay pending.
	transport, err := o.transportFactory(cfg.Agent.RoomID, logger)
	if err != nil {
		return err
	}
	outbox.New(store, transport, storage.RealClock{}, logger, outbox.Config{}).Flush(ctx)
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

// RunCheck runs the named check, or every configured check when checkID is
// empty, exactly once with the check's effective timeout as the single
// absolute deadline. It never opens the store: no incident state is changed
// and nothing is sent. One sanitized result line is printed per check plus a
// summary; a nonzero error is returned when any check failed or the ID is
// unknown.
func RunCheck(ctx context.Context, checkID, configPath string) error {
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

	logger := logging.New("vshm: ")
	entries, err := buildEntries(cfg, logger)
	if err != nil {
		return err
	}

	selected := entries
	if checkID != "" {
		selected = nil
		for _, e := range entries {
			if e.Config.CheckID() == checkID {
				selected = []scheduler.Entry{e}
				break
			}
		}
		if selected == nil {
			ids := make([]string, 0, len(entries))
			for _, e := range entries {
				ids = append(ids, e.Config.CheckID())
			}
			return fmt.Errorf("unknown check ID %q (configured: %s)", checkID, strings.Join(ids, ", "))
		}
	}

	failed := 0
	for _, e := range selected {
		cctx, cancel := context.WithTimeout(ctx, e.Timeout)
		res := e.Check.Run(cctx, e.Config)
		cancel()
		fmt.Println(reports.FormatCheckLine(res.CheckID, res.CheckType, res.Success, false, time.Since(res.CompletedAt), res.ReasonCode, res.Reason))
		if !res.Success {
			failed++
		}
	}
	fmt.Printf("check: %d run, %d failed\n", len(selected), failed)
	if failed > 0 {
		return fmt.Errorf("%d of %d check(s) failed", failed, len(selected))
	}
	return nil
}

// runOptions carries optional seams for RunAgent/RunOnce.
type runOptions struct {
	transportFactory func(roomID string, logger *logging.Logger) (model.Transport, error)
}

// Option customizes RunAgent/RunOnce.
type Option func(*runOptions)

// WithTransportFactory overrides the Matrix transport factory (used by tests
// to inject a fake; production default is the fail-closed Matrix CLI transport,
// which sends nothing until validated on the host).
func WithTransportFactory(f func(roomID string, logger *logging.Logger) (model.Transport, error)) Option {
	return func(o *runOptions) { o.transportFactory = f }
}

// defaultTransportFactory builds the production Matrix CLI transport.
func defaultTransportFactory(roomID string, logger *logging.Logger) (model.Transport, error) {
	return matrixtransport.New(matrixtransport.Config{RoomID: roomID, Logger: logger})
}

func resolveOptions(opts []Option) runOptions {
	o := runOptions{transportFactory: defaultTransportFactory}
	for _, opt := range opts {
		opt(&o)
	}
	return o
}

// passObserver wraps the incident observer with two behaviors:
//   - after every recorded observation it triggers an immediate outbox flush,
//     so failure/recovery messages are sent as soon as they are recorded
//     (spec §1: alert immediately after the observation completes) instead of
//     waiting for the next delivery tick;
//   - it closes done once `want` observations have been recorded, i.e. when the
//     prompt startup pass of all configured checks has finished.
type passObserver struct {
	inner scheduler.Observer
	ob    *outbox.Manager
	ctx   context.Context
	want  int
	done  chan struct{}
	once  sync.Once
	mu    sync.Mutex
	count int
}

func (p *passObserver) Observe(ctx context.Context, r model.CheckResult) error {
	err := p.inner.Observe(ctx, r)
	if p.ob != nil {
		go p.ob.Flush(p.ctx) // async: never block a scheduler worker on a send
	}
	p.mu.Lock()
	p.count++
	hit := p.count >= p.want
	p.mu.Unlock()
	if hit {
		p.once.Do(func() { close(p.done) })
	}
	return err
}

// markDoneIfNoChecks closes done immediately when no checks are configured.
func (p *passObserver) markDoneIfNoChecks() {
	if p.want == 0 {
		p.once.Do(func() { close(p.done) })
	}
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
// the full agent until ctx is cancelled: the check scheduler (WP-03), the daily
// report scheduler (WP-11) and the outbox delivery loop (WP-12) on the Matrix
// transport. Every check runs promptly at startup; failure/recovery events are
// flushed to the outbox as soon as they are recorded, and once the startup pass
// completes, the current-date report is sent immediately before the configured
// report schedule takes over (next future time only, no catch-up). A Matrix
// compatibility failure blocks sending only — the transport fails closed and
// monitoring/persistence continue. A cleanup failure in the scheduler is
// returned as an error so the process exits nonzero and systemd restarts it.
func RunAgent(ctx context.Context, configPath, stateDir string, opts ...Option) error {
	o := resolveOptions(opts)
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
	// config.Validate applies defaults on a copy; normalize here for the
	// report service, which needs an explicit HH:MM.
	if cfg.Agent.ReportTime == "" {
		cfg.Agent.ReportTime = "09:00"
	}

	store, err := openStore(ctx, stateDir)
	if err != nil {
		return err
	}
	defer store.Close()

	logger := logging.New("vshm: ")
	entries, err := buildEntries(cfg, logger)
	if err != nil {
		return err
	}

	// Matrix transport (WP-12). Delivery is fail-closed: Send validates lazily
	// and refuses to send while the transport is not compatible, so a Matrix
	// validation failure blocks sending only — monitoring and persistence
	// continue (spec §5) and startup is never delayed by Matrix. Validate once
	// asynchronously purely for an early journal signal; it is not a startup
	// prerequisite.
	transport, err := o.transportFactory(cfg.Agent.RoomID, logger)
	if err != nil {
		return fmt.Errorf("failed to create matrix transport: %w", err)
	}
	go func() {
		vctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		compat, verr := transport.Validate(vctx)
		switch {
		case verr != nil:
			logger.Printf("matrix validation failed (delivery blocked, monitoring continues)")
		case !compat.Compatible:
			logger.Printf("matrix transport not compatible (delivery blocked, monitoring continues): %s", compat.Reason)
		default:
			logger.Printf("matrix transport validated")
		}
	}()

	// Outbox delivery loop (WP-12): one immediate batch at start (pending
	// events from before a restart are retried promptly), then every delivery
	// interval; ordered durable retries while pending.
	ob := outbox.New(store, transport, storage.RealClock{}, logger, outbox.Config{})
	if err := ob.Start(ctx); err != nil {
		return fmt.Errorf("failed to start outbox delivery: %w", err)
	}
	defer ob.Stop()

	base, err := newObserver(cfg.Agent.HostName, store, logger)
	if err != nil {
		return err
	}
	startupDone := make(chan struct{})
	observer := &passObserver{inner: base, ob: ob, ctx: ctx, want: len(entries), done: startupDone}
	observer.markDoneIfNoChecks()
	sched, err := newSchedulerFromEntries(entries, observer, store, logger)
	if err != nil {
		return err
	}

	// Daily report service (WP-11).
	limits := make([]reports.CheckLimit, 0, len(entries))
	for _, e := range entries {
		limits = append(limits, reports.CheckLimit{
			CheckID:   e.Config.CheckID(),
			CheckType: e.Config.CheckType(),
			Interval:  e.Interval,
			Timeout:   e.Timeout,
		})
	}
	reporter, err := reports.New(cfg.Agent.HostName, store, storage.RealClock{}, cfg.Agent.ReportTime, cfg.Metrics.Mounts, limits, nil, logger)
	if err != nil {
		return fmt.Errorf("failed to create report service: %w", err)
	}
	go func() {
		// Startup behavior (user-confirmed): the scheduler runs every check
		// promptly at startup; once that first pass has completed, the report
		// for the current local date is rendered, enqueued and sent right away
		// (the unique date key still prevents duplicates on restart). After the
		// first report, scheduling follows the configuration: next future
		// report time only, DST-aware, no catch-up.
		<-startupDone
		if err := reporter.ReportNow(ctx); err != nil {
			logger.Printf("startup report failed: %v", err)
		} else {
			ob.Flush(ctx) // send the report (and any failure events) now
		}
		if err := reporter.Schedule(ctx); err != nil && !errors.Is(err, context.Canceled) {
			logger.Printf("report scheduler stopped: %v", err)
		}
	}()

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

// ValidateMatrix validates the matrix-commander-rs installation and configuration.
// It checks: CLI binary exists, version meets requirements, E2EE is supported,
// and account/room are configured.
// Note: matrix-commander-rs manages its own crypto store, so we don't need to validate it.
func ValidateMatrix(ctx context.Context, cliPath, configPath, roomID string) error {
	logger := logging.New("vshm: ")
	
	// Use config values if not provided
	if cliPath == "" {
		cliPath = matrixtransport.DefaultCLIPath
	}
	if roomID == "" {
		// Try to get from config
		if configPath == "" {
			configPath = config.GetConfigPath()
		}
		if configPath != "" {
			cfg, err := config.Load(configPath)
			if err == nil {
				roomID = cfg.Agent.RoomID
			}
		}
	}

	// Validate the matrix CLI
	// cryptoStorePath is empty - matrix-commander-rs manages its own store
	result, err := matrixtransport.ValidateMatrixCLI(ctx, cliPath, configPath, "", roomID, logger)
	if err != nil {
		fmt.Printf("Matrix validation FAILED: %v\n", err)
		fmt.Printf("\nValidation details:\n")
		fmt.Printf("  CLI found: %v\n", result.CLIFound)
		fmt.Printf("  Version: %s (OK: %v)\n", result.Version, result.VersionOK)
		fmt.Printf("  Encryption: %v\n", result.EncryptionOK)
		fmt.Printf("  Room: %s (OK: %v)\n", result.RoomID, result.RoomOK)
		fmt.Printf("  Reason: %s\n", result.Reason)
		return fmt.Errorf("matrix validation failed: %w", err)
	}

	fmt.Printf("Matrix validation PASSED\n")
	fmt.Printf("\nValidation details:\n")
	fmt.Printf("  CLI: %s (found)\n", cliPath)
	fmt.Printf("  Version: %s\n", result.Version)
	fmt.Printf("  Encryption: verified\n")
	fmt.Printf("  Crypto store: %s\n", result.CryptoStorePath)
	if roomID != "" {
		fmt.Printf("  Room: %s\n", roomID)
	}
	fmt.Printf("  All checks passed: %s\n", result.Reason)
	
	return nil
}

// TestMatrix sends a test message via Matrix to verify the transport works.
// This is the only command that actually sends a message.
func TestMatrix(ctx context.Context, cliPath, roomID, message string) error {
	logger := logging.New("vshm: ")
	
	// Use default CLI path if not provided
	if cliPath == "" {
		cliPath = matrixtransport.DefaultCLIPath
	}
	
	// Fall back to the configured room if no room ID was given on the command
	// line (same behavior as ValidateMatrix).
	if roomID == "" {
		cfg, err := config.Load(config.GetConfigPath())
		if err == nil {
			roomID = cfg.Agent.RoomID
		}
	}
	if roomID == "" {
		return fmt.Errorf("room_id is required: pass it as an argument or set agent.room_id in %s", config.GetConfigPath())
	}
	
	// Create transport
	cfg := matrixtransport.Config{
		CLIPath:   cliPath,
		RoomID:    roomID,
		Logger:    logger,
	}
	
	transport, err := matrixtransport.New(cfg)
	if err != nil {
		return fmt.Errorf("failed to create transport: %w", err)
	}

	// Validate first
	compat, err := transport.Validate(ctx)
	if err != nil {
		return fmt.Errorf("validation failed: %w", err)
	}
	if !compat.Compatible {
		return fmt.Errorf("transport not compatible: %s", compat.Reason)
	}

	// Send test message
	if message == "" {
		message = "vshm test message"
	}
	
	fmt.Printf("Sending test message to room %s: %s\n", roomID, message)
	
	outcome, err := transport.TestSend(ctx, message)
	if err != nil {
		return fmt.Errorf("test send failed: %w", err)
	}

	fmt.Printf("Test message outcome: %s\n", outcome)
	
	if outcome == model.DeliverySuccess {
		fmt.Println("Test message sent successfully!")
	} else {
		fmt.Printf("Warning: test message outcome was %s (may still succeed on retry)\n", outcome)
	}

	return nil
}
