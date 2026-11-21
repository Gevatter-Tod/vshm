// Package tests holds end-to-end acceptance tests for the wired application
// (WP-13). Per spec §8 they use only local fake targets (an httptest server on
// 127.0.0.1), temporary SQLite state and the real fail-closed Matrix transport
// (no Matrix CLI installed here, so no send can happen): no live credentials,
// endpoints, databases, ping targets or Matrix accounts.
package tests

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/cli"
	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/storage"
)

// failClosedTransport is a fake transport that never sends (delivery blocked,
// e.g. Matrix not validated on this host), so e2e tests never touch the real
// matrix CLI or the network.
type failClosedTransport struct{}

func (failClosedTransport) Validate(ctx context.Context) (model.Compatibility, error) {
	return model.Compatibility{Compatible: false, Reason: "not validated"}, nil
}

func (failClosedTransport) Send(ctx context.Context, ev model.OutboxEvent) (model.DeliveryOutcome, error) {
	return model.DeliveryFailure, errors.New("delivery blocked: transport not validated")
}

// blockedOpt injects the fail-closed fake transport into cli run/once.
func blockedOpt() cli.Option {
	return cli.WithTransportFactory(func(roomID string, logger *logging.Logger) (model.Transport, error) {
		return failClosedTransport{}, nil
	})
}

