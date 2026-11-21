// Package reports implements WP-11: daily report rendering and the DST-aware
// local-time schedule with durable per-date deduplication.
//
// Decisions (user-confirmed):
//   - Report text is plain text; the metrics block comes first, then one line
//     per configured check.
//   - The durable per-local-date dedupe key is the local calendar date in
//     "2006-01-02" form, stored in outbox.dedupe_key via Store.EnqueueReport
//     (key and event are inserted in one transaction; both or neither).
//   - Scheduling uses the server-local timezone (time.Local, host tzdata);
//     no timezone config exists. A report time inside a spring-forward DST
//     gap fires at the first valid local instant after the gap; a time inside
//     a fall-back fold fires at its first occurrence.
//   - The only date/time in the report is the header's host-local
//     "02.01.2006 15:04:05" (DD.MM.YYYY, no UTC offset; user-confirmed display
//     decision); check lines carry only the age. Each check line starts with a
//     status emoticon (✅/❌/❓) and carries ⌛ when STALE.
//   - At startup only the next future report is scheduled; reports missed
//     during downtime are never caught up.
package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/metrics"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// dateKeyLayout is the stable local-date dedupe key representation.
const dateKeyLayout = "2006-01-02"

// displayLayout is the host-local timestamp format used in rendered reports.
const displayLayout = "02.01.2006 15:04:05"

// Status emoticons at the start of each check line; stale adds ⌛.
const (
	iconSuccess  = "✅"
	iconFailure  = "❌"
	iconNoResult = "❓"
	iconStale    = "⌛"
)

// CheckLimit describes one configured check: its identity plus the resolved
// interval/timeout used for STALE evaluation.
type CheckLimit struct {
	CheckID   string
	CheckType string
	Interval  time.Duration
	Timeout   time.Duration
}

// Logger is the sanitized logging surface (satisfied by *logging.Logger).
type Logger interface {
	Printf(format string, v ...any)
}

// Service renders daily reports and schedules them at a fixed local HH:MM.
// It implements model.Reporter.
type Service struct {
	store   model.Store
	clock   model.Clock
	host    string
	hour    int
	minute  int
	mounts  []string
	checks  []CheckLimit
	metrics *metrics.Collector
	logger  Logger
}

var _ model.Reporter = (*Service)(nil)

// New creates the daily report service. reportTime is local "HH:MM" (24h).
func New(host string, store model.Store, clock model.Clock, reportTime string, mounts []string, checks []CheckLimit, collector *metrics.Collector, logger Logger) (*Service, error) {
	h, m, err := parseHHMM(reportTime)
	if err != nil {
		return nil, err
	}
	if collector == nil {
		collector = metrics.NewWithDefaults()
	}
	return &Service{
		store:   store,
		clock:   clock,
		host:    host,
		hour:    h,
		minute:  m,
		mounts:  metrics.ResolveMounts(mounts),
		checks:  checks,
		metrics: collector,
		logger:  logger,
	}, nil
}

func parseHHMM(s string) (int, int, error) {
	var h, m int
	if _, err := fmt.Sscanf(s, "%d:%d", &h, &m); err != nil || len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("reports: invalid report_time %q (want HH:MM 24h)", s)
	}
	if h < 0 || h > 23 || m < 0 || m > 59 {
		return 0, 0, fmt.Errorf("reports: invalid report_time %q (hour 0-23, minute 0-59)", s)
	}
	return h, m, nil
}

func (s *Service) logf(format string, v ...any) {
	if s.logger != nil {
		s.logger.Printf(format, v...)
	}
}

// dateKey returns the durable local-date dedupe key for t.
func dateKey(t time.Time) string { return t.In(time.Local).Format(dateKeyLayout) }

// NextReport returns the next strictly-future local report instant after now.
func (s *Service) NextReport(now time.Time) time.Time {
	loc := now.In(time.Local)
	cand := resolveLocalWall(loc.Year(), loc.Month(), loc.Day(), s.hour, s.minute)
	if !cand.After(now) {
		next := loc.AddDate(0, 0, 1)
		cand = resolveLocalWall(next.Year(), next.Month(), next.Day(), s.hour, s.minute)
	}
	return cand
}

// resolveLocalWall resolves a local wall clock on the given local calendar date
// to a concrete instant: for a DST gap it returns the first valid local instant
// after the gap; for a DST fold it returns the first occurrence.
func resolveLocalWall(y int, m time.Month, d, hh, mm int) time.Time {
	day := time.Date(y, m, d, 0, 0, 0, 0, time.Local)
	probe := day
	limit := day.Add(26 * time.Hour)
	for probe.Before(limit) {
		if wallGE(probe, hh, mm) {
			// Walk back over any repeated wall clock to reach the first occurrence.
			e := probe
			for {
				prev := e.Add(-time.Minute)
				if sameWall(prev, e) {
					e = prev
				} else {
					break
				}
			}
			return e
		}
		probe = probe.Add(time.Minute)
	}
	return time.Date(y, m, d, hh, mm, 0, 0, time.Local)
}

