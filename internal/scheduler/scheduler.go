// Package scheduler runs configured checks on a fixed worker pool.
//
// Design decisions (WP-03):
//   - Cadence is anchored to the first dispatch (all checks run immediately at
//     start) and advances on a fixed grid of start+n*interval, so a slow run never
//     shifts later runs. Ticks missed while the process was stalled are not
//     replayed, and a tick that is due while the same check is still queued or
//     running is skipped and logged (no fabricated failure).
//   - Four workers run distinct checks concurrently; one check never overlaps itself.
//   - Exactly one absolute deadline is created when a worker starts executing a
//     check. The same context is passed to the adapter, so adapters must derive
//     DNS/connect/I/O/query/subprocess limits from it and never reset it.
//   - Cleanup ownership: the adapter owns the resources it opens during Run
//     (connections, subprocess process groups) and must release them when the
//     context is done, before Run returns. The scheduler treats "Run returned" as
//     "cleanup finished" and allows at most CleanupGrace after the deadline (or
//     shutdown) for that. If Run has not returned by then the scheduler is marked
//     unhealthy and Run/Once return ErrCleanupTimeout so the process exits
//     nonzero and systemd restarts it (the cgroup kill removes any leftover child).
//   - Adapters that also implement io.Closer (and are comparable, i.e. pointer
//     types) are closed once by the scheduler after all workers have stopped.
//   - Results are handed to an Observer (the incident service, WP-10); the
//     scheduler never writes to the store or outbox itself, except for Prune.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/logging"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Fixed scheduler parameters from the specification.
const (
	Workers       = 4
	CleanupGrace  = 2 * time.Second
	PruneInterval = time.Hour
)

// Reason codes of observations produced by the scheduler itself.
const (
	ReasonTimeout      = "timeout"
	ReasonAdapterPanic = "adapter_panic"
)

// ErrCleanupTimeout is returned when a check did not release its resources within CleanupGrace.
var ErrCleanupTimeout = errors.New("scheduler: check cleanup exceeded grace period; agent unhealthy")

// Entry is one scheduled check with its resolved limits.
type Entry struct {
	Check    model.Check
	Config   model.CheckConfig
	Interval time.Duration
	Timeout  time.Duration
}

// Observer receives every observation of an execution that actually ran.
type Observer interface {
	Observe(ctx context.Context, result model.CheckResult) error
}

// Pruner is the part of model.Store the scheduler needs; it is called once per hour.
type Pruner interface {
	Prune(ctx context.Context, now time.Time) (model.PruneResult, error)
}

// Logger is the logging surface used by the scheduler (satisfied by *logging.Logger).
type Logger interface {
	Printf(format string, v ...any)
}

// Options configures a Scheduler. Clock and Observer are required.
type Options struct {
	Clock    model.Clock
	Observer Observer
	Pruner   Pruner // optional
	Logger   Logger // optional
}

type checkState struct {
	entry   Entry
	next    time.Time
	running atomic.Bool // set from enqueue until the execution (queued or running) finished
}

