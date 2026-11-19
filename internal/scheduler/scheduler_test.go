package scheduler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// ---- fake clock ----------------------------------------------------------

type testClock struct {
	mu     sync.Mutex
	now    time.Time
	timers map[*testTimer]struct{}
}

type testTimer struct {
	c      *testClock
	when   time.Time
	ch     chan time.Time
	active bool
}

func newTestClock() *testClock {
	return &testClock{
		now:    time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC),
		timers: map[*testTimer]struct{}{},
	}
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) NewTimer(d time.Duration) model.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &testTimer{c: c, when: c.now.Add(d), ch: make(chan time.Time, 1), active: true}
	c.timers[t] = struct{}{}
	return t
}

func (t *testTimer) C() <-chan time.Time { return t.ch }

func (t *testTimer) Stop() bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.active = false
	delete(t.c.timers, t)
	return was
}

func (t *testTimer) Reset(d time.Duration) bool {
	t.c.mu.Lock()
	defer t.c.mu.Unlock()
	was := t.active
	t.active = true
	t.when = t.c.now.Add(d)
	t.c.timers[t] = struct{}{}
	return was
}

// Advance moves time forward in one step and fires every timer that became due.
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	for t := range c.timers {
		if !t.when.After(c.now) {
			t.active = false
			delete(c.timers, t)
			t.ch <- c.now
		}
	}
	c.mu.Unlock()
}

// hasTimerAt reports whether an active timer is due exactly at when.
func (c *testClock) hasTimerAt(when time.Time) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for t := range c.timers {
		if t.when.Equal(when) {
			return true
		}
	}
	return false
}

// ---- helpers -------------------------------------------------------------

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// never asserts cond stays false for a short real period.
func never(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 20; i++ {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(time.Millisecond)
	}
}

type recorder struct {
	mu      sync.Mutex
	results []model.CheckResult
}

func (r *recorder) Observe(_ context.Context, res model.CheckResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res)
	return nil
}

func (r *recorder) all() []model.CheckResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]model.CheckResult(nil), r.results...)
}

func (r *recorder) count(id string) int {
	n := 0
	for _, res := range r.all() {
		if id == "" || res.CheckID == id {
			n++
		}
	}
	return n
}

type testLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *testLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (l *testLogger) count(sub string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, ln := range l.lines {
		if strings.Contains(ln, sub) {
			n++
		}
	}
	return n
}

type cfg struct{ id, typ string }

func (c cfg) CheckID() string   { return c.id }
func (c cfg) CheckType() string { return c.typ }

// fakeCheck is a controllable adapter. run is called with the execution context.
type fakeCheck struct {
	typ     string
	runs    atomic.Int32
	active  atomic.Int32
	maxSeen atomic.Int32
	run     func(ctx context.Context, cfg model.CheckConfig) model.CheckResult
	closed  atomic.Int32
}

func (f *fakeCheck) Type() string { return f.typ }

func (f *fakeCheck) Run(ctx context.Context, c model.CheckConfig) model.CheckResult {
	f.runs.Add(1)
	n := f.active.Add(1)
	for {
		m := f.maxSeen.Load()
		if n <= m || f.maxSeen.CompareAndSwap(m, n) {
			break
		}
	}
	defer f.active.Add(-1)
	if f.run != nil {
		return f.run(ctx, c)
	}
	return model.CheckResult{Success: true, ReasonCode: "ok", Reason: "fine"}
}

func (f *fakeCheck) Close() error { f.closed.Add(1); return nil }

func entry(f model.Check, id string, interval, timeout time.Duration) Entry {
	return Entry{Check: f, Config: cfg{id: id, typ: "fake"}, Interval: interval, Timeout: timeout}
}

type harness struct {
	s      *Scheduler
	clock  *testClock
	t0     time.Time
	rec    *recorder
	log    *testLogger
	cancel context.CancelFunc
	done   chan error
}