// wallGE reports whether t's local wall clock is at or past hh:mm on its date.
func wallGE(t time.Time, hh, mm int) bool {
	return t.Hour() > hh || (t.Hour() == hh && t.Minute() >= mm)
}

// sameWall reports whether a and b share the same local wall clock.
func sameWall(a, b time.Time) bool {
	return a.Year() == b.Year() && a.Month() == b.Month() && a.Day() == b.Day() &&
		a.Hour() == b.Hour() && a.Minute() == b.Minute()
}

// FormatCheckLine renders one check line in the shared report/CLI format:
// leading status emoticon (✅/❌, plus ⌛ when stale), then age, reason_code and
// optional sanitized reason. No timestamp — the report header carries the only
// date/time. Used by both Render and the `vshm once` observer so the two outputs
// cannot drift apart.
func FormatCheckLine(checkID, checkType string, success, stale bool, age time.Duration, reasonCode, reason string) string {
	icon := iconSuccess
	if !success {
		icon = iconFailure
	}
	if stale {
		icon += iconStale
	}
	line := fmt.Sprintf("%s check %s (%s): age=%s reason_code=%s", icon, checkID, checkType, age.Round(time.Second), reasonCode)
	if reason != "" {
		line += " reason=" + reason
	}
	return line
}

// Render builds the daily report event for the snapshot at now. Resource
// metrics are sampled at render time and rendered first, followed by one line
// per configured check.
func (s *Service) Render(ctx context.Context, snap model.Snapshot, now time.Time) (model.OutboxEvent, error) {
	ms := s.metrics.Collect(ctx, s.mounts, now)

	local := func(t time.Time) string { return t.In(time.Local).Format(displayLayout) }

	var sb strings.Builder
	fmt.Fprintf(&sb, "daily report host=%s at=%s\n", s.host, local(now))
	sb.WriteString(metrics.Render(ms))

	byID := make(map[string]model.CheckStatus, len(snap.Checks))
	for _, c := range snap.Checks {
		byID[c.CheckID] = c
	}
	for _, cl := range s.checks {
		c, ok := byID[cl.CheckID]
		if !ok {
			fmt.Fprintf(&sb, "%s check %s (%s): NO RESULT\n", iconNoResult, cl.CheckID, cl.CheckType)
			continue
		}
		age := now.Sub(c.LastCompleted)
		stale := age > cl.Interval+cl.Timeout
		sb.WriteString(FormatCheckLine(cl.CheckID, cl.CheckType, c.LastStatus == "success", stale, age, c.ReasonCode, c.Reason) + "\n")
	}

	return model.OutboxEvent{
		Kind:       "report",
		HostName:   s.host,
		Payload:    sb.String(),
		EnqueuedAt: now,
	}, nil
}

// Schedule runs the report loop until ctx is cancelled: it waits for the next
// future local report instant, renders and enqueues the report, and repeats.
// Reports missed while stopped are not caught up.
func (s *Service) Schedule(ctx context.Context) error {
	for {
		now := s.clock.Now()
		next := s.NextReport(now)
		timer := s.clock.NewTimer(next.Sub(now))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C():
			if err := s.runReport(ctx); err != nil {
				s.logf("daily report failed: %v", err)
			}
		}
	}
}

// ReportNow renders and enqueues the report for the current local date
// immediately (used once at agent startup, right after the prompt first check
// pass). The unique local-date key makes this idempotent: if today's report
// already exists, no new event is enqueued.
func (s *Service) ReportNow(ctx context.Context) error { return s.runReport(ctx) }

// runReport renders and transactionally enqueues the report for the current
// local date. The date key prevents duplicates on restart, clock adjustment or
// DST transitions; if the key already exists no new event is enqueued.
func (s *Service) runReport(ctx context.Context) error {
	now := s.clock.Now()
	snap, err := s.store.Snapshot(ctx, now)
	if err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	snap.HostName = s.host
	ev, err := s.Render(ctx, snap, now)
	if err != nil {
		return fmt.Errorf("render: %w", err)
	}
	key := dateKey(now)
	ev.ID = "report-" + key
	ev.LocalReport = key
	inserted, err := s.store.EnqueueReport(ctx, key, ev)
	if err != nil {
		return fmt.Errorf("enqueue report: %w", err)
	}
	if inserted {
		s.logf("daily report for %s enqueued", key)
	}
	return nil
}
