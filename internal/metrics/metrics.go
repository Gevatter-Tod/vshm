// Package metrics provides report-only Linux resource metrics collection.
//
// Metrics are sampled when a daily report is rendered (the caller passes the
// render instant) and are never used to create check failures, incidents, or
// alerts: this package produces only a Snapshot and its rendered text and has
// no access to the Store, incident service, or outbox.
//
// Every metric carries an explicit availability flag. An unavailable source
// or mount is reported as UNAVAILABLE with a short sanitized reason — never as
// zero or a guessed value. All persisted/rendered instants are UTC.
//
// Linux amd64 only. Mount statistics use the pure-Go stdlib syscall.Statfs, so
// the CGO_ENABLED=0 build stays C-free and no extra module dependency is needed.
package metrics

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DefaultMounts is used when [metrics].mounts is omitted or empty.
// Decision (user-confirmed): the root mount is always reported.
var DefaultMounts = []string{"/"}

// Bounded reason codes for unavailable metrics.
const (
	ReasonOK            = "ok"
	ReasonReadFailed    = "read_failed"
	ReasonParseFailed   = "parse_failed"
	ReasonMissingField  = "missing_field"
	ReasonStatvfsFailed = "statvfs_failed"
	ReasonCancelled     = "cancelled"
	maxReasonLen        = 120
	mib                 = 1024 * 1024
)

// LoadAvgSample is a raw /proc/loadavg sample.
type LoadAvgSample struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// MeminfoSample is a raw /proc/meminfo sample, already converted to bytes.
type MeminfoSample struct {
	TotalBytes     uint64
	AvailableBytes uint64
}

// StatfsSample is a raw statvfs sample, already converted to bytes.
type StatfsSample struct {
	TotalBytes     uint64 // f_blocks * f_bsize
	FreeBytes      uint64 // f_bfree * f_bsize
	AvailableBytes uint64 // f_bavail * f_bsize (available to unprivileged processes)
}

// ProcReader abstracts the procfs sources so tests can inject fakes or
// fixture directories.
type ProcReader interface {
	ReadLoadAvg() (LoadAvgSample, error)
	ReadMeminfo() (MeminfoSample, error)
	ReadUptime() (time.Duration, error)
}

// Statvfs abstracts the statvfs(2) call so tests can inject fakes.
type Statvfs interface {
	Statvfs(path string) (StatfsSample, error)
}

// LoadMetric is the 1/5/15-minute load average with availability.
type LoadMetric struct {
	Available            bool
	Load1, Load5, Load15 float64
	ReasonCode           string
	Reason               string
}

// MemoryMetric is RAM total/available/used (used = total - available).
type MemoryMetric struct {
	Available      bool
	TotalBytes     uint64
	AvailableBytes uint64
	UsedBytes      uint64
	ReasonCode     string
	Reason         string
}

// MountMetric is used/total bytes for one configured mount.
type MountMetric struct {
	Mount          string
	Available      bool
	TotalBytes     uint64
	AvailableBytes uint64
	UsedBytes      uint64 // total - free (df "used" semantics, includes reserved blocks)
	ReasonCode     string
	Reason         string
}

// UptimeMetric is host uptime from /proc/uptime.
type UptimeMetric struct {
	Available  bool
	Uptime     time.Duration
	ReasonCode string
	Reason     string
}

// Snapshot is the full set of resource metrics captured at CollectedAt.
// CollectedAt is the UTC instant at which the daily report is rendered.
type Snapshot struct {
	CollectedAt time.Time
	Load        LoadMetric
	Memory      MemoryMetric
	Mounts      []MountMetric
	Uptime      UptimeMetric
}

// Collector gathers report-only system metrics from injected providers.
type Collector struct {
	proc ProcReader
	fs   Statvfs
}

// New creates a Collector with injected proc reader and statvfs provider.
func New(proc ProcReader, fs Statvfs) *Collector {
	return &Collector{proc: proc, fs: fs}
}

// NewWithDefaults creates a Collector reading the real /proc and statvfs(2).
func NewWithDefaults() *Collector {
	return New(NewProcReader("/proc"), RealStatvfs{})
}