func start(t *testing.T, pruner Pruner, entries ...Entry) *harness {
	t.Helper()
	h := &harness{clock: newTestClock(), rec: &recorder{}, log: &testLogger{}, done: make(chan error, 1)}
	s, err := New(Options{Clock: h.clock, Observer: h.rec, Pruner: pruner, Logger: h.log}, entries)
	if err != nil {
		t.Fatal(err)
	}
	h.s = s
	h.t0 = h.clock.Now()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	go func() { h.done <- s.Run(ctx) }()
	t.Cleanup(cancel)
	return h
}

// waitTimer waits until a timer due at start+offset is armed.
func (h *harness) waitTimer(t *testing.T, base time.Time, offset time.Duration) {
	t.Helper()
	eventually(t, fmt.Sprintf("timer armed at +%v", offset), func() bool { return h.clock.hasTimerAt(base.Add(offset)) })
}

func (h *harness) stop(t *testing.T) error {
	t.Helper()
	h.cancel()
	select {
	case err := <-h.done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
		return nil
	}
}

// ---- tests ---------------------------------------------------------------

func TestNewValidation(t *testing.T) {
	clk, rec := newTestClock(), &recorder{}
	f := &fakeCheck{typ: "fake"}
	cases := map[string][]Entry{
		"timeout>=interval": {entry(f, "a", time.Minute, time.Minute)},
		"zero timeout":      {entry(f, "a", time.Minute, 0)},
		"duplicate id":      {entry(f, "a", time.Minute, time.Second), entry(f, "a", time.Minute, time.Second)},
		"nil check":         {{Config: cfg{id: "a"}, Interval: time.Minute, Timeout: time.Second}},
	}
	for name, entries := range cases {
		if _, err := New(Options{Clock: clk, Observer: rec}, entries); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	if _, err := New(Options{Observer: rec}, nil); err == nil {
		t.Error("missing clock accepted")
	}
	if _, err := New(Options{Clock: clk}, nil); err == nil {
		t.Error("missing observer accepted")
	}
}

func TestPromptStartThenMonotonicCadence(t *testing.T) {
	f := &fakeCheck{typ: "fake"}
	h := start(t, nil, entry(f, "a", time.Minute, 5*time.Second))

	eventually(t, "initial run", func() bool { return h.rec.count("a") == 1 })
	if got := h.rec.all()[0]; !got.StartedAt.Equal(h.t0) || got.StartedAt.Location() != time.UTC {
		t.Fatalf("initial run not prompt/UTC: %v", got.StartedAt)
	}
	h.waitTimer(t, h.t0, time.Minute)

	h.clock.Advance(59 * time.Second)
	never(t, "early run", func() bool { return h.rec.count("a") > 1 })

	h.clock.Advance(time.Second)
	eventually(t, "second run", func() bool { return h.rec.count("a") == 2 })
	if got := h.rec.all()[1].StartedAt; !got.Equal(h.t0.Add(time.Minute)) {
		t.Fatalf("second run at %v, want %v", got, h.t0.Add(time.Minute))
	}
	if err := h.stop(t); err != nil {
		t.Fatal(err)
	}
}

func TestPerCheckIntervals(t *testing.T) {
	fast, slow := &fakeCheck{typ: "fake"}, &fakeCheck{typ: "fake"}
	h := start(t, nil, entry(fast, "fast", time.Minute, time.Second), entry(slow, "slow", 5*time.Minute, time.Second))
	eventually(t, "initial runs", func() bool { return h.rec.count("") == 2 })
	for i := 0; i < 5; i++ {
		h.waitTimer(t, h.t0, time.Duration(i+1)*time.Minute)
		h.clock.Advance(time.Minute)
		want := int32(i + 2)
		eventually(t, "fast run", func() bool { return fast.runs.Load() == want })
	}
	eventually(t, "slow second run", func() bool { return slow.runs.Load() == 2 })
	never(t, "extra runs", func() bool { return fast.runs.Load() != 6 || slow.runs.Load() != 2 })
	h.stop(t)
}

func TestMissedTicksAreNotReplayed(t *testing.T) {
	f := &fakeCheck{typ: "fake"}
	h := start(t, nil, entry(f, "a", time.Minute, time.Second))
	eventually(t, "initial run", func() bool { return f.runs.Load() == 1 })
	h.waitTimer(t, h.t0, time.Minute)

	// The process stalls for 10 intervals: exactly one catch-up run, no replay.
	h.clock.Advance(10*time.Minute + 30*time.Second)
	eventually(t, "one run", func() bool { return f.runs.Load() == 2 })
	never(t, "replayed runs", func() bool { return f.runs.Load() > 2 })

	// Cadence stays on the original grid: next run at t0+11m.
	h.waitTimer(t, h.t0, 11*time.Minute)
	h.clock.Advance(29 * time.Second)
	never(t, "off-grid run", func() bool { return f.runs.Load() > 2 })
	h.clock.Advance(time.Second)
	eventually(t, "grid run", func() bool { return f.runs.Load() == 3 })
	h.stop(t)
}

// Blocked checks ignore their context so they stay "active" through the deadline
// (inside the cleanup grace). The next due tick must then be skipped and logged.
func TestSkipDueTickWhilePreviousExecutionActive(t *testing.T) {
	release := make(chan struct{})
	blocked := func(context.Context, model.CheckConfig) model.CheckResult {
		<-release
		return model.CheckResult{Success: true}
	}
	var entries []Entry
	var blockers []*fakeCheck
	for i := 0; i < Workers; i++ {
		f := &fakeCheck{typ: "fake", run: blocked}
		blockers = append(blockers, f)
		entries = append(entries, entry(f, fmt.Sprintf("b%d", i), time.Minute, 59*time.Second))
	}
	queued := &fakeCheck{typ: "fake"}
	entries = append(entries, entry(queued, "queued", time.Minute, 5*time.Second))
	h := start(t, nil, entries...)

	eventually(t, "all workers busy", func() bool {
		n := int32(0)
		for _, b := range blockers {
			n += b.active.Load()
		}
		return n == Workers
	})
	h.waitTimer(t, h.t0, 59*time.Second)
	h.waitTimer(t, h.t0, time.Minute)
	if queued.runs.Load() != 0 {
		t.Fatal("queued check ran while all workers were busy")
	}

	h.clock.Advance(59 * time.Second) // blockers time out but stay in cleanup until t0+61s
	eventually(t, "timeouts recorded", func() bool { return h.rec.count("") == Workers })
	h.clock.Advance(time.Second) // t0+60s: every check is still queued/running
	eventually(t, "skips logged", func() bool { return h.log.count("skipping due run of check") == Workers+1 })
	if h.log.count("skipping due run of check queued") != 1 {
		t.Fatal("skip of the still-queued check not logged")
	}
	if queued.runs.Load() != 0 {
		t.Fatal("skipped tick ran the check")
	}

	close(release)
	eventually(t, "queued runs once", func() bool { return h.rec.count("queued") == 1 })
	never(t, "extra queued runs", func() bool { return queued.runs.Load() != 1 })
	for _, r := range h.rec.all() {
		if r.CheckID == "queued" && !r.Success {
			t.Fatalf("fabricated failure: %+v", r)
		}
	}
	for _, b := range blockers {
		if b.maxSeen.Load() != 1 || b.runs.Load() != 1 {
			t.Fatalf("check overlapped or re-ran: max=%d runs=%d", b.maxSeen.Load(), b.runs.Load())
		}
	}
	if !h.s.Healthy() {
		t.Fatal("cleanup within grace must keep the agent healthy")
	}
	h.stop(t)
}

func TestFourWorkerLimit(t *testing.T) {
	release := make(chan struct{})
	shared := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return model.CheckResult{Success: true}
	}}
	var entries []Entry
	for i := 0; i < 7; i++ {
		entries = append(entries, entry(shared, fmt.Sprintf("c%d", i), time.Minute, 30*time.Second))
	}
	h := start(t, nil, entries...)

	eventually(t, "four running", func() bool { return shared.active.Load() == Workers })
	never(t, "more than four concurrent", func() bool { return shared.active.Load() > Workers })
	if shared.runs.Load() != Workers {
		t.Fatalf("runs=%d, want %d while blocked", shared.runs.Load(), Workers)
	}
	close(release)
	eventually(t, "all seven ran", func() bool { return h.rec.count("") == 7 })
	if shared.maxSeen.Load() != Workers {
		t.Fatalf("max concurrency %d, want %d", shared.maxSeen.Load(), Workers)
	}
	h.stop(t)
}

