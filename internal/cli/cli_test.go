package cli

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/storage"
)

// recordingTransport is a fake model.Transport that records every Send.
type recordingTransport struct {
	mu      sync.Mutex
	sent    []model.OutboxEvent
	outcome model.DeliveryOutcome
}

func (r *recordingTransport) Validate(ctx context.Context) (model.Compatibility, error) {
	return model.Compatibility{Compatible: true, Reason: "fake ok"}, nil
}

func (r *recordingTransport) Send(ctx context.Context, ev model.OutboxEvent) (model.DeliveryOutcome, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, ev)
	if r.outcome == model.DeliveryFailure {
		return model.DeliveryFailure, errors.New("blocked")
	}
	return r.outcome, nil
}

func (r *recordingTransport) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.sent)
}

func (r *recordingTransport) kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, 0, len(r.sent))
	for _, ev := range r.sent {
		out = append(out, ev.Kind+":"+ev.CheckID)
	}
	return out
}

// fakeFactory returns a WithTransportFactory option injecting tr.
func fakeFactory(tr model.Transport) Option {
	return WithTransportFactory(func(roomID string, logger *logging.Logger) (model.Transport, error) {
		return tr, nil
	})
}

func writeTestConfig(t *testing.T, dir, okURL, failURL string) string {
	t.Helper()
	cfg := fmt.Sprintf(`
[agent]
host_name = "cli-test"
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
		t.Fatal(err)
	}
	return p
}

func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func statusOf(t *testing.T, ctx context.Context, stateDir string) model.StoreStatus {
	t.Helper()
	store, err := storage.Open(ctx, filepath.Join(stateDir, DBFileName), storage.RealClock{}, storage.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	st, err := store.Status(ctx, time.Now())
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	return st
}

// TestRunOnceSendsFailureMessageWhenPassDone: `vshm once` runs all checks once
// and, when done, delivers the enqueued failure message immediately.
func TestRunOnceSendsFailureMessageWhenPassDone(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, srv.URL+"/ok", srv.URL+"/fail")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	rec := &recordingTransport{outcome: model.DeliverySuccess}

	if err := RunOnce(context.Background(), cfgPath, stateDir, fakeFactory(rec)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("want exactly 1 message sent when the pass is done, got %d (%v)", rec.count(), rec.kinds())
	}
	if !strings.HasPrefix(rec.kinds()[0], "event:") || !strings.Contains(rec.sent[0].Payload, "check=fail-http") {
		t.Fatalf("want failure event for fail-http, got %v payload=%q", rec.kinds(), rec.sent[0].Payload)
	}
	st := statusOf(t, context.Background(), stateDir)
	if st.OutboxCounts[model.StateDelivered] != 1 || st.OutboxCounts[model.StatePending] != 0 {
		t.Fatalf("want delivered=1 pending=0, got %v", st.OutboxCounts)
	}
}

// TestRunOnceFailClosedTransportKeepsEventPending: without a validated
// transport nothing is sent and the event stays pending.
func TestRunOnceFailClosedTransportKeepsEventPending(t *testing.T) {
	srv := newTestServer(t)
	dir := t.TempDir()
	cfgPath := writeTestConfig(t, dir, srv.URL+"/ok", srv.URL+"/fail")
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	rec := &recordingTransport{outcome: model.DeliveryFailure}

	if err := RunOnce(context.Background(), cfgPath, stateDir, fakeFactory(rec)); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if rec.count() != 1 {
		t.Fatalf("want 1 send attempt, got %d", rec.count())
	}
	st := statusOf(t, context.Background(), stateDir)
	if st.OutboxCounts[model.StatePending] != 1 || st.OutboxCounts[model.StateDelivered] != 0 {
		t.Fatalf("want pending=1 delivered=0 (fail closed), got %v", st.OutboxCounts)
	}
}

// TestRunAgentSendsStartupReportAfterFirstPass: `vshm run` executes all checks
// right away and, once that startup pass is done, sends the current-date report
// via the transport; afterwards it keeps running on the configured schedule.
func TestRunAgentSendsStartupReportAfterFirstPass(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	srv := newTestServer(t)
	dir := t.TempDir()
	cfg := fmt.Sprintf(`
[agent]
host_name = "cli-run"
interval = "1h"
timeout = "5s"
report_time = "09:00"

[[checks]]
check_id = "ok-http"
type = "http"
url = %q
expected_status = 200
interval = "1h"
timeout = "5s"
`, srv.URL+"/ok")
	cfgPath := filepath.Join(dir, "vshm.toml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}

	rec := &recordingTransport{outcome: model.DeliverySuccess}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- RunAgent(ctx, cfgPath, stateDir, fakeFactory(rec)) }()

	// Wait until the startup report has been sent (poll the fake transport).
	deadline := time.Now().Add(15 * time.Second)
	sent := false
	for time.Now().Before(deadline) {
		for _, k := range rec.kinds() {
			if k == "report:" {
				sent = true
			}
		}
		if sent {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("RunAgent: %v", err)
	}
	if !sent {
		t.Fatalf("startup report was not sent after the first check pass; sends=%v", rec.kinds())
	}
	st := statusOf(t, context.Background(), stateDir)
	if st.OutboxCounts[model.StateDelivered] < 1 {
		t.Fatalf("want the startup report delivered, got %v", st.OutboxCounts)
	}
	if st.RecordsCount < 1 {
		t.Fatalf("want the prompt startup observation recorded, got %d", st.RecordsCount)
	}
}