// ResolveMounts applies the configured [metrics].mounts, defaulting to the
// root mount when the list is omitted or empty.
func ResolveMounts(mounts []string) []string {
	if len(mounts) == 0 {
		out := make([]string, len(DefaultMounts))
		copy(out, DefaultMounts)
		return out
	}
	out := make([]string, len(mounts))
	copy(out, mounts)
	return out
}

// Collect samples all metrics. now is the report-render instant and is stored
// as the UTC collection time. Each source is independent: a failing source
// marks only its own metric unavailable; Collect itself never fails the report.
func (c *Collector) Collect(ctx context.Context, mounts []string, now time.Time) Snapshot {
	snap := Snapshot{CollectedAt: now.UTC()}

	if err := ctx.Err(); err != nil {
		reason := sanitizeReason(ReasonCancelled, err)
		snap.Load = unavailableLoad(ReasonCancelled, reason)
		snap.Memory = unavailableMemory(ReasonCancelled, reason)
		snap.Uptime = unavailableUptime(ReasonCancelled, reason)
		for _, m := range ResolveMounts(mounts) {
			snap.Mounts = append(snap.Mounts, unavailableMount(m, ReasonCancelled, reason))
		}
		return snap
	}

	snap.Load = c.collectLoad(ctx)
	snap.Memory = c.collectMemory(ctx)
	snap.Uptime = c.collectUptime(ctx)
	for _, m := range ResolveMounts(mounts) {
		if ctx.Err() != nil {
			snap.Mounts = append(snap.Mounts, unavailableMount(m, ReasonCancelled, sanitizeReason(ReasonCancelled, ctx.Err())))
			continue
		}
		snap.Mounts = append(snap.Mounts, c.collectMount(m))
	}
	return snap
}

func (c *Collector) collectLoad(ctx context.Context) LoadMetric {
	if err := ctx.Err(); err != nil {
		return unavailableLoad(ReasonCancelled, sanitizeReason(ReasonCancelled, err))
	}
	s, err := c.proc.ReadLoadAvg()
	if err != nil {
		return unavailableLoad(reasonCodeOf(err), sanitizeReason(reasonCodeOf(err), err))
	}
	return LoadMetric{Available: true, Load1: s.Load1, Load5: s.Load5, Load15: s.Load15, ReasonCode: ReasonOK}
}

func (c *Collector) collectMemory(ctx context.Context) MemoryMetric {
	if err := ctx.Err(); err != nil {
		return unavailableMemory(ReasonCancelled, sanitizeReason(ReasonCancelled, err))
	}
	s, err := c.proc.ReadMeminfo()
	if err != nil {
		return unavailableMemory(reasonCodeOf(err), sanitizeReason(reasonCodeOf(err), err))
	}
	return MemoryMetric{
		Available:      true,
		TotalBytes:     s.TotalBytes,
		AvailableBytes: s.AvailableBytes,
		UsedBytes:      s.TotalBytes - s.AvailableBytes, // spec formula: used = total - available
		ReasonCode:     ReasonOK,
	}
}

func (c *Collector) collectUptime(ctx context.Context) UptimeMetric {
	if err := ctx.Err(); err != nil {
		return unavailableUptime(ReasonCancelled, sanitizeReason(ReasonCancelled, err))
	}
	d, err := c.proc.ReadUptime()
	if err != nil {
		return unavailableUptime(reasonCodeOf(err), sanitizeReason(reasonCodeOf(err), err))
	}
	return UptimeMetric{Available: true, Uptime: d, ReasonCode: ReasonOK}
}

func (c *Collector) collectMount(mount string) MountMetric {
	s, err := c.fs.Statvfs(mount)
	if err != nil {
		return unavailableMount(mount, ReasonStatvfsFailed, sanitizeReason(ReasonStatvfsFailed, err))
	}
	return MountMetric{
		Mount:          mount,
		Available:      true,
		TotalBytes:     s.TotalBytes,
		AvailableBytes: s.AvailableBytes,
		UsedBytes:      s.TotalBytes - s.FreeBytes,
		ReasonCode:     ReasonOK,
	}
}

func unavailableLoad(code, reason string) LoadMetric {
	return LoadMetric{ReasonCode: code, Reason: reason}
}

func unavailableMemory(code, reason string) MemoryMetric {
	return MemoryMetric{ReasonCode: code, Reason: reason}
}