func TestSingleDeadlineAndExactlyOneTimeoutResult(t *testing.T) {
	var d1, d2 time.Time
	var hasDeadline bool
	f := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		d1, hasDeadline = ctx.Deadline()
		<-ctx.Done()
		d2, _ = ctx.Deadline()
		// A cooperative adapter reports its own failure after cancellation.
		return model.CheckResult{Success: false, ReasonCode: "ctx", Reason: ctx.Err().Error()}
	}}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "running", func() bool { return f.active.Load() == 1 })
	h.waitTimer(t, h.t0, 10*time.Second)

	h.clock.Advance(10 * time.Second)
	eventually(t, "timeout observation", func() bool { return h.rec.count("a") == 1 })
	eventually(t, "adapter returned", func() bool { return f.active.Load() == 0 })
	never(t, "second observation for same execution", func() bool { return h.rec.count("a") > 1 })

	res := h.rec.all()[0]
	if res.Success || res.ReasonCode != ReasonTimeout {
		t.Fatalf("want failed timeout result, got %+v", res)
	}
	if res.Duration != 10*time.Second || res.CompletedAt.Sub(res.StartedAt) != 10*time.Second {
		t.Fatalf("duration %v / %v", res.Duration, res.CompletedAt.Sub(res.StartedAt))
	}
	if !hasDeadline || !d1.Equal(d2) {
		t.Fatalf("deadline must be set once and never reset: %v vs %v (set=%v)", d1, d2, hasDeadline)
	}
	if !h.s.Healthy() {
		t.Fatal("cooperative cleanup must not make the scheduler unhealthy")
	}
	h.stop(t)
}

