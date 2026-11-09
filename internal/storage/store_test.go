package storage

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/model/fake"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newClock() *fake.Clock {
	c := fake.NewClock()
	c.SetNow(t0)
	return c
}

func openTest(t *testing.T, dir string, opts Options) *SQLiteStore {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(dir, "vshm.db"), newClock(), opts)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func count(t *testing.T, s *SQLiteStore, query string, args ...any) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatalf("count %q: %v", query, err)
	}
	return n
}

func event(id string, at time.Time) model.OutboxEvent {
	return model.OutboxEvent{ID: id, Kind: "event", EnqueuedAt: at, Payload: "body " + id}
}

func result(checkID string, ok bool, at time.Time) model.CheckResult {
	return model.CheckResult{CheckID: checkID, CheckType: "http", Success: ok, StartedAt: at.Add(-time.Second),
		CompletedAt: at, Duration: time.Second, ReasonCode: "rc", Reason: "reason"}
}

func enqueueEvents(t *testing.T, s *SQLiteStore, evs ...model.OutboxEvent) {
	t.Helper()
	for _, ev := range evs {
		if err := s.RecordObservationAndIncident(context.Background(), result("c", false, ev.EnqueuedAt),
			model.IncidentMutation{CheckID: "c", Open: true}, []model.OutboxEvent{ev}); err != nil {
			t.Fatalf("enqueue %s: %v", ev.ID, err)
		}
	}
}

func status(t *testing.T, s *SQLiteStore, eventID string) (string, int, string) {
	t.Helper()
	var st, reason string
	var attempts int
	if err := s.db.QueryRow(`SELECT status, attempts, last_reason FROM outbox WHERE event_id = ?`, eventID).
		Scan(&st, &attempts, &reason); err != nil {
		t.Fatalf("status %s: %v", eventID, err)
	}
	return st, attempts, reason
}

func TestOptionsWithDefaults(t *testing.T) {
	got := (Options{}).WithDefaults()
	want := Options{ObservationRetention: 90 * 24 * time.Hour, DeliveredRetention: 30 * 24 * time.Hour,
		PendingMaxAge: 24 * time.Hour, MaxPending: 1000}
	if got != want {
		t.Fatalf("WithDefaults() = %+v, want %+v", got, want)
	}
}

// 1. Schema, file modes, lock.
func TestOpenSchemaPermissionsAndLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vshm.db")
	s, err := Open(context.Background(), path, newClock(), Options{})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var v string
	if err := s.db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&v); err != nil || v != "1" {
		t.Fatalf("schema_version = %q, %v", v, err)
	}
	for _, p := range []string{path, filepath.Join(dir, LockFileName)} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 600", p, info.Mode().Perm())
		}
	}

	if s2, err := Open(context.Background(), path, newClock(), Options{}); !errors.Is(err, ErrAlreadyRunning) || s2 != nil {
		t.Fatalf("second Open = %v, %v; want ErrAlreadyRunning", s2, err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	s3, err := Open(context.Background(), path, newClock(), Options{})
	if err != nil {
		t.Fatalf("Open after Close: %v", err)
	}
	_ = s3.Close()
}

func TestOpenRejectsUnknownSchemaVersion(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir, Options{})
	if _, err := s.db.Exec(`UPDATE meta SET value = '2' WHERE key = 'schema_version'`); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	if _, err := Open(context.Background(), filepath.Join(dir, "vshm.db"), newClock(), Options{}); err == nil {
		t.Fatal("Open with schema_version 2 succeeded")
	}
	// The lock must have been released by the failed Open.
	f, err := acquireLock(filepath.Join(dir, LockFileName))
	if err != nil {
		t.Fatalf("lock still held after failed Open: %v", err)
	}
	_ = f.Close()
}

