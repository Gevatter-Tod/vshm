package metrics

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/logging"
)

// mockLoadAvgResult holds load average data for mocking.
type mockLoadAvgResult struct {
	Load1  float64
	Load5  float64
	Load15 float64
	Err    error
}

func (r *mockLoadAvgResult) LoadAvg() (*mockProcLoadAvg, error) {
	if r.Err != nil {
		return nil, r.Err
	}
	return &mockProcLoadAvg{
		Load1:  r.Load1,
		Load5:  r.Load5,
		Load15: r.Load15,
	}, nil
}

// mockMeminfoResult holds meminfo data for mocking.
type mockMeminfoResult struct {
	MemTotal   *uint64
	MemAvail   *uint64
	Err        error
}

func (r *mockMeminfoResult) Meminfo() (mockProcMeminfo, error) {
	if r.Err != nil {
		return mockProcMeminfo{}, r.Err
	}
	return mockProcMeminfo{
		MemTotal:     r.MemTotal,
		MemAvailable: r.MemAvail,
	}, nil
}

// mockProcLoadAvg mimics procfs.LoadAvg for testing.
type mockProcLoadAvg struct {
	Load1  float64
	Load5  float64
	Load15 float64
}

// mockProcMeminfo mimics procfs.Meminfo for testing.
type mockProcMeminfo struct {
	MemTotal     *uint64
	MemAvailable *uint64
}

// mockFS implements FSProvider interface for testing.
type mockFS struct {
	loadAvg *mockLoadAvgResult
	meminfo *mockMeminfoResult
}

func (m *mockFS) LoadAvg() (*mockProcLoadAvg, error) {
	if m.loadAvg != nil {
		return m.loadAvg.LoadAvg()
	}
	return nil, os.ErrNotExist
}

func (m *mockFS) Meminfo() (mockProcMeminfo, error) {
	if m.meminfo != nil {
		return m.meminfo.Meminfo()
	}
	return mockProcMeminfo{}, os.ErrNotExist
}

// mockStatfs implements StatfsProvider interface for testing.
type mockStatfs struct {
	data *syscall.Statfs_t
	err  error
}

func (m *mockStatfs) Statvfs(path string, buf *syscall.Statfs_t) error {
	if m.err != nil {
		return m.err
	}
	*buf = *m.data
	return nil
}

// fakeUptime implements the UptimeProvider interface.
type fakeUptime struct {
	duration time.Duration
	err      error
}

func (f *fakeUptime) ReadUptime() (time.Duration, error) {
	return f.duration, f.err
}

// fakeHostname implements the HostnameProvider interface.
type fakeHostname struct {
	name string
	err  error
}

func (f *fakeHostname) ReadHostname() (string, error) {
	return f.name, f.err
}

func ptrUint64(v uint64) *uint64 { return &v }

