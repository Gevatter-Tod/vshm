package incidents

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/model/fake"
	"github.com/Gevatter-Tod/vshm/internal/storage"
)

var t0 = time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)

func newTestClock() *fake.Clock {
	c := fake.NewClock()
	c.SetNow(t0)
	return c
}

func newService(t *testing.T, store model.Store, clock model.Clock) *Service {
	t.Helper()
	svc, err := New("web-01", store, clock, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func result(checkID string, ok bool, at time.Time) model.CheckResult {
	return model.CheckResult{
		CheckID:     checkID,
		CheckType:   "systemd",
		Success:     ok,
		StartedAt:   at.Add(-time.Second).UTC(),
		CompletedAt: at.UTC(),
		Duration:    time.Second,
		ReasonCode:  map[bool]string{true: "success", false: "systemd_failed"}[ok],
		Reason:      map[bool]string{true: "unit is active", false: "unit is not active"}[ok],
	}
}

func TestNewValidation(t *testing.T) {
	store := fake.NewStore()
	clk := newTestClock()
	if _, err := New("", store, clk, nil); err == nil {
		t.Error("empty host accepted")
	}
	if _, err := New("web-01", nil, clk, nil); err == nil {
		t.Error("nil store accepted")
	}
	if _, err := New("web-01", store, nil, nil); err == nil {
		t.Error("nil clock accepted")
	}
	if _, err := New("web-01", store, clk, nil); err != nil {
		t.Errorf("valid args rejected: %v", err)
	}
}

func TestObserveRequiresCheckID(t *testing.T) {
	svc := newService(t, fake.NewStore(), newTestClock())
	if err := svc.Observe(context.Background(), result("", false, t0)); err == nil {
		t.Fatal("empty check ID accepted")
	}
}

// First failure: opens the incident and enqueues exactly one failure event.
func TestFirstFailureOpensIncidentAndEnqueuesOneEvent(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())
	r := result("nginx", false, t0)

	if err := svc.Observe(context.Background(), r); err != nil {
		t.Fatalf("Observe: %v", err)
	}

	events := store.OutboxEvents()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	ev := events[0]
	if ev.ID == "" || ev.Kind != OutboxKind || ev.HostName != "web-01" ||
		ev.CheckID != "nginx" || ev.CheckType != "systemd" || ev.IncidentID != "nginx" {
		t.Errorf("unexpected event: %+v", ev)
	}
	want := fmt.Sprintf(`vshm event=failure host=web-01 check=nginx type=systemd time=%s reason_code=systemd_failed reason="unit is not active"`,
		formatLocal(r.CompletedAt))
	if ev.Payload != want {
		t.Errorf("payload:\n got %q\nwant %q", ev.Payload, want)
	}

	inc, ok := store.Incident("nginx")
	if !ok || inc.ResolvedAt != nil {
		t.Fatalf("incident = %+v, %v; want open", inc, ok)
	}
	if !inc.OpenedAt.Equal(r.CompletedAt) {
		t.Errorf("OpenedAt = %v, want %v", inc.OpenedAt, r.CompletedAt)
	}

	muts := store.Mutations()
	if len(muts) != 1 || !muts[0].Open || muts[0].HostName != "web-01" ||
		!muts[0].LastFailure.Equal(r.CompletedAt) || muts[0].ReasonCode != "systemd_failed" {
		t.Errorf("mutations = %+v", muts)
	}
}

// Repeat failures: one distinct event each, no duplicate incident, the
// original incident start is preserved, last failure is updated.
func TestRepeatFailuresGetDistinctEventsAndKeepIncident(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())
	t1 := t0
	t2 := t0.Add(30 * time.Minute)

	if err := svc.Observe(context.Background(), result("nginx", false, t1)); err != nil {
		t.Fatal(err)
	}
	if err := svc.Observe(context.Background(), result("nginx", false, t2)); err != nil {
		t.Fatal(err)
	}

	events := store.OutboxEvents()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2 (one per failed observation)", len(events))
	}
	if events[0].ID == events[1].ID {
		t.Errorf("repeat failure reused event ID %q", events[0].ID)
	}
	for i, ev := range events {
		if !strings.Contains(ev.Payload, "event=failure") {
			t.Errorf("event %d payload missing event=failure: %q", i, ev.Payload)
		}
	}
	if !strings.Contains(events[1].Payload, formatLocal(t2)) {
		t.Errorf("second event carries wrong time: %q", events[1].Payload)
	}

	inc, _ := store.Incident("nginx")
	if inc.ResolvedAt != nil {
		t.Error("incident closed by repeat failure")
	}
	if !inc.OpenedAt.Equal(t1) {
		t.Errorf("OpenedAt = %v, want first failure %v", inc.OpenedAt, t1)
	}
	if !inc.LastFailure.Equal(t2) {
		t.Errorf("LastFailure = %v, want %v", inc.LastFailure, t2)
	}
	if n := len(store.Mutations()); n != 2 {
		t.Errorf("mutations = %d, want 2", n)
	}
}