func TestLateAdapterResultAfterDeadlineIsDiscarded(t *testing.T) {
	release := make(chan struct{})
	f := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		<-release // ignores ctx for a moment, then claims success
		return model.CheckResult{Success: true, ReasonCode: "ok"}
	}}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "running", func() bool { return f.active.Load() == 1 })
	h.waitTimer(t, h.t0, 10*time.Second)
	h.clock.Advance(10 * time.Second)
	eventually(t, "timeout observation", func() bool { return h.rec.count("a") == 1 })
	h.clock.Advance(time.Second) // within cleanup grace
	close(release)
	eventually(t, "adapter returned", func() bool { return f.active.Load() == 0 })
	never(t, "extra observation", func() bool { return h.rec.count("a") != 1 })
	if r := h.rec.all()[0]; r.Success {
		t.Fatalf("late success must not replace timeout: %+v", r)
	}
	if !h.s.Healthy() {
		t.Fatal("cleanup inside grace must keep the agent healthy")
	}
	h.stop(t)
}

func TestCleanupGraceExceededMakesAgentUnhealthy(t *testing.T) {
	release := make(chan struct{})
	f := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		<-release // never cleans up
		return model.CheckResult{}
	}}
	h := start(t, nil, entry(f, "stuck", time.Minute, 10*time.Second))
	eventually(t, "running", func() bool { return f.active.Load() == 1 })
	h.waitTimer(t, h.t0, 10*time.Second)
	h.clock.Advance(10 * time.Second)
	eventually(t, "timeout observation", func() bool { return h.rec.count("stuck") == 1 })
	h.waitTimer(t, h.t0, 10*time.Second+CleanupGrace)

	h.clock.Advance(CleanupGrace - time.Millisecond)
	select {
	case err := <-h.done:
		t.Fatalf("exited before grace elapsed: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	h.clock.Advance(time.Millisecond)
	select {
	case err := <-h.done:
		if !errors.Is(err, ErrCleanupTimeout) {
			t.Fatalf("got %v, want ErrCleanupTimeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not exit after grace period")
	}
	if h.s.Healthy() {
		t.Fatal("scheduler must be unhealthy")
	}
	close(release)
}

func TestShutdownAbortsRunWithoutObservation(t *testing.T) {
	f := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		<-ctx.Done()
		return model.CheckResult{Success: false, ReasonCode: "ctx", Reason: "canceled"}
	}}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "running", func() bool { return f.active.Load() == 1 })
	if err := h.stop(t); err != nil {
		t.Fatalf("clean shutdown returned %v", err)
	}
	if n := h.rec.count(""); n != 0 {
		t.Fatalf("shutdown fabricated %d observations", n)
	}
	if f.closed.Load() != 1 {
		t.Fatalf("adapter closed %d times, want 1", f.closed.Load())
	}
}