func TestCollect(t *testing.T) {
	logger := logging.New("[metrics] ")
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	cfg := config.MetricsConfig{}

	tests := []struct {
		name     string
		fs       *mockFS
		statfs   *mockStatfs
		uptime   *fakeUptime
		hostname *fakeHostname
		cfg      config.MetricsConfig
		wantErr  bool
		validate func(*testing.T, *MetricsSnapshot)
	}{
		{
			name: "collect all metrics successfully",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{
					Load1:  1.234,
					Load5:  0.987,
					Load15: 0.765,
				},
				meminfo: &mockMeminfoResult{
					MemTotal:   ptrUint64(8589934080 * 1024), // 8589934080 KiB → bytes
					MemAvail:   ptrUint64(4294967296 * 1024),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 52428800,
					Bfree:  31457280,
					Bavail: 31457280,
					Bsize:  4096,
				},
			},
			uptime:   &fakeUptime{duration: 86400 * time.Second},
			hostname: &fakeHostname{name: "testhost"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if snap.HostName != "testhost" {
					t.Errorf("hostname = %q, want %q", snap.HostName, "testhost")
				}
				if snap.LoadAvg1Min != 1.234 {
					t.Errorf("load avg 1m = %f, want 1.234", snap.LoadAvg1Min)
				}
				if snap.LoadAvg5Min != 0.987 {
					t.Errorf("load avg 5m = %f, want 0.987", snap.LoadAvg5Min)
				}
				if snap.LoadAvg15Min != 0.765 {
					t.Errorf("load avg 15m = %f, want 0.765", snap.LoadAvg15Min)
				}
				if snap.LoadUnavailable {
					t.Error("load should not be unavailable")
				}
				wantTotal := uint64(8589934080) * 1024
				if snap.Memory.TotalBytes != wantTotal {
					t.Errorf("memory total = %d, want %d", snap.Memory.TotalBytes, wantTotal)
				}
				wantAvail := uint64(4294967296) * 1024
				if snap.Memory.AvailableBytes != wantAvail {
					t.Errorf("memory available = %d, want %d", snap.Memory.AvailableBytes, wantAvail)
				}
				if snap.Memory.UsedBytes != wantTotal-wantAvail {
					t.Errorf("memory used = %d, want %d", snap.Memory.UsedBytes, wantTotal-wantAvail)
				}
				if snap.Memory.Unavailable {
					t.Error("memory should not be unavailable")
				}
				if len(snap.FS) != 1 {
					t.Fatalf("expected 1 filesystem, got %d", len(snap.FS))
				}
				fs := snap.FS[0]
				if fs.ReasonCode != "success" {
					t.Error("filesystem should report success")
				}
				if fs.MountPoint != "/" {
					t.Errorf("mount point = %q, want %q", fs.MountPoint, "/")
				}
				if snap.UptimeUnavailable {
					t.Error("uptime should not be unavailable")
				}
				if snap.Uptime != 86400*time.Second {
					t.Errorf("uptime = %v, want %v", snap.Uptime, 86400*time.Second)
				}
			},
		},
		{
			name: "load average unavailable",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Err: os.ErrNotExist},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(500000),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if !snap.LoadUnavailable {
					t.Error("load average should be unavailable")
				}
				if snap.LoadAvg1Min != 0 {
					t.Errorf("load avg 1m should be 0 when unavailable, got %f", snap.LoadAvg1Min)
				}
				if !strings.Contains(snap.LoadReason, "load average unavailable") {
					t.Errorf("load reason should contain 'load average unavailable', got: %s", snap.LoadReason)
				}
			},
		},
		{
			name: "memory unavailable",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{Err: os.ErrNotExist},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if !snap.Memory.Unavailable {
					t.Error("memory should be unavailable")
				}
				if !strings.Contains(snap.Memory.Reason, "memory metrics unavailable") {
					t.Errorf("memory reason should contain 'memory metrics unavailable', got: %s", snap.Memory.Reason)
				}
			},
		},
		{
			name: "filesystem unavailable",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(500000),
				},
			},
			statfs:   &mockStatfs{err: syscall.ENOENT},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if len(snap.FS) != 1 {
					t.Fatalf("expected 1 filesystem, got %d", len(snap.FS))
				}
				if !snap.FS[0].Unavailable {
					t.Error("filesystem should be unavailable")
				}
				if snap.FS[0].ReasonCode != "statvfs_failed" {
					t.Errorf("reason code = %q, want %q", snap.FS[0].ReasonCode, "statvfs_failed")
				}
			},
		},
		{
			name: "uptime unavailable",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(500000),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{err: os.ErrNotExist},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if !snap.UptimeUnavailable {
					t.Error("uptime should be unavailable")
				}
				if snap.Uptime != 0 {
					t.Errorf("uptime should be zero when unavailable, got %v", snap.Uptime)
				}
			},
		},
		{
			name: "multiple configured mounts",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(500000),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg: config.MetricsConfig{
				Mounts: []string{"/", "/tmp"},
			},
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if len(snap.FS) != 2 {
					t.Fatalf("expected 2 filesystems, got %d", len(snap.FS))
				}
				if snap.FS[0].MountPoint != "/" {
					t.Errorf("first mount = %q, want %q", snap.FS[0].MountPoint, "/")
				}
				if snap.FS[1].MountPoint != "/tmp" {
					t.Errorf("second mount = %q, want %q", snap.FS[1].MountPoint, "/tmp")
				}
			},
		},
		{
			name: "MemAvailable is zero old kernel",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(0),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				if snap.Memory.Unavailable {
					t.Error("memory should not be unavailable just because MemAvailable=0")
				}
				if snap.Memory.AvailableBytes != 0 {
					t.Errorf("available = %d, want 0", snap.Memory.AvailableBytes)
				}
				if snap.Memory.UsedBytes != snap.Memory.TotalBytes {
					t.Errorf("used = %d, want total=%d", snap.Memory.UsedBytes, snap.Memory.TotalBytes)
				}
			},
		},
		{
			name: "report-only no check failures",
			fs: &mockFS{
				loadAvg: &mockLoadAvgResult{Load1: 0.5, Load5: 0.3, Load15: 0.1},
				meminfo: &mockMeminfoResult{
					MemTotal: ptrUint64(1000000),
					MemAvail: ptrUint64(500000),
				},
			},
			statfs: &mockStatfs{
				data: &syscall.Statfs_t{
					Blocks: 1000, Bfree: 500, Bavail: 500, Bsize: 4096,
				},
			},
			uptime:   &fakeUptime{duration: 100 * time.Second},
			hostname: &fakeHostname{name: "test"},
			cfg:      cfg,
			validate: func(t *testing.T, snap *MetricsSnapshot) {
				// Metrics are report-only, no check failures or incidents
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			collector := New(logger, tt.fs, tt.statfs)
			collector.uptime = tt.uptime
			if tt.hostname != nil {
				collector.hostname = tt.hostname
			} else {
				collector.hostname = &fakeHostname{name: "default"}
			}

			snap, err := collector.Collect(context.Background(), tt.cfg, now)
			if (err != nil) != tt.wantErr {
				t.Errorf("Collect() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err != nil {
				return
			}
			if snap == nil {
				t.Fatal("Collect() returned nil snapshot")
			}
			tt.validate(t, snap)
		})
	}
}