// First success after failures closes the incident and enqueues exactly one
// recovery event carrying the incident start time.
func TestRecoveryClosesIncidentOnce(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())
	t1 := t0
	t2 := t0.Add(30 * time.Minute)
	t3 := t0.Add(60 * time.Minute)

	_ = svc.Observe(context.Background(), result("nginx", false, t1))
	_ = svc.Observe(context.Background(), result("nginx", false, t2))
	if err := svc.Observe(context.Background(), result("nginx", true, t3)); err != nil {
		t.Fatalf("recovery Observe: %v", err)
	}

	events := store.OutboxEvents()
	if len(events) != 3 {
		t.Fatalf("events = %d, want 3 (2 failures + 1 recovery)", len(events))
	}
	rec := events[2]
	if !strings.Contains(rec.Payload, "event=recovery") {
		t.Fatalf("third event is not a recovery: %q", rec.Payload)
	}
	want := fmt.Sprintf(`vshm event=recovery host=web-01 check=nginx type=systemd time=%s incident_started=%s status=ok reason_code=success reason="unit is active"`,
		formatLocal(t3), formatLocal(t1))
	if rec.Payload != want {
		t.Errorf("recovery payload:\n got %q\nwant %q", rec.Payload, want)
	}

	inc, _ := store.Incident("nginx")
	if inc.ResolvedAt == nil || !inc.ResolvedAt.Equal(t3) {
		t.Errorf("ResolvedAt = %v, want %v", inc.ResolvedAt, t3)
	}
	if !inc.OpenedAt.Equal(t1) {
		t.Errorf("OpenedAt changed to %v, want %v", inc.OpenedAt, t1)
	}

	// A second success must be silent: no new event, no new mutation.
	t4 := t0.Add(90 * time.Minute)
	if err := svc.Observe(context.Background(), result("nginx", true, t4)); err != nil {
		t.Fatal(err)
	}
	if n := len(store.OutboxEvents()); n != 3 {
		t.Errorf("events after second success = %d, want still 3", n)
	}
	if n := len(store.Mutations()); n != 3 {
		t.Errorf("mutations after second success = %d, want still 3", n)
	}
}

// Success without an open incident records the observation only: no event,
// no incident mutation.
func TestSuccessWithoutOpenIncidentIsSilent(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())

	if err := svc.Observe(context.Background(), result("nginx", true, t0)); err != nil {
		t.Fatal(err)
	}
	if n := len(store.Observations()); n != 1 {
		t.Errorf("observations = %d, want 1", n)
	}
	if n := len(store.OutboxEvents()); n != 0 {
		t.Errorf("events = %d, want 0 (healthy observation must not notify)", n)
	}
	if n := len(store.Mutations()); n != 0 {
		t.Errorf("mutations = %d, want 0", n)
	}
	if _, ok := store.Incident("nginx"); ok {
		t.Error("incident row created by silent success")
	}
}

