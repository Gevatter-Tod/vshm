// Package incidents implements the WP-10 incident service: it consumes every
// normalized observation produced by the scheduler (it satisfies
// scheduler.Observer) and transactionally persists the observation, the
// per-check incident state transition and the corresponding outbox event(s)
// through model.Store.RecordObservationAndIncident.
//
// Transition semantics (spec §5, fixed — no thresholds, no flap suppression):
//   - every failed observation enqueues exactly one distinct, uniquely
//     identified failure event, whether or not the incident is already open;
//   - the first failure opens the incident; repeat failures only update the
//     last-failure time/reason (the store keeps the original "since");
//   - the first success after one or more failures closes the incident and
//     enqueues exactly one recovery event that carries the incident start time;
//   - a success without an open incident is recorded silently (no event, no
//     incident write);
//   - process downtime alone never opens or closes an incident: state is read
//     back from the store on every observation, so a restart preserves it.
//
// Incident state is keyed by the exact configured host_name plus the stable
// check ID. The store is per-host (single agent per host, enforced by the
// storage process lock), so the host name is the identity of the whole state
// directory and is carried on every mutation/event; the check ID keys the
// incident row inside it.
//
// Messages are plain text (never Markdown), one single-line key=value message
// per event (user-confirmed format):
//
//	failure:  vshm event=failure host=H check=C type=T time=<local RFC3339, numeric UTC offset> reason_code=R reason="S"
//	recovery: vshm event=recovery host=H check=C type=T time=<local> incident_started=<local> status=ok reason_code=R reason="S"
//
// Reason codes and reason texts are passed through from the observation,
// sanitized (single line, bounded, quote-free) — WP-10 introduces no reason
// vocabulary of its own (user-confirmed).
package incidents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Event kinds carried in the payload's event= key (the outbox row kind stays
// "event"; reports use "report").
const (
	KindFailure  = "failure"
	KindRecovery = "recovery"
)

// OutboxKind is the outbox row kind for incident events.
const OutboxKind = "event"

// Bounds for sanitized message fields.
const (
	maxTokenLen = 64
	maxLineLen  = 200
)

// localTimeLayout always renders a numeric UTC offset ("+00:00" for UTC,
// never "Z"), as the spec requires for displayed local timestamps.
const localTimeLayout = "2006-01-02T15:04:05-07:00"

// Logger is the logging surface used by the service (satisfied by *logging.Logger).
type Logger interface {
	Printf(format string, v ...any)
}

type discardLogger struct{}

func (discardLogger) Printf(string, ...any) {}

// Service is the incident service. It implements scheduler.Observer.
type Service struct {
	host  string
	store model.Store
	clock model.Clock
	log   Logger
}

// New creates the incident service for one host. host, store and clock are required.
func New(host string, store model.Store, clock model.Clock, log Logger) (*Service, error) {
	if host == "" {
		return nil, errors.New("incidents: host_name is required")
	}
	if store == nil {
		return nil, errors.New("incidents: store is required")
	}
	if clock == nil {
		return nil, errors.New("incidents: clock is required")
	}
	if log == nil {
		log = discardLogger{}
	}
	return &Service{host: host, store: store, clock: clock, log: log}, nil
}

// Host returns the configured host name the service keys incidents by.
func (s *Service) Host() string { return s.host }

// Observe records one observation and performs the incident transition and
// event enqueue atomically via the store. It is safe to call from multiple
// workers: the scheduler never runs the same check concurrently, and distinct
// checks key distinct incident rows.
func (s *Service) Observe(ctx context.Context, r model.CheckResult) error {
	if r.CheckID == "" {
		return errors.New("incidents: observation without check ID")
	}

	open, err := s.openIncident(ctx, r.CheckID)
	if err != nil {
		return fmt.Errorf("incidents: read incident state for check %s: %w", sanitizeToken(r.CheckID), err)
	}

	if r.Success {
		if open == nil {
			// Success without an open incident: recorded only, no event, no
			// incident write.
			return s.store.RecordObservation(ctx, r)
		}
		resolved := r.CompletedAt.UTC()
		mutation := model.IncidentMutation{
			HostName:   s.host,
			CheckID:    r.CheckID,
			Open:       false,
			ReasonCode: r.ReasonCode,
			Reason:     r.Reason,
			ResolvedAt: &resolved,
		}
		ev := s.event(KindRecovery, r, open.OpenedAt)
		s.log.Printf("incidents: recovery host=%s check=%s incident_started=%s",
			sanitizeToken(s.host), sanitizeToken(r.CheckID), formatLocal(open.OpenedAt))
		return s.store.RecordObservationAndIncident(ctx, r, mutation, []model.OutboxEvent{ev})
	}

	// Failure: always exactly one distinct failure event; the first failure
	// opens the incident, repeats only update last-failure time/reason.
	mutation := model.IncidentMutation{
		HostName:    s.host,
		CheckID:     r.CheckID,
		Open:        true,
		LastFailure: r.CompletedAt.UTC(),
		ReasonCode:  r.ReasonCode,
		Reason:      r.Reason,
	}
	ev := s.event(KindFailure, r, time.Time{})
	if open == nil {
		s.log.Printf("incidents: opened incident host=%s check=%s reason_code=%s",
			sanitizeToken(s.host), sanitizeToken(r.CheckID), sanitizeToken(r.ReasonCode))
	}
	return s.store.RecordObservationAndIncident(ctx, r, mutation, []model.OutboxEvent{ev})
}