// 2. Atomicity.
func TestRecordObservationAndIncidentAtomic(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	evs := []model.OutboxEvent{event("dup", t0), event("dup", t0)}
	err := s.RecordObservationAndIncident(context.Background(), result("c", false, t0),
		model.IncidentMutation{CheckID: "c", Open: true, LastFailure: t0}, evs)
	if err == nil {
		t.Fatal("expected duplicate event_id error")
	}
	for _, table := range []string{"observations", "incidents", "outbox"} {
		if n := count(t, s, "SELECT COUNT(*) FROM "+table); n != 0 {
			t.Errorf("%s rows = %d, want 0", table, n)
		}
	}
}

func TestIncidentRowTracksState(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	ctx := context.Background()
	open := model.IncidentMutation{CheckID: "c", Open: true, LastFailure: t0}
	if err := s.RecordObservationAndIncident(ctx, result("c", false, t0), open, nil); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordObservationAndIncident(ctx, result("c", false, t0.Add(time.Minute)), open, nil); err != nil {
		t.Fatal(err)
	}
	var state, since string
	var failures int
	if err := s.db.QueryRow(`SELECT state, since, consecutive_failures FROM incidents WHERE check_id='c'`).Scan(&state, &since, &failures); err != nil {
		t.Fatal(err)
	}
	if state != "open" || since != formatTime(t0) || failures != 2 {
		t.Fatalf("incident = %s %s %d", state, since, failures)
	}
	resolved := t0.Add(2 * time.Minute)
	if err := s.RecordObservationAndIncident(ctx, result("c", true, resolved),
		model.IncidentMutation{CheckID: "c", ResolvedAt: &resolved}, nil); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Snapshot(ctx, resolved)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Incidents) != 1 || snap.Incidents[0].ResolvedAt == nil || !snap.Incidents[0].ResolvedAt.Equal(resolved) {
		t.Fatalf("incidents = %+v", snap.Incidents)
	}
	if len(snap.Checks) != 1 || snap.Checks[0].LastStatus != "success" || !snap.Checks[0].LastCompleted.Equal(resolved) ||
		snap.Checks[0].Successes24h != 1 || snap.Checks[0].Failures24h != 2 {
		t.Fatalf("checks = %+v", snap.Checks)
	}
}