func unavailableUptime(code, reason string) UptimeMetric {
	return UptimeMetric{ReasonCode: code, Reason: reason}
}

func unavailableMount(mount, code, reason string) MountMetric {
	return MountMetric{Mount: mount, ReasonCode: code, Reason: reason}
}

// errMissingField marks parse errors caused by a missing proc field.
type errMissingField struct{ field string }

func (e errMissingField) Error() string { return "missing field: " + e.field }

// errParseFailed marks malformed proc content.
type errParseFailed struct{}

func (errParseFailed) Error() string { return "malformed proc content" }

// reasonCodeOf maps an error to a bounded reason code.
func reasonCodeOf(err error) string {
	var mf errMissingField
	if errors.As(err, &mf) {
		return ReasonMissingField
	}
	var pf errParseFailed
	if errors.As(err, &pf) {
		return ReasonParseFailed
	}
	return ReasonReadFailed
}

// sanitizeReason produces a short, single-line, bounded, non-secret reason
// string of the form "<code>: <err>".
func sanitizeReason(code string, err error) string {
	msg := err.Error()
	msg = strings.ReplaceAll(msg, "\n", " ")
	msg = strings.ReplaceAll(msg, "\r", " ")
	r := code + ": " + msg
	if len(r) > maxReasonLen {
		r = r[:maxReasonLen-3] + "..."
	}
	return r
}

// ProcFS reads proc sources from a root directory (default /proc), which lets
// tests point the reader at fixture trees.
type ProcFS struct {
	Root string
}

// NewProcReader returns a ProcReader reading from root (use "/proc" in
// production).
func NewProcReader(root string) ProcReader {
	return ProcFS{Root: root}
}

func (p ProcFS) read(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join(p.Root, name))
}

// ReadLoadAvg parses /proc/loadavg: "<load1> <load5> <load15> <running>/<total> ...".
func (p ProcFS) ReadLoadAvg() (LoadAvgSample, error) {
	data, err := p.read("loadavg")
	if err != nil {
		return LoadAvgSample{}, err
	}
	return parseLoadAvg(data)
}

// ReadMeminfo parses MemTotal/MemAvailable (kB) from /proc/meminfo into bytes.
// Both fields are required: a missing field is an error, never a guessed zero.
func (p ProcFS) ReadMeminfo() (MeminfoSample, error) {
	data, err := p.read("meminfo")
	if err != nil {
		return MeminfoSample{}, err
	}
	return parseMeminfo(data)
}

// ReadUptime parses the first field (seconds) of /proc/uptime.
func (p ProcFS) ReadUptime() (time.Duration, error) {
	data, err := p.read("uptime")
	if err != nil {
		return 0, err
	}
	return parseUptime(data)
}

func parseLoadAvg(data []byte) (LoadAvgSample, error) {
	fields := strings.Fields(string(data))
	if len(fields) < 3 {
		return LoadAvgSample{}, fmt.Errorf("%w: loadavg needs 3 fields, got %d", errParseFailed{}, len(fields))
	}
	var s LoadAvgSample
	var err error
	if s.Load1, err = strconv.ParseFloat(fields[0], 64); err != nil {
		return LoadAvgSample{}, fmt.Errorf("%w: load1 %q", errParseFailed{}, fields[0])
	}
	if s.Load5, err = strconv.ParseFloat(fields[1], 64); err != nil {
		return LoadAvgSample{}, fmt.Errorf("%w: load5 %q", errParseFailed{}, fields[1])
	}
	if s.Load15, err = strconv.ParseFloat(fields[2], 64); err != nil {
		return LoadAvgSample{}, fmt.Errorf("%w: load15 %q", errParseFailed{}, fields[2])
	}
	return s, nil
}