func TestShutdownWithStuckAdapterReportsCleanupTimeout(t *testing.T) {
	release := make(chan struct{})
	f := &fakeCheck{typ: "fake", run: func(ctx context.Context, _ model.CheckConfig) model.CheckResult {
		<-release
		return model.CheckResult{}
	}}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "running", func() bool { return f.active.Load() == 1 })
	h.cancel()
	h.waitTimer(t, h.t0, CleanupGrace)
	h.clock.Advance(CleanupGrace)
	select {
	case err := <-h.done:
		if !errors.Is(err, ErrCleanupTimeout) {
			t.Fatalf("got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no exit")
	}
	close(release)
}

func TestAdapterPanicBecomesOneFailure(t *testing.T) {
	f := &fakeCheck{typ: "fake", run: func(context.Context, model.CheckConfig) model.CheckResult { panic("boom") }}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "observation", func() bool { return h.rec.count("a") == 1 })
	if r := h.rec.all()[0]; r.Success || r.ReasonCode != ReasonAdapterPanic {
		t.Fatalf("got %+v", r)
	}
	h.stop(t)
}

func TestResultNormalization(t *testing.T) {
	f := &fakeCheck{typ: "fake", run: func(context.Context, model.CheckConfig) model.CheckResult {
		return model.CheckResult{CheckID: "wrong", CheckType: "wrong", Success: true, StartedAt: time.Unix(1, 0)}
	}}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "observation", func() bool { return h.rec.count("a") == 1 })
	r := h.rec.all()[0]
	if r.CheckID != "a" || r.CheckType != "fake" || r.StartedAt.Location() != time.UTC || r.CompletedAt.Location() != time.UTC {
		t.Fatalf("not normalized: %+v", r)
	}
	h.stop(t)
}

type fakePruner struct{ calls atomic.Int32 }

func (p *fakePruner) Prune(context.Context, time.Time) (model.PruneResult, error) {
	p.calls.Add(1)
	return model.PruneResult{}, nil
}

func TestPruneOncePerHour(t *testing.T) {
	p := &fakePruner{}
	f := &fakeCheck{typ: "fake"}
	h := start(t, p, entry(f, "a", 30*time.Minute, 10*time.Second))
	eventually(t, "startup prune", func() bool { return p.calls.Load() == 1 })
	h.waitTimer(t, h.t0, time.Hour)
	h.waitTimer(t, h.t0, 30*time.Minute)
	h.clock.Advance(30 * time.Minute)
	never(t, "early prune", func() bool { return p.calls.Load() > 1 })
	h.clock.Advance(30 * time.Minute)
	eventually(t, "hourly prune", func() bool { return p.calls.Load() == 2 })
	h.waitTimer(t, h.t0, 2*time.Hour)
	h.clock.Advance(time.Hour)
	eventually(t, "second hourly prune", func() bool { return p.calls.Load() == 3 })
	h.stop(t)
}