// Scheduler executes checks. It can be used for exactly one Run or Once call.
type Scheduler struct {
	clock    model.Clock
	observer Observer
	pruner   Pruner
	log      Logger

	states  []*checkState
	jobs    chan *checkState
	pending sync.WaitGroup // jobs enqueued but not yet finished
	fatal   chan error
	started atomic.Bool
	healthy atomic.Bool
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// New validates the entries and creates a scheduler.
func New(opts Options, entries []Entry) (*Scheduler, error) {
	if opts.Clock == nil {
		return nil, errors.New("scheduler: clock is required")
	}
	if opts.Observer == nil {
		return nil, errors.New("scheduler: observer is required")
	}
	s := &Scheduler{
		clock:    opts.Clock,
		observer: opts.Observer,
		pruner:   opts.Pruner,
		log:      opts.Logger,
		states:   make([]*checkState, 0, len(entries)),
		jobs:     make(chan *checkState, len(entries)),
		fatal:    make(chan error, 1),
	}
	if s.log == nil {
		s.log = discardLogger{}
	}
	s.healthy.Store(true)

	seen := make(map[string]bool, len(entries))
	for i, e := range entries {
		if e.Check == nil || e.Config == nil {
			return nil, fmt.Errorf("scheduler: entry %d: check and config are required", i)
		}
		id := e.Config.CheckID()
		if seen[id] {
			return nil, fmt.Errorf("scheduler: duplicate check id %q", id)
		}
		seen[id] = true
		if e.Interval <= 0 || e.Timeout <= 0 || e.Timeout >= e.Interval {
			return nil, fmt.Errorf("scheduler: check %q: need 0 < timeout (%v) < interval (%v)", id, e.Timeout, e.Interval)
		}
		s.states = append(s.states, &checkState{entry: e})
	}
	return s, nil
}

// Healthy reports whether the scheduler is free of unrecovered cleanup failures.
func (s *Scheduler) Healthy() bool { return s.healthy.Load() }

// Run schedules checks until ctx is cancelled (returns nil) or a cleanup failure
// makes the agent unhealthy (returns ErrCleanupTimeout).
func (s *Scheduler) Run(ctx context.Context) error { return s.run(ctx, false) }

// Once executes every check exactly once on the worker pool and returns when all finished.
func (s *Scheduler) Once(ctx context.Context) error { return s.run(ctx, true) }

func (s *Scheduler) run(ctx context.Context, once bool) error {
	if !s.started.CompareAndSwap(false, true) {
		return errors.New("scheduler: already started")
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	for i := 0; i < Workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(runCtx)
		}()
	}

	onceDone := make(chan struct{})
	if once {
		now := s.clock.Now()
		for _, st := range s.states {
			s.enqueue(st, now)
		}
		go func() {
			s.pending.Wait()
			close(onceDone)
		}()
	} else {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.dispatch(runCtx)
		}()
		if s.pruner != nil {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.pruneLoop(runCtx)
			}()
		}
	}

	select {
	case <-ctx.Done():
	case <-onceDone:
	case err := <-s.fatal:
		// Return immediately: the process exits nonzero and leftover goroutines die with it.
		return err
	}
	cancel()

	stopped := make(chan struct{})
	go func() {
		wg.Wait()
		close(stopped)
	}()
	grace := s.clock.NewTimer(CleanupGrace)
	defer grace.Stop()
	select {
	case <-stopped:
	case err := <-s.fatal:
		return err
	case <-grace.C():
		s.log.Printf("scheduler: workers did not stop within %v of shutdown", CleanupGrace)
		s.healthy.Store(false)
		return ErrCleanupTimeout
	}

	s.closeAdapters()
	return nil
}

// fail marks the scheduler unhealthy; the first failure is reported by run.
func (s *Scheduler) fail(err error) {
	s.healthy.Store(false)
	select {
	case s.fatal <- err:
	default:
	}
}

// dispatch hands due checks to the workers on the absolute grid start+n*interval.
func (s *Scheduler) dispatch(ctx context.Context) {
	now := s.clock.Now()
	for _, st := range s.states {
		st.next = now // every check runs promptly after startup
	}
	for {
		now = s.clock.Now()
		var wake time.Time
		for _, st := range s.states {
			if !st.next.After(now) {
				s.enqueue(st, now)
				// Jump past now: missed ticks are skipped, never replayed.
				iv := st.entry.Interval
				st.next = st.next.Add(iv * ((now.Sub(st.next) / iv) + 1))
			}
			if wake.IsZero() || st.next.Before(wake) {
				wake = st.next
			}
		}
		if wake.IsZero() { // no checks configured
			<-ctx.Done()
			return
		}
		t := s.clock.NewTimer(wake.Sub(now))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}

// enqueue queues a due check unless its previous execution is still queued or running.
func (s *Scheduler) enqueue(st *checkState, now time.Time) {
	id := st.entry.Config.CheckID()
	if !st.running.CompareAndSwap(false, true) {
		s.log.Printf("scheduler: skipping due run of check %s: previous execution still active (due %s)",
			logging.Sanitize(id), now.UTC().Format(time.RFC3339))
		return
	}
	s.pending.Add(1)
	s.jobs <- st // capacity == number of checks and each check is queued at most once
}

func (s *Scheduler) worker(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case st := <-s.jobs:
			s.execute(ctx, st)
			st.running.Store(false)
			s.pending.Done()
		}
	}
}

// pruneLoop calls Store.Prune at startup and then once per hour.
func (s *Scheduler) pruneLoop(ctx context.Context) {
	for {
		if res, err := s.pruner.Prune(ctx, s.clock.Now().UTC()); err != nil {
			if ctx.Err() == nil {
				s.log.Printf("scheduler: prune failed: %s", logging.Sanitize(err.Error()))
			}
		} else {
			s.log.Printf("scheduler: pruned observations=%d outbox=%d", res.Observations, res.OutboxRows)
		}
		t := s.clock.NewTimer(PruneInterval)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C():
		}
	}
}