// 3. Report dedupe.
func TestEnqueueReportDedupe(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	ctx := context.Background()
	ins, err := s.EnqueueReport(ctx, "2026-03-01", model.OutboxEvent{ID: "r1", EnqueuedAt: t0, Payload: "r"})
	if err != nil || !ins {
		t.Fatalf("first EnqueueReport = %v, %v", ins, err)
	}
	ins, err = s.EnqueueReport(ctx, "2026-03-01", model.OutboxEvent{ID: "r2", EnqueuedAt: t0, Payload: "r"})
	if err != nil || ins {
		t.Fatalf("second EnqueueReport = %v, %v", ins, err)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM outbox WHERE kind='report'`); n != 1 {
		t.Fatalf("report rows = %d", n)
	}
}

// 4. Expiry, ordering, next_attempt_at.
func TestPending(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	ctx := context.Background()
	now := t0.Add(25 * time.Hour)
	enqueueEvents(t, s, event("old", t0), event("a", now.Add(-time.Hour)), event("b", now.Add(-30*time.Minute)), event("later", now.Add(-10*time.Minute)))
	if err := s.MarkDelivery(ctx, "later", model.DeliveryFailure, now, now.Add(time.Hour), "retry"); err != nil {
		t.Fatal(err)
	}
	got, err := s.Pending(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].ID != "a" || got[1].ID != "b" || got[0].Sequence >= got[1].Sequence {
		t.Fatalf("pending = %+v", got)
	}
	if got[0].Payload != "body a" || got[0].DeliveryState != model.StatePending {
		t.Fatalf("event fields = %+v", got[0])
	}
	if st, _, _ := status(t, s, "old"); st != "expired" {
		t.Fatalf("old status = %s", st)
	}
	if got, _ := s.Pending(ctx, now, 1); len(got) != 1 || got[0].ID != "a" {
		t.Fatalf("limit 1 = %+v", got)
	}
}

// 5. MarkDelivery transitions.
func TestMarkDelivery(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	ctx := context.Background()
	enqueueEvents(t, s, event("ok", t0), event("retry", t0), event("giveup", t0))

	if err := s.MarkDelivery(ctx, "retry", model.DeliveryFailure, t0, t0.Add(time.Minute), "send_failed"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivery(ctx, "retry", model.DeliveryAmbiguous, t0.Add(time.Minute), t0.Add(3*time.Minute), "ambiguous"); err != nil {
		t.Fatal(err)
	}
	if st, n, r := status(t, s, "retry"); st != "pending" || n != 2 || r != "ambiguous" {
		t.Fatalf("retry = %s %d %s", st, n, r)
	}
	var next string
	_ = s.db.QueryRow(`SELECT next_attempt_at FROM outbox WHERE event_id='retry'`).Scan(&next)
	if next != formatTime(t0.Add(3*time.Minute)) {
		t.Fatalf("next_attempt_at = %s", next)
	}

	if err := s.MarkDelivery(ctx, "ok", model.DeliverySuccess, t0, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	if st, n, _ := status(t, s, "ok"); st != "delivered" || n != 1 {
		t.Fatalf("ok = %s %d", st, n)
	}
	if err := s.MarkDelivery(ctx, "giveup", model.DeliveryEvicted, t0, time.Time{}, "gave_up"); err != nil {
		t.Fatal(err)
	}
	if st, _, r := status(t, s, "giveup"); st != "dropped" || r != "gave_up" {
		t.Fatalf("giveup = %s %s", st, r)
	}
	if err := s.MarkDelivery(ctx, "missing", model.DeliverySuccess, t0, t0, ""); err == nil {
		t.Fatal("MarkDelivery on unknown event succeeded")
	}
	if err := s.MarkDelivery(ctx, "ok", model.DeliveryOutcome("bogus"), t0, t0, ""); err == nil {
		t.Fatal("MarkDelivery with unknown outcome succeeded")
	}
}

// 6. Queue bound.
func TestQueueBound(t *testing.T) {
	const max = 10
	s := openTest(t, t.TempDir(), Options{MaxPending: max})
	var evs []model.OutboxEvent
	for i := 0; i < max+5; i++ {
		evs = append(evs, event(fmt.Sprintf("e%02d", i), t0.Add(time.Duration(i)*time.Second)))
	}
	enqueueEvents(t, s, evs...)
	if n := count(t, s, `SELECT COUNT(*) FROM outbox WHERE status='pending'`); n != max {
		t.Fatalf("pending = %d, want %d", n, max)
	}
	for i := 0; i < 5; i++ {
		if st, _, r := status(t, s, fmt.Sprintf("e%02d", i)); st != "dropped" || r != "queue_full" {
			t.Fatalf("e%02d = %s %s", i, st, r)
		}
	}
	if st, _, _ := status(t, s, "e05"); st != "pending" {
		t.Fatalf("e05 = %s", st)
	}
}

// 7. Prune.
func TestPrune(t *testing.T) {
	s := openTest(t, t.TempDir(), Options{})
	ctx := context.Background()
	now := t0.Add(200 * 24 * time.Hour)
	day := 24 * time.Hour

	for i := 0; i < 1203; i++ {
		if err := s.RecordObservation(ctx, result("c", true, now.Add(-91*day))); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.RecordObservation(ctx, result("c", true, now.Add(-89*day))); err != nil {
		t.Fatal(err)
	}
	old, recent := now.Add(-31*day), now.Add(-29*day)
	enqueueEvents(t, s, event("old-delivered", old), event("recent-delivered", recent), event("old-pending", old))
	for id, at := range map[string]time.Time{"old-delivered": old, "recent-delivered": recent} {
		if err := s.MarkDelivery(ctx, id, model.DeliverySuccess, at, time.Time{}, ""); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.EnqueueReport(ctx, "old-report", model.OutboxEvent{ID: "rep", EnqueuedAt: old.Add(-100 * day)}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkDelivery(ctx, "rep", model.DeliverySuccess, old.Add(-100*day), time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	incidentsBefore := count(t, s, `SELECT COUNT(*) FROM incidents`)
	obsBefore := count(t, s, `SELECT COUNT(*) FROM observations`)

	res, err := s.Prune(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	// enqueueEvents added 3 observations: 2 at old (-31d, kept) and 1 at recent.
	if res.Observations != 1203 || res.OutboxRows != 1 {
		t.Fatalf("Prune = %+v", res)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM observations`); n != obsBefore-1203 {
		t.Fatalf("observations left = %d", n)
	}
	if n := count(t, s, `SELECT COUNT(*) FROM outbox WHERE event_id = 'old-delivered'`); n != 0 {
		t.Fatal("old delivered event not pruned")
	}
	for _, id := range []string{"recent-delivered", "old-pending", "rep"} {
		if n := count(t, s, `SELECT COUNT(*) FROM outbox WHERE event_id = ?`, id); n != 1 {
			t.Fatalf("%s pruned", id)
		}
	}
	if n := count(t, s, `SELECT COUNT(*) FROM incidents`); n != incidentsBefore || n == 0 {
		t.Fatalf("incidents = %d, want %d", n, incidentsBefore)
	}
}