func parseMeminfo(data []byte) (MeminfoSample, error) {
	var total, avail *uint64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		if key != "MemTotal" && key != "MemAvailable" {
			continue
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil {
			return MeminfoSample{}, fmt.Errorf("%w: %s value %q", errParseFailed{}, key, fields[1])
		}
		if len(fields) < 3 || !strings.EqualFold(fields[2], "kB") {
			return MeminfoSample{}, fmt.Errorf("%w: %s unit %q is not kB", errParseFailed{}, key, strings.Join(fields[2:], " "))
		}
		kb := value * 1024
		switch key {
		case "MemTotal":
			total = &kb
		case "MemAvailable":
			avail = &kb
		}
	}
	if total == nil {
		return MeminfoSample{}, errMissingField{field: "MemTotal"}
	}
	if avail == nil {
		return MeminfoSample{}, errMissingField{field: "MemAvailable"}
	}
	return MeminfoSample{TotalBytes: *total, AvailableBytes: *avail}, nil
}

func parseUptime(data []byte) (time.Duration, error) {
	fields := strings.Fields(string(data))
	if len(fields) < 1 {
		return 0, fmt.Errorf("%w: uptime is empty", errParseFailed{})
	}
	seconds, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || seconds < 0 {
		return 0, fmt.Errorf("%w: uptime %q", errParseFailed{}, fields[0])
	}
	return time.Duration(seconds * float64(time.Second)), nil
}

// RealStatvfs implements Statvfs with the pure-Go stdlib syscall.Statfs, which
// requires no CGO and works in the CGO_ENABLED=0 Linux amd64 build.
type RealStatvfs struct{}

// Statvfs calls statvfs(2) and converts the result to bytes.
func (RealStatvfs) Statvfs(path string) (StatfsSample, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return StatfsSample{}, err
	}
	if st.Bsize <= 0 {
		return StatfsSample{}, fmt.Errorf("invalid block size %d for %q", st.Bsize, path)
	}
	bsize := uint64(st.Bsize)
	return StatfsSample{
		TotalBytes:     uint64(st.Blocks) * bsize,
		FreeBytes:      uint64(st.Bfree) * bsize,
		AvailableBytes: uint64(st.Bavail) * bsize,
	}, nil
}

// FormatLoad formats a load average with two decimals.
func FormatLoad(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }

// FormatMiB formats a byte count with an explicit MiB unit.
func FormatMiB(b uint64) string {
	return fmt.Sprintf("%.1f MiB", float64(b)/float64(mib))
}

// FormatUptime formats an uptime with an explicit seconds unit.
func FormatUptime(d time.Duration) string {
	return fmt.Sprintf("%ds", int64(d.Seconds()))
}

// Render renders the snapshot as a plain-text metrics block with explicit
// units. Unavailable metrics render as UNAVAILABLE(<reason>), never zero.
func Render(s Snapshot) string {
	var sb strings.Builder
	sb.WriteString("metrics:\n")
	sb.WriteString("  collected_at=" + s.CollectedAt.UTC().Format(time.RFC3339) + "\n")

	if s.Load.Available {
		sb.WriteString(fmt.Sprintf("  load1=%s load5=%s load15=%s\n",
			FormatLoad(s.Load.Load1), FormatLoad(s.Load.Load5), FormatLoad(s.Load.Load15)))
	} else {
		sb.WriteString(fmt.Sprintf("  load=UNAVAILABLE(%s)\n", s.Load.Reason))
	}

	if s.Memory.Available {
		sb.WriteString(fmt.Sprintf("  mem_total=%s mem_available=%s mem_used=%s\n",
			FormatMiB(s.Memory.TotalBytes), FormatMiB(s.Memory.AvailableBytes), FormatMiB(s.Memory.UsedBytes)))
	} else {
		sb.WriteString(fmt.Sprintf("  mem=UNAVAILABLE(%s)\n", s.Memory.Reason))
	}

	for _, m := range s.Mounts {
		if m.Available {
			sb.WriteString(fmt.Sprintf("  mount %s: total=%s available=%s used=%s\n",
				m.Mount, FormatMiB(m.TotalBytes), FormatMiB(m.AvailableBytes), FormatMiB(m.UsedBytes)))
		} else {
			sb.WriteString(fmt.Sprintf("  mount %s: UNAVAILABLE(%s)\n", m.Mount, m.Reason))
		}
	}

	if s.Uptime.Available {
		sb.WriteString(fmt.Sprintf("  uptime=%s\n", FormatUptime(s.Uptime.Uptime)))
	} else {
		sb.WriteString(fmt.Sprintf("  uptime=UNAVAILABLE(%s)\n", s.Uptime.Reason))
	}

	return sb.String()
}
