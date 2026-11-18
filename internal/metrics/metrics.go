// Package metrics provides Linux system metrics collection for reporting.
// It collects load average, memory usage, filesystem metrics, and uptime.
// Metrics are report-only: never create check failures, incidents, or alerts.
package metrics

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/logging"
)

// LoadAvgResult represents load average data.
type LoadAvgResult struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// MeminfoResult represents memory info data.
type MeminfoResult struct {
	MemTotal     *uint64
	MemAvailable *uint64
}

// FSProvider provides procfs-style data access.
type FSProvider interface {
	LoadAvg() (*LoadAvgResult, error)
	Meminfo() (*MeminfoResult, error)
}

// StatfsProvider provides filesystem statfs operations.
type StatfsProvider interface {
	Statvfs(path string, buf *syscall.Statfs_t) error
}

// UptimeProvider reads uptime from /proc/uptime.
type UptimeProvider interface {
	ReadUptime() (time.Duration, error)
}

// HostnameProvider reads the system hostname.
type HostnameProvider interface {
	ReadHostname() (string, error)
}

// Collector collects system metrics.
type Collector struct {
	logger     *logging.Logger
	fsProvider FSProvider
	statfs     StatfsProvider
	uptime     UptimeProvider
	hostname   HostnameProvider
}

// MemoryMetrics holds memory information.
type MemoryMetrics struct {
	TotalBytes     uint64
	AvailableBytes uint64
	UsedBytes      uint64
	UsedPercent    float64
	TotalMiB       float64
	AvailableMiB   float64
	UsedMiB        float64
	Unavailable    bool
	Reason         string
}

// FilesystemMetrics holds filesystem information.
type FilesystemMetrics struct {
	MountPoint       string
	TotalBytes       uint64
	AvailableBytes   uint64
	UsedBytes        uint64
	UsedPercent      float64
	AvailablePercent float64
	TotalMiB         float64
	AvailableMiB     float64
	UsedMiB          float64
	Unavailable      bool
	ReasonCode       string
	Reason           string
}

// MetricsSnapshot holds all collected system metrics.
type MetricsSnapshot struct {
	HostName          string
	LoadAvg1Min       float64
	LoadAvg5Min       float64
	LoadAvg15Min      float64
	LoadUnavailable   bool
	LoadReason        string
	Memory            MemoryMetrics
	FS                []FilesystemMetrics
	Uptime            time.Duration
	UptimeUnavailable bool
	UptimeReason      string
	CollectionTime    time.Time
}

// New creates a new metrics collector with injected dependencies.
func New(logger *logging.Logger, fsProvider FSProvider, statfs StatfsProvider) *Collector {
	return &Collector{
		logger:     logger,
		fsProvider: fsProvider,
		statfs:     statfs,
		uptime:     &defaultUptimeProvider{},
		hostname:   &defaultHostnameProvider{},
	}
}

// NewWithDefaults creates a new metrics collector with default dependencies.
func NewWithDefaults(logger *logging.Logger) *Collector {
	return New(logger, nil, nil)
}

// Collect gathers all system metrics and returns a snapshot.
func (c *Collector) Collect(ctx context.Context, cfg config.MetricsConfig, now time.Time) (*MetricsSnapshot, error) {
	snap := &MetricsSnapshot{CollectionTime: now}

	snap.HostName, _ = c.hostname.ReadHostname()

	if err := c.collectLoadAverage(snap); err != nil {
		c.logger.Printf("[metrics] Load average unavailable: %v", err)
	}

	if err := c.collectMemory(snap); err != nil {
		c.logger.Printf("[metrics] Memory metrics unavailable: %v", err)
	}

	if err := c.collectFilesystems(cfg, snap); err != nil {
		c.logger.Printf("[metrics] Filesystem metrics unavailable: %v", err)
	}

	if err := c.collectUptime(snap); err != nil {
		c.logger.Printf("[metrics] Uptime unavailable: %v", err)
	}

	return snap, nil
}