// execute runs one check under a single absolute deadline and emits exactly one
// observation unless the execution was interrupted by shutdown.
func (s *Scheduler) execute(parent context.Context, st *checkState) {
	e := st.entry
	id := e.Config.CheckID()

	started := s.clock.Now()
	// The one deadline for the whole execution. The clock timer is the
	// authoritative trigger; the context deadline lets adapters see it.
	ctx, cancel := context.WithDeadline(parent, time.Now().Add(e.Timeout))
	defer cancel()
	deadline := s.clock.NewTimer(e.Timeout)
	defer deadline.Stop()

	done := make(chan model.CheckResult, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- model.CheckResult{
					Success:    false,
					ReasonCode: ReasonAdapterPanic,
					Reason:     "check adapter panicked",
				}
			}
		}()
		done <- e.Check.Run(ctx, e.Config)
	}()

	select {
	case res := <-done:
		switch {
		case parent.Err() != nil:
			s.log.Printf("scheduler: check %s aborted by shutdown; no observation recorded", logging.Sanitize(id))
		case ctx.Err() != nil || timerFired(deadline):
			// Adapter returned at/after the deadline: the timeout result wins, once.
			s.emit(parent, s.timeoutResult(e, started))
		default:
			s.emit(parent, s.finish(e, started, res))
		}
	case <-deadline.C():
		// Start the cleanup grace before anything else so recording cannot eat into it.
		grace := s.clock.NewTimer(CleanupGrace)
		defer grace.Stop()
		cancel()
		s.emit(parent, s.timeoutResult(e, started))
		select {
		case <-done: // adapter released its connections / process group; late result is discarded
		case <-grace.C():
			s.log.Printf("scheduler: check %s did not finish cleanup within %v after its deadline", logging.Sanitize(id), CleanupGrace)
			s.fail(ErrCleanupTimeout)
		}
	case <-parent.Done():
		cancel()
		<-done // run() bounds this wait with the shutdown grace
		s.log.Printf("scheduler: check %s aborted by shutdown; no observation recorded", logging.Sanitize(id))
	}
}

func timerFired(t model.Timer) bool {
	select {
	case <-t.C():
		return true
	default:
		return false
	}
}

// finish normalizes an adapter result: identity from the config, UTC timestamps and
// a duration measured by the scheduler around the execution.
func (s *Scheduler) finish(e Entry, started time.Time, res model.CheckResult) model.CheckResult {
	completed := s.clock.Now()
	res.CheckID = e.Config.CheckID()
	res.CheckType = e.Config.CheckType()
	res.Duration = completed.Sub(started)
	res.StartedAt = started.UTC()
	res.CompletedAt = completed.UTC()
	res.Reason = logging.Sanitize(res.Reason)
	return res
}

func (s *Scheduler) timeoutResult(e Entry, started time.Time) model.CheckResult {
	return s.finish(e, started, model.CheckResult{
		Success:    false,
		ReasonCode: ReasonTimeout,
		Reason:     fmt.Sprintf("check exceeded timeout of %v", e.Timeout),
	})
}

// emit hands a result to the observer. It is detached from shutdown cancellation
// so a completed execution is still recorded, but bounded by the cleanup grace.
func (s *Scheduler) emit(parent context.Context, res model.CheckResult) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), CleanupGrace)
	defer cancel()
	if err := s.observer.Observe(ctx, res); err != nil {
		s.log.Printf("scheduler: recording observation of check %s failed: %s",
			logging.Sanitize(res.CheckID), logging.Sanitize(err.Error()))
	}
}

// closeAdapters closes each distinct adapter that implements io.Closer.
func (s *Scheduler) closeAdapters() {
	closed := make(map[model.Check]bool)
	for _, st := range s.states {
		c, ok := st.entry.Check.(io.Closer)
		if !ok || closed[st.entry.Check] {
			continue
		}
		closed[st.entry.Check] = true
		if err := c.Close(); err != nil {
			s.log.Printf("scheduler: closing %s adapter: %s", logging.Sanitize(st.entry.Config.CheckType()), logging.Sanitize(err.Error()))
		}
	}
}