// writeConfig writes a TOML config with two http checks: okURL must return 200,
// failURL is expected to fail (server status is controlled by the test).
func writeConfig(t *testing.T, dir, okURL, failURL string) string {
	t.Helper()
	cfg := fmt.Sprintf(`
[agent]
host_name = "e2e-host"
interval = "1m"
timeout = "5s"
report_time = "09:00"

[[checks]]
check_id = "ok-http"
type = "http"
url = %q
expected_status = 200
interval = "1m"
timeout = "5s"

[[checks]]
check_id = "fail-http"
type = "http"
url = %q
expected_status = 200
interval = "1m"
timeout = "5s"
`, okURL, failURL)
	p := filepath.Join(dir, "vshm.toml")
	if err := os.WriteFile(p, []byte(cfg), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return p
}

func openStore(t *testing.T, ctx context.Context, stateDir string) *storage.SQLiteStore {
	t.Helper()
	store, err := storage.Open(ctx, filepath.Join(stateDir, cli.DBFileName), storage.RealClock{}, storage.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// TestRunOnceEndToEnd runs `vshm once` against a local fake HTTP target and
// verifies the full pipeline: observations recorded, one distinct failure
// event per failed observation, incident opened on first failure, and one
// recovery event closing the incident on the next success — all persisted in
// temporary SQLite.
func TestRunOnceEndToEnd(t *testing.T) {
	var failStatus atomic.Int32
	failStatus.Store(http.StatusInternalServerError)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(int(failStatus.Load()))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, srv.URL+"/ok", srv.URL+"/fail")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// First pass: ok-http succeeds (silent), fail-http fails -> one failure
	// event + open incident.
	if err := cli.RunOnce(ctx, cfgPath, stateDir, blockedOpt()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	store := openStore(t, ctx, stateDir)
	status, err := store.Status(ctx, time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RecordsCount != 2 {
		t.Fatalf("want 2 observations recorded, got %d", status.RecordsCount)
	}
	if got := status.OutboxCounts[model.StatePending]; got != 1 {
		t.Fatalf("want 1 pending failure event, got %d", got)
	}
	snap, err := store.Snapshot(ctx, time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snap.Incidents) != 1 || snap.Incidents[0].CheckID != "fail-http" || snap.Incidents[0].ResolvedAt != nil {
		t.Fatalf("want one open incident for fail-http, got %+v", snap.Incidents)
	}
	store.Close()

	// Second pass with the target healthy: success after failure -> exactly
	// one recovery event, incident closed.
	failStatus.Store(http.StatusOK)
	if err := cli.RunOnce(ctx, cfgPath, stateDir, blockedOpt()); err != nil {
		t.Fatalf("RunOnce (recovery): %v", err)
	}
	store = openStore(t, ctx, stateDir)
	defer store.Close()
	status, err = store.Status(ctx, time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RecordsCount != 4 {
		t.Fatalf("want 4 observations recorded, got %d", status.RecordsCount)
	}
	if got := status.OutboxCounts[model.StatePending]; got != 2 {
		t.Fatalf("want 2 pending events (failure + recovery), got %d", got)
	}
	snap, err = store.Snapshot(ctx, time.Now())
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	for _, inc := range snap.Incidents {
		if inc.ResolvedAt == nil {
			t.Fatalf("want incident closed after recovery, got %+v", inc)
		}
	}
}

// TestRunCheck verifies `vshm check [ID]`: named and all-check runs, nonzero
// on a failed check, error on unknown ID — without touching any store.
func TestRunCheck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfgPath := writeConfig(t, dir, srv.URL+"/ok", srv.URL+"/fail")
	ctx := context.Background()

	if err := cli.RunCheck(ctx, "ok-http", cfgPath); err != nil {
		t.Fatalf("RunCheck ok-http: %v", err)
	}
	if err := cli.RunCheck(ctx, "fail-http", cfgPath); err == nil {
		t.Fatal("RunCheck fail-http: want nonzero error for failed check")
	}
	if err := cli.RunCheck(ctx, "", cfgPath); err == nil {
		t.Fatal("RunCheck all: want nonzero error because fail-http fails")
	}
	if err := cli.RunCheck(ctx, "no-such-check", cfgPath); err == nil {
		t.Fatal("RunCheck unknown ID: want error")
	}
	// No state must have been created: check never opens the store.
	if _, err := os.Stat(filepath.Join(dir, cli.DBFileName)); !os.IsNotExist(err) {
		t.Fatalf("RunCheck must not create a database, stat err = %v", err)
	}
}

// TestRunAgentStartStop exercises `vshm run` for a few seconds against a local
// fake target: prompt startup check, graceful shutdown on cancel, observation
// persisted, no send attempted (no Matrix CLI -> transport fails closed).
func TestRunAgentStartStop(t *testing.T) {
	// Hermetic per spec §8: no Matrix account/CLI config may be visible, so
	// the transport's lazy validation fails closed locally without any
	// network access.
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	dir := t.TempDir()
	cfg := fmt.Sprintf(`
[agent]
host_name = "e2e-run"
interval = "1m"
timeout = "5s"

[[checks]]
check_id = "ok-http"
type = "http"
url = %q
expected_status = 200
interval = "1m"
timeout = "5s"
`, srv.URL)
	cfgPath := filepath.Join(dir, "vshm.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(3 * time.Second)
		cancel()
	}()
	if err := cli.RunAgent(ctx, cfgPath, stateDir, blockedOpt()); err != nil {
		t.Fatalf("RunAgent: %v", err)
	}

	store := openStore(t, context.Background(), stateDir)
	defer store.Close()
	status, err := store.Status(context.Background(), time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.RecordsCount < 1 {
		t.Fatalf("want at least the prompt startup observation, got %d", status.RecordsCount)
	}
	// Success without an open incident must not enqueue any event, and the
	// fail-closed transport must never have delivered anything.
	if got := status.OutboxCounts[model.StateDelivered]; got != 0 {
		t.Fatalf("want 0 delivered, got %d", got)
	}
}

// TestSecondInstanceRefused verifies the state-directory lock: a second agent
// on the same state dir is refused (spec §8: second agent instance refused).
func TestSecondInstanceRefused(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	store := openStore(t, ctx, stateDir) // holds the flock for the test duration
	_ = store

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	cfg := fmt.Sprintf(`
[agent]
host_name = "e2e-lock"
interval = "1m"
timeout = "5s"

[[checks]]
check_id = "ok-http"
type = "http"
url = %q
interval = "1m"
timeout = "5s"
`, srv.URL)
	cfgPath := filepath.Join(dir, "vshm.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	err := cli.RunAgent(ctx, cfgPath, stateDir, blockedOpt())
	if err == nil {
		t.Fatal("want second instance to be refused")
	}
}