func TestRestartDoesNotFabricateResults(t *testing.T) {
	f := &fakeCheck{typ: "fake"}
	h := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "run", func() bool { return h.rec.count("a") == 1 })
	h.stop(t)

	// "Downtime": a new scheduler records only what it actually runs.
	h2 := start(t, nil, entry(f, "a", time.Minute, 10*time.Second))
	eventually(t, "run after restart", func() bool { return h2.rec.count("a") == 1 })
	never(t, "extra results", func() bool { return h2.rec.count("a") > 1 })
	h2.stop(t)
}

func TestOnceRunsEveryCheckExactlyOnce(t *testing.T) {
	var entries []Entry
	var checks []*fakeCheck
	for i := 0; i < 6; i++ {
		f := &fakeCheck{typ: "fake"}
		checks = append(checks, f)
		entries = append(entries, entry(f, fmt.Sprintf("c%d", i), time.Minute, 10*time.Second))
	}
	clk, rec := newTestClock(), &recorder{}
	s, err := New(Options{Clock: clk, Observer: rec}, entries)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Once(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rec.count("") != 6 {
		t.Fatalf("got %d observations", rec.count(""))
	}
	for _, f := range checks {
		if f.runs.Load() != 1 || f.closed.Load() != 1 {
			t.Fatalf("runs=%d closed=%d", f.runs.Load(), f.closed.Load())
		}
	}
	if err := s.Once(context.Background()); err == nil {
		t.Fatal("scheduler reuse must fail")
	}
}

func TestSharedAdapterClosedOnce(t *testing.T) {
	f := &fakeCheck{typ: "fake"}
	h := start(t, nil, entry(f, "a", time.Minute, time.Second), entry(f, "b", time.Minute, time.Second))
	eventually(t, "runs", func() bool { return h.rec.count("") == 2 })
	h.stop(t)
	if f.closed.Load() != 1 {
		t.Fatalf("closed %d times", f.closed.Load())
	}
}

// ---- config resolution ---------------------------------------------------

func TestEntriesFromConfig(t *testing.T) {
	reg := NewRegistry()
	for _, typ := range []string{"systemd", "http"} {
		if err := reg.Register(typ, &fakeCheck{typ: typ}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Register("http", &fakeCheck{}); err == nil {
		t.Fatal("duplicate registration accepted")
	}

	c := &config.Config{Checks: []config.CheckConfig{
		{Type: "systemd", CheckID: "default", Target: "multi-user.target"},
		{Type: "http", CheckID: "override", Interval: "2m", Timeout: "20s", URL: "https://example.org/", BodyContains: "ok"},
		{Type: "http", CheckID: "global-override"},
	}}
	// Agent defaults unset -> 30m / 10s.
	got, err := EntriesFromConfig(c, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Interval != 30*time.Minute || got[0].Timeout != 10*time.Second {
		t.Fatalf("defaults: %v %v", got[0].Interval, got[0].Timeout)
	}
	if sp, ok := got[0].Config.(*Spec); !ok || sp.Target != "multi-user.target" {
		t.Fatalf("systemd target not wired into spec: %+v", got[0].Config)
	}
	if sp, ok := got[1].Config.(*Spec); !ok || sp.URL != "https://example.org/" || sp.BodyContains != "ok" || sp.ExpectedStatus != 0 {
		t.Fatalf("http fields not wired into spec: %+v", got[1].Config)
	}
	if got[1].Interval != 2*time.Minute || got[1].Timeout != 20*time.Second {
		t.Fatalf("override: %v %v", got[1].Interval, got[1].Timeout)
	}
	if got[1].Config.CheckID() != "override" || got[1].Config.CheckType() != "http" {
		t.Fatalf("config: %+v", got[1].Config)
	}

	c.Agent.Interval, c.Agent.Timeout = 5*time.Minute, 15*time.Second
	got, err = EntriesFromConfig(c, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Interval != 5*time.Minute || got[2].Timeout != 15*time.Second || got[1].Interval != 2*time.Minute {
		t.Fatalf("agent-level values not applied: %+v", got)
	}

	bad := []config.CheckConfig{
		{Type: "http", CheckID: "x", Interval: "1m", Timeout: "1m"},
		{Type: "http", CheckID: "x", Interval: "30s"},
		{Type: "http", CheckID: "x", Timeout: "301s"},
		{Type: "http", CheckID: "x", Interval: "8d"},
		{Type: "http", CheckID: "x", Interval: "abc"},
		{Type: "ping", CheckID: "x"},
		{Type: "http", CheckID: "x", Interval: "1m"}, // agent timeout 15s fine; below: timeout>=interval
	}
	c.Agent.Timeout = 10 * time.Second
	for i, ch := range bad[:6] {
		c.Checks = []config.CheckConfig{ch}
		if _, err := EntriesFromConfig(c, reg); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
	// Per-check interval shorter than the inherited timeout is rejected.
	c.Agent.Timeout = 90 * time.Second
	c.Checks = []config.CheckConfig{bad[6]}
	if _, err := EntriesFromConfig(c, reg); err == nil {
		t.Error("inherited timeout >= overridden interval accepted")
	}
}

func TestEntriesFromConfigWiresMySQL(t *testing.T) {
	reg := NewRegistry()
	if err := reg.Register("mysql", &fakeCheck{typ: "mysql"}); err != nil {
		t.Fatal(err)
	}
	c := &config.Config{Checks: []config.CheckConfig{
		{Type: "mysql", CheckID: "db", Host: "db.example.org", Port: 3307, Database: "app",
			CredentialFile: "/etc/vshm/secrets/db.ini", TLSCA: "/etc/ssl/certs/ca.crt", Query: "SELECT 1;"},
		{Type: "mysql", CheckID: "db-local", Socket: "/run/mysqld/mysqld.sock",
			CredentialFile: "/etc/vshm/secrets/local.ini", Query: "SELECT 1"},
	}}
	got, err := EntriesFromConfig(c, reg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 entries, got %d", len(got))
	}
	sp, ok := got[0].Config.(*Spec)
	if !ok || sp.MySQL == nil {
		t.Fatalf("mysql spec not wired: %+v", got[0].Config)
	}
	m := sp.MySQL
	if m.Host != "db.example.org" || m.Port != 3307 || m.Database != "app" ||
		m.CredentialFile != "/etc/vshm/secrets/db.ini" || m.TLSCA != "/etc/ssl/certs/ca.crt" || m.Query != "SELECT 1;" {
		t.Errorf("mysql fields wrong: %+v", m)
	}
	sp2 := got[1].Config.(*Spec)
	if sp2.MySQL == nil || sp2.MySQL.Socket != "/run/mysqld/mysqld.sock" || sp2.MySQL.Host != "" || sp2.MySQL.TLSCA != "" {
		t.Errorf("socket mysql fields wrong: %+v", sp2.MySQL)
	}
	// non-mysql specs must have nil MySQL
	c.Checks = append(c.Checks, config.CheckConfig{Type: "mysql", CheckID: "x"})
	c.Checks = c.Checks[:2]
	c.Checks = append([]config.CheckConfig{{Type: "http", CheckID: "h", URL: "https://example.org/"}}, c.Checks...)
	if err := reg.Register("http", &fakeCheck{typ: "http"}); err != nil {
		t.Fatal(err)
	}
	got, err = EntriesFromConfig(c, reg)
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Config.(*Spec).MySQL != nil {
		t.Error("http spec must not carry mysql fields")
	}
}