// 8. Persistence across reopen.
func TestReopenPersists(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vshm.db")
	s, err := Open(context.Background(), path, newClock(), Options{})
	if err != nil {
		t.Fatal(err)
	}
	enqueueEvents(t, s, event("e1", t0))
	if _, err := s.EnqueueReport(context.Background(), "k", model.OutboxEvent{ID: "r", EnqueuedAt: t0}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s = openTest(t, dir, Options{})
	if count(t, s, `SELECT COUNT(*) FROM observations`) != 1 || count(t, s, `SELECT COUNT(*) FROM incidents`) != 1 ||
		count(t, s, `SELECT COUNT(*) FROM outbox`) != 2 {
		t.Fatal("rows lost after reopen")
	}
	if ins, err := s.EnqueueReport(context.Background(), "k", model.OutboxEvent{ID: "r2", EnqueuedAt: t0}); err != nil || ins {
		t.Fatalf("report key not persisted: %v %v", ins, err)
	}
}

// 9. Status counts.
func TestStatus(t *testing.T) {
	dir := t.TempDir()
	s := openTest(t, dir, Options{MaxPending: 3})
	ctx := context.Background()
	now := t0.Add(48 * time.Hour)
	enqueueEvents(t, s, event("x1", t0), event("x2", now), event("x3", now), event("x4", now), event("x5", now))
	// MaxPending 3 drops x1 and x2; x6 is older than PendingMaxAge and expires.
	if err := s.MarkDelivery(ctx, "x3", model.DeliverySuccess, now, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	enqueueEvents(t, s, model.OutboxEvent{ID: "x6", EnqueuedAt: t0})
	if _, err := s.Pending(ctx, now, 10); err != nil {
		t.Fatal(err)
	}
	st, err := s.Status(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	want := map[model.DeliveryState]int64{"pending": 2, "delivered": 1, "expired": 1, "dropped": 2}
	for k, v := range want {
		if st.OutboxCounts[k] != v {
			t.Errorf("OutboxCounts[%s] = %d, want %d (all: %v)", k, st.OutboxCounts[k], v, st.OutboxCounts)
		}
	}
	if !st.Healthy || st.SchemaVersion != "1" || st.RecordsCount != 6 || st.DBSizeBytes <= 0 ||
		st.LockPath != filepath.Join(dir, LockFileName) {
		t.Fatalf("status = %+v", st)
	}
}