// Events are never grouped across checks: each check's failure gets its own event.
func TestSeparateEventsAcrossChecks(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())

	_ = svc.Observe(context.Background(), result("nginx", false, t0))
	_ = svc.Observe(context.Background(), result("db", false, t0))

	events := store.OutboxEvents()
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].ID == events[1].ID {
		t.Error("events across checks share an ID")
	}
	if !strings.Contains(events[0].Payload, "check=nginx") || !strings.Contains(events[1].Payload, "check=db") {
		t.Errorf("payloads not check-specific: %q / %q", events[0].Payload, events[1].Payload)
	}
}

// The message must stay one bounded line without embedded quotes/newlines even
// for hostile reason text.
func TestPayloadSanitization(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())
	r := result("nginx", false, t0)
	r.Reason = "line1\nline2 \"quoted\" " + strings.Repeat("x", 500) + "\ttab"

	if err := svc.Observe(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	payload := store.OutboxEvents()[0].Payload
	if strings.ContainsAny(payload, "\n\r") {
		t.Errorf("payload has newline: %q", payload)
	}
	if strings.Contains(payload, `"`) && !strings.HasSuffix(payload, `"`) {
		t.Errorf("payload has embedded quote: %q", payload)
	}
	// exactly the two delimiting quotes of reason="..."
	if n := strings.Count(payload, `"`); n != 2 {
		t.Errorf("payload quote count = %d, want 2: %q", n, payload)
	}
	if len(payload) > maxLineLen+200 {
		t.Errorf("payload too long: %d", len(payload))
	}
}

func TestFormatLocalAlwaysNumericOffset(t *testing.T) {
	utc := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	if got := formatLocal(utc); !strings.HasSuffix(got, "+00:00") {
		t.Errorf("UTC time formatted as %q, want numeric +00:00 (not Z)", got)
	}
	// The layout itself must render any zone as a numeric offset.
	ist := time.FixedZone("IST", 5*3600+30*60)
	got := time.Date(2026, 3, 1, 12, 0, 0, 0, ist).Format(localTimeLayout)
	if !strings.HasSuffix(got, "+05:30") {
		t.Errorf("fixed zone formatted as %q, want +05:30", got)
	}
	if !regexp.MustCompile(`[+-]\d{2}:\d{2}$`).MatchString(got) {
		t.Errorf("no numeric UTC offset in %q", got)
	}
}

func TestEventIDFormat(t *testing.T) {
	store := fake.NewStore()
	svc := newService(t, store, newTestClock())
	r := result("nginx", false, t0)
	_ = svc.Observe(context.Background(), r)
	id := store.OutboxEvents()[0].ID
	if !regexp.MustCompile(`^evt-failure-nginx-\d+$`).MatchString(id) {
		t.Errorf("event ID %q not in expected shape", id)
	}
}

// --- integration with the real SQLite store ---

func openRealStore(t *testing.T, dir string, clock model.Clock) *storage.SQLiteStore {
	t.Helper()
	s, err := storage.Open(context.Background(), filepath.Join(dir, "vshm.db"), clock, storage.Options{})
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	return s
}