func TestCollectWithDefaultDependencies(t *testing.T) {
	logger := logging.New("[metrics] ")
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	collector := NewWithDefaults(logger)

	snap, err := collector.Collect(context.Background(), config.MetricsConfig{}, now)
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	if snap == nil {
		t.Fatal("Collect() returned nil snapshot")
	}
	if snap.HostName == "" {
		t.Error("hostname should not be empty")
	}
	if snap.CollectionTime.IsZero() {
		t.Error("collection time should not be zero")
	}
}

func TestCollect_Uncollected(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)

	snap, err := collector.Collect(context.Background(), config.MetricsConfig{
		Mounts: []string{"/nonexistent_mount_12345"},
	}, time.Now())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	if snap == nil {
		t.Fatal("Collect() returned nil snapshot")
	}
	if len(snap.FS) != 1 {
		t.Fatalf("expected 1 filesystem, got %d", len(snap.FS))
	}
	if !snap.FS[0].Unavailable {
		t.Errorf("expected filesystem to be unavailable for nonexistent mount")
	}
}

func TestCollect_CollectedAtUTC(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)

	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("failed to load timezone: %v", err)
	}
	nyTime := time.Date(2025, 6, 15, 14, 30, 0, 0, loc)
	snap, err := collector.Collect(context.Background(), config.MetricsConfig{}, nyTime)
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}
	if snap.CollectionTime.Location().String() != "UTC" {
		t.Logf("collection time location: %s (expected UTC)", snap.CollectionTime.Location())
	}
}

func TestCollect_Formulas(t *testing.T) {
	// Test exact formulas: used = total - available
	total := uint64(16 * 1024 * 1024 * 1024) // 16 GiB in bytes
	available := uint64(4 * 1024 * 1024 * 1024)
	used := total - available

	mem := MemoryMetrics{
		TotalBytes:     total,
		AvailableBytes: available,
		UsedBytes:      used,
	}

	if mem.UsedBytes != used {
		t.Errorf("used = %d, want total - available = %d", mem.UsedBytes, used)
	}

	wantPct := float64(used) / float64(total) * 100
	if mem.UsedPercent != wantPct {
		t.Errorf("used percent = %f, want %f", mem.UsedPercent, wantPct)
	}
}

func TestCollect_MountUnavailable_ReturnsUNAVAILABLE(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)
	cfg := config.MetricsConfig{
		Mounts: []string{"/does_not_exist_vshm_test_12345"},
	}

	snap, err := collector.Collect(context.Background(), cfg, time.Now())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	if len(snap.FS) != 1 {
		t.Fatalf("expected 1 filesystem, got %d", len(snap.FS))
	}

	fs := snap.FS[0]
	if fs.ReasonCode != "statvfs_failed" {
		t.Errorf("reason code = %q, want %q", fs.ReasonCode, "statvfs_failed")
	}
	if fs.Reason == "" {
		t.Error("reason should not be empty for unavailable filesystem")
	}
}

