package scheduler

import (
	"context"
	"errors"
	"os/exec"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) NewTimer(d time.Duration) model.Timer {
	return &realTestTimer{time.NewTimer(d)}
}

type realTestTimer struct{ t *time.Timer }

func (r *realTestTimer) C() <-chan time.Time        { return r.t.C }
func (r *realTestTimer) Stop() bool                 { return r.t.Stop() }
func (r *realTestTimer) Reset(d time.Duration) bool { return r.t.Reset(d) }

// procAdapter follows the adapter contract for subprocess work: the child gets its
// own process group, which is killed when the scheduler's context is done.
type procAdapter struct {
	pgid atomic.Int64
}

func (p *procAdapter) Type() string { return "proc" }

func (p *procAdapter) Run(ctx context.Context, _ model.CheckConfig) model.CheckResult {
	cmd := exec.Command("sh", "-c", "sleep 300 & sleep 300 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return model.CheckResult{Reason: err.Error()}
	}
	p.pgid.Store(int64(cmd.Process.Pid))
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	select {
	case err := <-waited:
		return model.CheckResult{Success: err == nil}
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-waited
		return model.CheckResult{Reason: "cancelled"}
	}
}

func TestRealClockDeadlineKillsProcessGroup(t *testing.T) {
	a := &procAdapter{}
	rec := &recorder{}
	s, err := New(Options{Clock: realClock{}, Observer: rec}, []Entry{
		{Check: a, Config: cfg{id: "p", typ: "proc"}, Interval: time.Minute, Timeout: 300 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	eventually(t, "timeout observation", func() bool { return rec.count("p") == 1 })
	res := rec.all()[0]
	if res.Success || res.ReasonCode != ReasonTimeout {
		t.Fatalf("got %+v", res)
	}
	if res.Duration < 300*time.Millisecond || res.Duration > CleanupGrace {
		t.Fatalf("duration %v not within deadline..deadline+grace", res.Duration)
	}
	pgid := int(a.pgid.Load())
	eventually(t, "process group gone", func() bool {
		return errors.Is(syscall.Kill(-pgid, 0), syscall.ESRCH)
	})

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("scheduler did not stop")
	}
	if !s.Healthy() {
		t.Fatal("unhealthy")
	}
}