// A storage failure inside the atomic write must roll the observation back too:
// a duplicate event ID makes the second RecordObservationAndIncident fail, and
// no partial state may remain.
func TestAtomicRollbackOnStoreFailure(t *testing.T) {
	dir := t.TempDir()
	clk := newTestClock()
	store := openRealStore(t, dir, clk)
	t.Cleanup(func() { _ = store.Close() })

	svc := newService(t, store, clk)
	r := result("nginx", false, t0)
	if err := svc.Observe(context.Background(), r); err != nil {
		t.Fatalf("first Observe: %v", err)
	}

	// Same check, same completion instant -> same generated event ID -> the
	// UNIQUE(event_id) constraint fails the whole transaction.
	if err := svc.Observe(context.Background(), r); err == nil {
		t.Fatal("duplicate event ID accepted; want storage error")
	}

	st, err := store.Status(context.Background(), clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if st.RecordsCount != 1 {
		t.Errorf("observations after rollback = %d, want 1 (atomic rollback)", st.RecordsCount)
	}
	pending, err := store.Pending(context.Background(), clk.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 1 {
		t.Errorf("pending events after rollback = %d, want 1", len(pending))
	}
}

// Incident state survives a restart (store close + reopen): the first success
// after the restart closes the incident opened before the restart, and the
// recovery message carries the original incident start time.
func TestStateSurvivesRestart(t *testing.T) {
	dir := t.TempDir()
	clk := newTestClock()

	store1 := openRealStore(t, dir, clk)
	svc1 := newService(t, store1, clk)
	t1 := t0
	if err := svc1.Observe(context.Background(), result("nginx", false, t1)); err != nil {
		t.Fatalf("Observe before restart: %v", err)
	}
	snap, err := store1.Snapshot(context.Background(), clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Incidents) != 1 || snap.Incidents[0].ResolvedAt != nil {
		t.Fatalf("incidents before restart = %+v, want one open", snap.Incidents)
	}
	if err := store1.Close(); err != nil {
		t.Fatal(err)
	}

	// "Restart": new store instance on the same file, new service instance.
	store2 := openRealStore(t, dir, clk)
	t.Cleanup(func() { _ = store2.Close() })
	svc2 := newService(t, store2, clk)

	t2 := t0.Add(45 * time.Minute)
	if err := svc2.Observe(context.Background(), result("nginx", true, t2)); err != nil {
		t.Fatalf("Observe after restart: %v", err)
	}

	pending, err := store2.Pending(context.Background(), clk.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 2 {
		t.Fatalf("pending events = %d, want 2 (failure + recovery)", len(pending))
	}
	rec := pending[1]
	if !strings.Contains(rec.Payload, "event=recovery") {
		t.Errorf("second event not recovery: %q", rec.Payload)
	}
	if !strings.Contains(rec.Payload, "incident_started="+formatLocal(t1)) {
		t.Errorf("recovery missing original incident start: %q", rec.Payload)
	}

	snap2, err := store2.Snapshot(context.Background(), clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap2.Incidents) != 1 || snap2.Incidents[0].ResolvedAt == nil {
		t.Fatalf("incidents after restart = %+v, want one closed", snap2.Incidents)
	}
	if !snap2.Incidents[0].ResolvedAt.Equal(t2) {
		t.Errorf("ResolvedAt after restart = %v, want %v", snap2.Incidents[0].ResolvedAt, t2)
	}
}

// Downtime alone fabricates nothing: after a restart with no open incident, a
// success stays silent; and no observation appears for time the agent was down.
func TestDowntimeAloneDoesNotTransition(t *testing.T) {
	dir := t.TempDir()
	clk := newTestClock()
	store := openRealStore(t, dir, clk)
	t.Cleanup(func() { _ = store.Close() })
	svc := newService(t, store, clk)

	// Agent "down" from t0 to t0+2h, then a healthy observation.
	clk.SetNow(t0.Add(2 * time.Hour))
	if err := svc.Observe(context.Background(), result("nginx", true, t0.Add(2*time.Hour))); err != nil {
		t.Fatal(err)
	}
	pending, err := store.Pending(context.Background(), clk.Now(), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pending) != 0 {
		t.Errorf("pending events after downtime+success = %d, want 0", len(pending))
	}
	snap, err := store.Snapshot(context.Background(), clk.Now())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Incidents) != 0 {
		t.Errorf("incidents = %+v, want none (downtime is not a failure)", snap.Incidents)
	}
}