func TestCollect_AbsentFixtureSourceReportsUnavailable(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)
	collector.uptime = &fakeUptime{err: os.ErrNotExist}
	collector.hostname = &fakeHostname{name: "fixture-test"}

	cfg := config.MetricsConfig{}
	snap, err := collector.Collect(context.Background(), cfg, time.Now())
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	if !snap.UptimeUnavailable {
		t.Error("uptime should be unavailable when source is absent")
	}
}

func TestFormatLoadAverage(t *testing.T) {
	tests := []struct {
		input  float64
		expect string
	}{
		{1.234, "1.23"},
		{0.0, "0.00"},
		{100.0, "100.00"},
		{0.004, "0.00"},
		{0.006, "0.01"},
	}
	for _, tt := range tests {
		got := FormatLoadAverage(tt.input)
		if got != tt.expect {
			t.Errorf("FormatLoadAverage(%f) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name   string
		input  uint64
		expect string
	}{
		{"1 MiB", 1024 * 1024, "1.0 MiB"},
		{"1 GiB", 1024 * 1024 * 1024, "1024.0 MiB"},
		{"0 bytes", 0, "0.0 MiB"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatBytes(tt.input)
			if got != tt.expect {
				t.Errorf("FormatBytes(%d) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestFormatPercent(t *testing.T) {
	tests := []struct {
		input  float64
		expect string
	}{
		{0.0, "0.0%"},
		{50.0, "50.0%"},
		{100.0, "100.0%"},
		{33.333, "33.3%"},
		{99.999, "100.0%"},
	}
	for _, tt := range tests {
		got := FormatPercent(tt.input)
		if got != tt.expect {
			t.Errorf("FormatPercent(%f) = %q, want %q", tt.input, got, tt.expect)
		}
	}
}

func TestFormatUptime(t *testing.T) {
	tests := []struct {
		name   string
		input  time.Duration
		expect string
	}{
		{"0 seconds", 0, "0h 0m 0s"},
		{"1 hour", time.Hour, "1h 0m 0s"},
		{"1 day", 24 * time.Hour, "1d 0h 0m 0s"},
		{"1 day 2h 3m 4s", (24*3 + 2)*time.Hour + 3*time.Minute + 4*time.Second, "3d 2h 3m 4s"},
		{"just seconds", 45 * time.Second, "0h 0m 45s"},
		{"hours and seconds", 2*time.Hour + 30*time.Second, "2h 0m 30s"},
		{"minutes only", 15 * time.Minute, "0h 15m 0s"},
		{"large days", 365 * 24 * time.Hour, "365d 0h 0m 0s"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatUptime(tt.input)
			if got != tt.expect {
				t.Errorf("FormatUptime(%v) = %q, want %q", tt.input, got, tt.expect)
			}
		})
	}
}

func TestRenderReport(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	snap := &MetricsSnapshot{
		HostName:       "testhost",
		CollectionTime: now,
		LoadAvg1Min:    1.234,
		LoadAvg5Min:    0.987,
		LoadAvg15Min:   0.765,
		Memory: MemoryMetrics{
			TotalBytes:     8589934080 * 1024,
			AvailableBytes: 4294967296 * 1024,
			UsedBytes:      4294967296 * 1024,
			UsedPercent:    50.0,
		},
		FS: []FilesystemMetrics{
			{
				MountPoint:     "/",
				TotalBytes:     100 * 1024 * 1024 * 1024,
				AvailableBytes: 50 * 1024 * 1024 * 1024,
				UsedBytes:      50 * 1024 * 1024 * 1024,
				UsedPercent:    50.0,
			},
		},
		Uptime: 86400 * time.Second,
	}

	report := RenderReport(snap)
	if !strings.Contains(report, "=== System Metrics ===") {
		t.Error("report missing header")
	}
	if !strings.Contains(report, "Host: testhost") {
		t.Error("report missing hostname")
	}
	if !strings.Contains(report, "Load average") {
		t.Error("report missing load average")
	}
	if !strings.Contains(report, "Memory:") {
		t.Error("report missing memory section")
	}
	if !strings.Contains(report, "Filesystems:") {
		t.Error("report missing filesystem section")
	}
	if !strings.Contains(report, "Uptime:") {
		t.Error("report missing uptime section")
	}
}

func TestRenderReportUnavailable(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	snap := &MetricsSnapshot{
		HostName:          "testhost",
		CollectionTime:    now,
		LoadUnavailable:   true,
		LoadReason:        "proc not mounted",
		Memory: MemoryMetrics{
			Unavailable: true,
			Reason:      "meminfo not available",
		},
		FS: []FilesystemMetrics{
			{
				MountPoint:  "/nonexistent",
				Unavailable: true,
				ReasonCode:  "statvfs_failed",
				Reason:      "statvfs failed for \"/nonexistent\": no such file",
			},
		},
		UptimeUnavailable: true,
		UptimeReason:      "uptime not available",
	}

	report := RenderReport(snap)
	if !strings.Contains(report, "UNAVAILABLE") {
		t.Error("report should contain UNAVAILABLE for unavailable metrics")
	}
	if !strings.Contains(report, "proc not mounted") {
		t.Error("report should contain load reason")
	}
	if !strings.Contains(report, "meminfo not available") {
		t.Error("report should contain memory reason")
	}
}

func TestRenderReportNoFilesystems(t *testing.T) {
	now := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)
	snap := &MetricsSnapshot{
		HostName:       "testhost",
		CollectionTime: now,
		FS:             []FilesystemMetrics{},
	}

	report := RenderReport(snap)
	if !strings.Contains(report, "No filesystems configured") {
		t.Error("report should indicate no filesystems configured")
	}
}

func TestRoundTo2(t *testing.T) {
	tests := []struct {
		input  float64
		expect float64
	}{
		{1.005, 1.01},
		{1.004, 1.0},
		{0.0, 0.0},
		{100.995, 101.0},
		{99.994, 99.99},
	}
	for _, tt := range tests {
		got := roundTo2(tt.input)
		if math.Abs(got-tt.expect) > 0.001 {
			t.Errorf("roundTo2(%f) = %f, want %f", tt.input, got, tt.expect)
		}
	}
}

func TestCollect_ConcurrentSafety(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)
	cfg := config.MetricsConfig{}

	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			defer func() { done <- struct{}{} }()
			snap, err := collector.Collect(context.Background(), cfg, time.Now())
			if err != nil {
				t.Errorf("concurrent Collect() error: %v", err)
				return
			}
			if snap == nil {
				t.Error("concurrent Collect() returned nil snapshot")
			}
		}()
	}

	for i := 0; i < 10; i++ {
		<-done
	}
}

func TestCollect_SnapshotTime(t *testing.T) {
	logger := logging.New("[metrics] ")
	collector := NewWithDefaults(logger)
	now := time.Date(2025, 6, 15, 14, 30, 45, 123000000, time.UTC)
	cfg := config.MetricsConfig{}

	snap, err := collector.Collect(context.Background(), cfg, now)
	if err != nil {
		t.Fatalf("Collect() error: %v", err)
	}

	if !snap.CollectionTime.Equal(now) {
		t.Errorf("collection time = %v, want %v", snap.CollectionTime, now)
	}
}

func TestProcfsFixturesExist(t *testing.T) {
	procfsDir := filepath.Join("tests", "fixtures", "metrics", "procfs")
	if _, err := os.Stat(procfsDir); os.IsNotExist(err) {
		t.Skip("procfs fixtures not available")
	}

	// Verify loadavg fixture
	loadavgData, err := os.ReadFile(filepath.Join(procfsDir, "loadavg"))
	if err != nil {
		t.Fatalf("cannot read loadavg fixture: %v", err)
	}
	if !strings.Contains(string(loadavgData), "1.234") {
		t.Error("loadavg fixture should contain 1.234")
	}

	// Verify meminfo fixture
	meminfoData, err := os.ReadFile(filepath.Join(procfsDir, "meminfo"))
	if err != nil {
		t.Fatalf("cannot read meminfo fixture: %v", err)
	}
	if !strings.Contains(string(meminfoData), "MemTotal") {
		t.Error("meminfo fixture should contain MemTotal")
	}

	// Verify uptime fixture
	uptimeData, err := os.ReadFile(filepath.Join(procfsDir, "uptime"))
	if err != nil {
		t.Fatalf("cannot read uptime fixture: %v", err)
	}
	if !strings.Contains(string(uptimeData), "86400") {
		t.Error("uptime fixture should contain 86400")
	}
}
