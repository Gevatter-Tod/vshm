package reports

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/metrics"
	"github.com/Gevatter-Tod/vshm/internal/model"
	"github.com/Gevatter-Tod/vshm/internal/model/fake"
	"github.com/Gevatter-Tod/vshm/internal/storage"
)

func withLocal(t *testing.T, name string) {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("load zone %s: %v", name, err)
	}
	old := time.Local
	time.Local = loc
	t.Cleanup(func() { time.Local = old })
}

func newTestService(t *testing.T, store model.Store, clock model.Clock, reportTime string, checks []CheckLimit) *Service {
	t.Helper()
	svc, err := New("test-host", store, clock, reportTime, nil, checks, metrics.NewWithDefaults(), nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return svc
}

func TestParseHHMM(t *testing.T) {
	for _, bad := range []string{"", "9:00", "09:00x", "24:00", "09:60", "ab:cd"} {
		if _, err := New("h", fake.NewStore(), fake.NewClock(), bad, nil, nil, nil, nil); err == nil {
			t.Errorf("report_time %q: want error", bad)
		}
	}
	if _, err := New("h", fake.NewStore(), fake.NewClock(), "09:00", nil, nil, nil, nil); err != nil {
		t.Errorf("09:00: unexpected error %v", err)
	}
}

func TestNextReportUTC(t *testing.T) {
	withLocal(t, "UTC")
	clock := fake.NewClock()
	svc := newTestService(t, fake.NewStore(), clock, "09:00", nil)

	at := func(s string) time.Time {
		tm, err := time.Parse(time.RFC3339, s)
		if err != nil {
			t.Fatal(err)
		}
		return tm
	}
	cases := []struct{ now, want string }{
		{"2025-11-20T08:00:00Z", "2025-11-20T09:00:00Z"}, // before today's report
		{"2025-11-20T09:00:00Z", "2025-11-21T09:00:00Z"}, // exactly at report time -> next day
		{"2025-11-20T10:00:00Z", "2025-11-21T09:00:00Z"}, // after -> next day (no catch-up)
	}
	for _, c := range cases {
		if got := svc.NextReport(at(c.now)); !got.Equal(at(c.want)) {
			t.Errorf("NextReport(%s) = %s, want %s", c.now, got, c.want)
		}
	}
}

func TestNextReportDSTGap(t *testing.T) {
	withLocal(t, "America/New_York")
	clock := fake.NewClock()
	svc := newTestService(t, fake.NewStore(), clock, "02:30", nil)

	// 2025-03-09 02:30 does not exist (spring forward 02:00 -> 03:00 EDT).
	now := time.Date(2025, 3, 9, 1, 0, 0, 0, time.Local)
	got := svc.NextReport(now)
	// First valid local instant after the gap: 2025-03-09 03:00 EDT (UTC-4).
	want := time.Date(2025, 3, 9, 3, 0, 0, 0, time.Local)
	if !got.Equal(want) {
		t.Errorf("gap: got %s, want %s", got, want)
	}
	if _, off := got.Zone(); off != -4*3600 {
		t.Errorf("gap: offset = %d, want -14400 (EDT)", off)
	}
}

func TestNextReportDSTFold(t *testing.T) {
	withLocal(t, "America/New_York")
	clock := fake.NewClock()
	svc := newTestService(t, fake.NewStore(), clock, "01:30", nil)

	// 2025-11-02 01:30 occurs twice (fall back 02:00 EDT -> 01:00 EST).
	now := time.Date(2025, 11, 2, 0, 0, 0, 0, time.Local)
	got := svc.NextReport(now)
	// First occurrence: 2025-11-02 01:30 EDT (UTC-4).
	if _, off := got.Zone(); off != -4*3600 {
		t.Errorf("fold: offset = %d, want -14400 (first occurrence, EDT)", off)
	}
	if got.Hour() != 1 || got.Minute() != 30 {
		t.Errorf("fold: wall clock = %02d:%02d, want 01:30", got.Hour(), got.Minute())
	}
}

func TestRenderFormat(t *testing.T) {
	withLocal(t, "UTC")
	clock := fake.NewClock()
	now := time.Date(2025, 11, 20, 9, 0, 0, 0, time.UTC)
	clock.SetNow(now)
	checks := []CheckLimit{
		{CheckID: "web", CheckType: "http", Interval: time.Minute, Timeout: 10 * time.Second},
		{CheckID: "db", CheckType: "mysql", Interval: 5 * time.Minute, Timeout: 10 * time.Second},
		{CheckID: "never-ran", CheckType: "ping", Interval: time.Minute, Timeout: 10 * time.Second},
	}
	svc := newTestService(t, fake.NewStore(), clock, "09:00", checks)

	snap := model.Snapshot{
		HostName: "test-host",
		Time:     now,
		Checks: []model.CheckStatus{
			{CheckID: "web", CheckType: "http", LastStatus: "success", LastCompleted: now.Add(-30 * time.Second), ReasonCode: "ok"},
			{CheckID: "db", CheckType: "mysql", LastStatus: "failure", LastCompleted: now.Add(-30 * time.Minute), ReasonCode: "query_failed", Reason: "query_failed: connection refused"},
		},
	}
	ev, err := svc.Render(context.Background(), snap, now)
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	p := ev.Payload

	// Header with host-local DD.MM.YYYY timestamp, no offset.
	if !strings.Contains(p, "daily report host=test-host at=20.11.2025 09:00:00\n") {
		t.Errorf("missing/incorrect header:\n%s", p)
	}
	// Metrics come before the check lines.
	if strings.Index(p, "metrics:") > strings.Index(p, "check web") {
		t.Errorf("metrics block must precede check lines:\n%s", p)
	}
	// Fresh success: checkmark icon, not stale; no timestamp on check lines.
	if !strings.Contains(p, "✅ check web (http): age=30s reason_code=ok\n") {
		t.Errorf("missing web line:\n%s", p)
	}
	// Old failure beyond interval+timeout: cross + hourglass icons.
	if !strings.Contains(p, "❌⌛ check db (mysql): age=30m0s reason_code=query_failed reason=query_failed: connection refused\n") {
		t.Errorf("missing stale db line:\n%s", p)
	}
	// Only the header line carries a date/time.
	if strings.Count(p, "20.11.2025") != 1 {
		t.Errorf("date must appear only in the header:\n%s", p)
	}
	// Configured but never run.
	if !strings.Contains(p, "❓ check never-ran (ping): NO RESULT\n") {
		t.Errorf("missing NO RESULT line:\n%s", p)
	}
	if ev.Kind != "report" || ev.HostName != "test-host" {
		t.Errorf("event kind/host = %q/%q", ev.Kind, ev.HostName)
	}
}

func TestRunReportDedupAndRestart(t *testing.T) {
	withLocal(t, "UTC")
	ctx := context.Background()
	dir := t.TempDir()
	clock := fake.NewClock()
	now := time.Date(2025, 11, 20, 9, 0, 0, 0, time.UTC)
	clock.SetNow(now)

	store, err := storage.Open(ctx, dir+"/vshm.db", clock, storage.Options{})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	svc := newTestService(t, store, clock, "09:00", []CheckLimit{{CheckID: "web", CheckType: "http", Interval: time.Minute, Timeout: time.Second}})
	if err := svc.runReport(ctx); err != nil {
		t.Fatalf("runReport: %v", err)
	}
	// Second run on the same date must not enqueue a duplicate.
	if err := svc.runReport(ctx); err != nil {
		t.Fatalf("runReport again: %v", err)
	}

	events, err := store.Pending(ctx, now, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	reports := 0
	for _, ev := range events {
		if ev.Kind == "report" {
			reports++
			if ev.LocalReport != "2025-11-20" {
				t.Errorf("date key = %q, want 2025-11-20", ev.LocalReport)
			}
		}
	}
	if reports != 1 {
		t.Fatalf("report events after duplicate runs = %d, want 1", reports)
	}

	// A restarted service (new store handle) on the same date stays deduplicated.
	svc2 := newTestService(t, store, clock, "09:00", nil)
	if err := svc2.runReport(ctx); err != nil {
		t.Fatalf("runReport after restart: %v", err)
	}
	events, err = store.Pending(ctx, now, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	reports = 0
	for _, ev := range events {
		if ev.Kind == "report" {
			reports++
		}
	}
	if reports != 1 {
		t.Fatalf("report events after restart = %d, want 1", reports)
	}

	// Next day produces a second report under a new date key.
	tomorrow := now.AddDate(0, 0, 1)
	clock.SetNow(tomorrow)
	svc3 := newTestService(t, store, clock, "09:00", nil)
	if err := svc3.runReport(ctx); err != nil {
		t.Fatalf("runReport next day: %v", err)
	}
	events, err = store.Pending(ctx, tomorrow, 0)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	reports = 0
	for _, ev := range events {
		if ev.Kind == "report" {
			reports++
		}
	}
	if reports != 2 {
		t.Fatalf("report events next day = %d, want 2", reports)
	}
}