// collectLoadAverage reads load averages from /proc/loadavg.
func (c *Collector) collectLoadAverage(snap *MetricsSnapshot) error {
	var loadAvg *LoadAvgResult
	var err error

	if c.fsProvider != nil {
		loadAvg, err = c.fsProvider.LoadAvg()
	} else {
		loadAvg, err = getDefaultFS().LoadAvg()
	}

	if err != nil {
		snap.LoadUnavailable = true
		snap.LoadReason = fmt.Sprintf("load average unavailable: %v", err)
		return fmt.Errorf("load average unavailable: %w", err)
	}

	snap.LoadAvg1Min = loadAvg.Load1
	snap.LoadAvg5Min = loadAvg.Load5
	snap.LoadAvg15Min = loadAvg.Load15
	return nil
}

// collectMemory reads memory information from /proc/meminfo.
func (c *Collector) collectMemory(snap *MetricsSnapshot) error {
	var meminfo *MeminfoResult
	var err error

	if c.fsProvider != nil {
		meminfo, err = c.fsProvider.Meminfo()
	} else {
		meminfo, err = getDefaultFS().Meminfo()
	}

	if err != nil {
		snap.Memory.Unavailable = true
		snap.Memory.Reason = fmt.Sprintf("memory metrics unavailable: %v", err)
		return fmt.Errorf("memory metrics unavailable: %w", err)
	}

	if meminfo.MemTotal == nil {
		snap.Memory.Unavailable = true
		snap.Memory.Reason = "MemTotal not available in /proc/meminfo"
		return fmt.Errorf("MemTotal not available in /proc/meminfo")
	}

	// Convert from KiB to bytes
	memTotalKB := *meminfo.MemTotal
	memAvailKB := uint64(0)
	if meminfo.MemAvailable != nil {
		memAvailKB = *meminfo.MemAvailable
	}

	snap.Memory.TotalBytes = memTotalKB * 1024
	snap.Memory.AvailableBytes = memAvailKB * 1024
	snap.Memory.UsedBytes = snap.Memory.TotalBytes - snap.Memory.AvailableBytes

	if snap.Memory.TotalBytes > 0 {
		snap.Memory.UsedPercent = float64(snap.Memory.UsedBytes) / float64(snap.Memory.TotalBytes) * 100
	}

	snap.Memory.TotalMiB = roundTo2(float64(snap.Memory.TotalBytes) / (1024 * 1024))
	snap.Memory.AvailableMiB = roundTo2(float64(snap.Memory.AvailableBytes) / (1024 * 1024))
	snap.Memory.UsedMiB = roundTo2(float64(snap.Memory.UsedBytes) / (1024 * 1024))

	return nil
}

// collectFilesystems reads filesystem metrics for configured mounts.
func (c *Collector) collectFilesystems(cfg config.MetricsConfig, snap *MetricsSnapshot) error {
	mounts := cfg.Mounts
	if len(mounts) == 0 {
		mounts = []string{"/"}
	}

	for _, mount := range mounts {
		fsMetrics := c.collectFilesystem(mount)
		snap.FS = append(snap.FS, fsMetrics)
	}

	return nil
}

// collectFilesystem reads metrics for a single mount point.
func (c *Collector) collectFilesystem(mount string) FilesystemMetrics {
	fs := FilesystemMetrics{
		MountPoint: mount,
	}

	var stat syscall.Statfs_t
	var err error

	if c.statfs != nil {
		err = c.statfs.Statvfs(mount, &stat)
	} else {
		err = syscall.Statfs(mount, &stat)
	}

	if err != nil {
		fs.Unavailable = true
		fs.ReasonCode = "statvfs_failed"
		fs.Reason = fmt.Sprintf("statvfs failed for %q: %v", mount, err)
		return fs
	}

	// statfs: f_blocks = total blocks, f_bavail = free blocks (non-privileged), f_bsize = block size
	totalBytes := uint64(stat.Blocks) * uint64(stat.Bsize)
	availBytes := uint64(stat.Bavail) * uint64(stat.Bsize)
	usedBytes := totalBytes - availBytes

	fs.TotalBytes = totalBytes
	fs.AvailableBytes = availBytes
	fs.UsedBytes = usedBytes

	if totalBytes > 0 {
		fs.UsedPercent = float64(usedBytes) / float64(totalBytes) * 100
		fs.AvailablePercent = float64(availBytes) / float64(totalBytes) * 100
	}

	fs.TotalMiB = roundTo2(float64(totalBytes) / (1024 * 1024))
	fs.AvailableMiB = roundTo2(float64(availBytes) / (1024 * 1024))
	fs.UsedMiB = roundTo2(float64(usedBytes) / (1024 * 1024))

	fs.ReasonCode = "success"
	fs.Reason = fmt.Sprintf("filesystem is mounted at %q", mount)
	return fs
}