// openIncident returns the currently open incident for checkID, or nil.
func (s *Service) openIncident(ctx context.Context, checkID string) (*model.IncidentSummary, error) {
	snap, err := s.store.Snapshot(ctx, s.clock.Now().UTC())
	if err != nil {
		return nil, err
	}
	for i := range snap.Incidents {
		inc := &snap.Incidents[i]
		if inc.CheckID == checkID && inc.ResolvedAt == nil {
			return inc, nil
		}
	}
	return nil, nil
}

// event builds the single outbox event for one observation. The event ID is
// unique per (kind, check, completion instant): a check never overlaps itself,
// so completion instants of one check are distinct.
func (s *Service) event(kind string, r model.CheckResult, incidentStarted time.Time) model.OutboxEvent {
	completed := r.CompletedAt.UTC()
	id := fmt.Sprintf("evt-%s-%s-%d", kind, sanitizeToken(r.CheckID), completed.UnixNano())
	ev := model.OutboxEvent{
		ID:         id,
		HostName:   s.host,
		Kind:       OutboxKind,
		CheckID:    r.CheckID,
		CheckType:  r.CheckType,
		IncidentID: r.CheckID,
		EnqueuedAt: s.clock.Now().UTC(),
		Payload:    s.payload(kind, r, incidentStarted),
	}
	return ev
}

// payload renders the plain-text single-line key=value message.
func (s *Service) payload(kind string, r model.CheckResult, incidentStarted time.Time) string {
	var b strings.Builder
	b.WriteString("vshm event=")
	b.WriteString(kind)
	b.WriteString(" host=")
	b.WriteString(sanitizeToken(s.host))
	b.WriteString(" check=")
	b.WriteString(sanitizeToken(r.CheckID))
	b.WriteString(" type=")
	b.WriteString(sanitizeToken(r.CheckType))
	b.WriteString(" time=")
	b.WriteString(formatLocal(r.CompletedAt))
	if kind == KindRecovery {
		b.WriteString(" incident_started=")
		b.WriteString(formatLocal(incidentStarted))
		b.WriteString(" status=ok")
	}
	b.WriteString(" reason_code=")
	b.WriteString(sanitizeToken(r.ReasonCode))
	b.WriteString(` reason="`)
	b.WriteString(sanitizeLine(r.Reason))
	b.WriteString(`"`)
	return b.String()
}

// formatLocal renders t in server-local time with a numeric UTC offset.
func formatLocal(t time.Time) string {
	return t.Local().Format(localTimeLayout)
}

// sanitizeToken keeps a value usable as a bare key=value token: only
// [a-zA-Z0-9_.-] survive, everything else becomes '_'; bounded length.
func sanitizeToken(v string) string {
	v = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9',
			r == '_', r == '.', r == '-':
			return r
		default:
			return '_'
		}
	}, v)
	if len(v) > maxTokenLen {
		v = v[:maxTokenLen]
	}
	return v
}

// sanitizeLine keeps a quoted value on one line and inside its quotes:
// control characters and '"' are replaced, length bounded.
func sanitizeLine(v string) string {
	v = strings.Map(func(r rune) rune {
		switch {
		case r == '"' || r == '\'':
			return '\''
		case r < 0x20 || r == 0x7f || !unicode.IsPrint(r):
			return ' '
		default:
			return r
		}
	}, v)
	if len(v) > maxLineLen {
		v = v[:maxLineLen]
	}
	return v
}