// collectUptime reads uptime from /proc/uptime.
func (c *Collector) collectUptime(snap *MetricsSnapshot) error {
	d, err := c.uptime.ReadUptime()
	if err != nil {
		snap.UptimeUnavailable = true
		snap.UptimeReason = fmt.Sprintf("uptime unavailable: %v", err)
		return fmt.Errorf("uptime unavailable: %w", err)
	}

	snap.Uptime = d
	return nil
}

// FormatLoadAverage returns a human-readable load average string.
func FormatLoadAverage(load1 float64) string {
	return fmt.Sprintf("%.2f", load1)
}

// FormatBytes returns a human-readable byte string with MiB units.
func FormatBytes(bytes uint64) string {
	miB := float64(bytes) / (1024 * 1024)
	return fmt.Sprintf("%.1f MiB", miB)
}

// FormatPercent returns a formatted percentage string.
func FormatPercent(pct float64) string {
	return fmt.Sprintf("%.1f%%", pct)
}

// FormatUptime returns a human-readable uptime string (d h m s).
func FormatUptime(uptime time.Duration) string {
	days := int(uptime.Hours() / 24)
	hours := int(uptime.Hours()) % 24
	minutes := int(uptime.Minutes()) % 60
	seconds := int(uptime.Seconds()) % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, seconds)
	}
	return fmt.Sprintf("%dh %dm %ds", hours, minutes, seconds)
}

// RenderReport renders metrics snapshot into a report string.
func RenderReport(snap *MetricsSnapshot) string {
	var sb strings.Builder

	sb.WriteString("=== System Metrics ===\n\n")

	// Hostname
	sb.WriteString(fmt.Sprintf("Host: %s\n", snap.HostName))
	sb.WriteString(fmt.Sprintf("Collected at: %s\n\n", snap.CollectionTime.UTC().Format(time.RFC3339)))

	// Load average
	if snap.LoadUnavailable {
		sb.WriteString(fmt.Sprintf("Load average: UNAVAILABLE (%s)\n\n", snap.LoadReason))
	} else {
		sb.WriteString(fmt.Sprintf("Load average (1m/5m/15m): %s / %s / %s\n\n",
			FormatLoadAverage(snap.LoadAvg1Min),
			FormatLoadAverage(snap.LoadAvg5Min),
			FormatLoadAverage(snap.LoadAvg15Min)))
	}

	// Memory
	sb.WriteString("Memory:\n")
	if snap.Memory.Unavailable {
		sb.WriteString(fmt.Sprintf("  Total/Available: UNAVAILABLE (%s)\n", snap.Memory.Reason))
	} else {
		sb.WriteString(fmt.Sprintf("  Total: %s\n", FormatBytes(snap.Memory.TotalBytes)))
		sb.WriteString(fmt.Sprintf("  Available: %s\n", FormatBytes(snap.Memory.AvailableBytes)))
		sb.WriteString(fmt.Sprintf("  Used: %s (%s)\n", FormatBytes(snap.Memory.UsedBytes), FormatPercent(snap.Memory.UsedPercent)))
	}
	sb.WriteString("\n")

	// Filesystems
	sb.WriteString("Filesystems:\n")
	if len(snap.FS) == 0 {
		sb.WriteString("  No filesystems configured for metrics\n")
	} else {
		for _, fs := range snap.FS {
			sb.WriteString(fmt.Sprintf("  %s:\n", fs.MountPoint))
			if fs.Unavailable {
				sb.WriteString(fmt.Sprintf("    Status: UNAVAILABLE (%s)\n", fs.Reason))
			} else {
				sb.WriteString(fmt.Sprintf("    Total: %s\n", FormatBytes(fs.TotalBytes)))
				sb.WriteString(fmt.Sprintf("    Available: %s (%s)\n", FormatBytes(fs.AvailableBytes), FormatPercent(fs.AvailablePercent)))
				sb.WriteString(fmt.Sprintf("    Used: %s (%s)\n", FormatBytes(fs.UsedBytes), FormatPercent(fs.UsedPercent)))
			}
		}
	}
	sb.WriteString("\n")

	// Uptime
	sb.WriteString("Uptime:\n")
	if snap.UptimeUnavailable {
		sb.WriteString(fmt.Sprintf("  UNAVAILABLE (%s)\n", snap.UptimeReason))
	} else {
		sb.WriteString(fmt.Sprintf("  %s\n", FormatUptime(snap.Uptime)))
	}

	return sb.String()
}

// Default implementations

type defaultFS struct {
	loadAvg  *LoadAvgResult
	meminfo  *MeminfoResult
	loadErr  error
	memErr   error
	loaded   bool
}

func (f *defaultFS) LoadAvg() (*LoadAvgResult, error) {
	if f.loaded {
		return f.loadAvg, f.loadErr
	}
	f.loaded = true

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		f.loadErr = err
		return nil, err
	}

	var load1, load5, load15 float64
	n, err := fmt.Sscanf(string(data), "%f %f %f", &load1, &load5, &load15)
	if err != nil || n < 1 {
		f.loadErr = err
		return nil, err
	}

	result := &LoadAvgResult{Load1: load1, Load5: load5, Load15: load15}
	f.loadAvg = result
	return result, nil
}

func (f *defaultFS) Meminfo() (*MeminfoResult, error) {
	if f.loaded {
		return f.meminfo, f.memErr
	}
	f.loaded = true

	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		f.memErr = err
		return nil, err
	}

	result := &MeminfoResult{}
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		var key string
		var value uint64
		n, err := fmt.Sscanf(line, "MemTotal: %d kB", &value)
		if err == nil && n == 1 {
			result.MemTotal = &value
			continue
		}
		n, err = fmt.Sscanf(line, "MemAvailable: %d kB", &value)
		if err == nil && n == 1 {
			result.MemAvailable = &value
		}
	}

	if result.MemTotal == nil {
		f.memErr = fmt.Errorf("MemTotal not found in /proc/meminfo")
		return nil, f.memErr
	}

	f.meminfo = result
	return result, nil
}

type defaultUptimeProvider struct{}

func (p *defaultUptimeProvider) ReadUptime() (time.Duration, error) {
	data, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, fmt.Errorf("read /proc/uptime: %w", err)
	}

	var seconds float64
	n, err := fmt.Sscanf(string(data), "%f", &seconds)
	if err != nil || n != 1 {
		return 0, fmt.Errorf("parse /proc/uptime: invalid format")
	}

	return time.Duration(seconds * float64(time.Second)), nil
}

type defaultHostnameProvider struct{}

func (p *defaultHostnameProvider) ReadHostname() (string, error) {
	data, err := os.ReadFile("/etc/hostname")
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	data, err = os.ReadFile("/proc/sys/kernel/hostname")
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	return "unknown", nil
}

// getDefaultFS returns a default FSProvider.
var _defaultFSOnce syncOnce
var _defaultFS defaultFS

func getDefaultFS() *defaultFS {
	_defaultFSOnce.Do(func() {
		// Initialize with defaults
	})
	return &_defaultFS
}

// syncOnce provides a simple sync.Once-like pattern.
type syncOnce struct {
	done bool
}

func (o *syncOnce) Do(f func()) {
	if o.done {
		return
	}
	f()
	o.done = true
}

// roundTo2 rounds a float to 2 decimal places.
func roundTo2(f float64) float64 {
	return math.Round(f*100) / 100
}
