package metrics

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeProc implements ProcReader with programmable samples/errors.
type fakeProc struct {
	load      LoadAvgSample
	loadErr   error
	mem       MeminfoSample
	memErr    error
	uptime    time.Duration
	uptimeErr error
}

func (f fakeProc) ReadLoadAvg() (LoadAvgSample, error) { return f.load, f.loadErr }
func (f fakeProc) ReadMeminfo() (MeminfoSample, error) { return f.mem, f.memErr }
func (f fakeProc) ReadUptime() (time.Duration, error)  { return f.uptime, f.uptimeErr }

// fakeStatvfs implements Statvfs with per-path programmable results.
type fakeStatvfs struct {
	samples map[string]StatfsSample
	errs    map[string]error
}

func (f fakeStatvfs) Statvfs(path string) (StatfsSample, error) {
	if err, ok := f.errs[path]; ok {
		return StatfsSample{}, err
	}
	if s, ok := f.samples[path]; ok {
		return s, nil
	}
	return StatfsSample{}, os.ErrNotExist
}

func okProc() fakeProc {
	return fakeProc{
		load:   LoadAvgSample{Load1: 1.234, Load5: 0.987, Load15: 0.765},
		mem:    MeminfoSample{TotalBytes: 16 << 30, AvailableBytes: 4 << 30},
		uptime: 90061 * time.Second,
	}
}

func okStatvfs() fakeStatvfs {
	return fakeStatvfs{samples: map[string]StatfsSample{
		"/":     {TotalBytes: 200 << 30, FreeBytes: 80 << 30, AvailableBytes: 60 << 30},
		"/data": {TotalBytes: 100 << 30, FreeBytes: 50 << 30, AvailableBytes: 40 << 30},
	}}
}

var testNow = time.Date(2026, 11, 5, 9, 0, 0, 0, time.UTC)

func TestCollectSuccess(t *testing.T) {
	c := New(okProc(), okStatvfs())
	snap := c.Collect(context.Background(), nil, testNow) // nil mounts -> default ["/"]

	if !snap.CollectedAt.Equal(testNow) || snap.CollectedAt.Location() != time.UTC {
		t.Errorf("CollectedAt = %v (%s), want %v UTC", snap.CollectedAt, snap.CollectedAt.Location(), testNow)
	}

	if !snap.Load.Available || snap.Load.Load1 != 1.234 || snap.Load.Load5 != 0.987 || snap.Load.Load15 != 0.765 {
		t.Errorf("load = %+v, want available 1.234/0.987/0.765", snap.Load)
	}
	if snap.Load.ReasonCode != ReasonOK {
		t.Errorf("load reason code = %q, want %q", snap.Load.ReasonCode, ReasonOK)
	}

	if !snap.Memory.Available {
		t.Fatal("memory should be available")
	}
	if snap.Memory.TotalBytes != 16<<30 || snap.Memory.AvailableBytes != 4<<30 {
		t.Errorf("memory = %+v", snap.Memory)
	}
	// Exact formula: used = total - available.
	if want := uint64(16<<30) - uint64(4<<30); snap.Memory.UsedBytes != want {
		t.Errorf("mem used = %d, want %d (total - available)", snap.Memory.UsedBytes, want)
	}

	if len(snap.Mounts) != 1 || snap.Mounts[0].Mount != "/" {
		t.Fatalf("mounts = %+v, want default [\"/\"]", snap.Mounts)
	}
	m := snap.Mounts[0]
	if !m.Available || m.TotalBytes != 200<<30 || m.AvailableBytes != 60<<30 {
		t.Errorf("mount = %+v", m)
	}
	// Mount used = total - free (df semantics).
	if want := uint64(200<<30) - uint64(80<<30); m.UsedBytes != want {
		t.Errorf("mount used = %d, want %d (total - free)", m.UsedBytes, want)
	}

	if !snap.Uptime.Available || snap.Uptime.Uptime != 90061*time.Second {
		t.Errorf("uptime = %+v", snap.Uptime)
	}
}

func TestCollectExplicitMountsPreserveOrder(t *testing.T) {
	c := New(okProc(), okStatvfs())
	snap := c.Collect(context.Background(), []string{"/data", "/"}, testNow)
	if len(snap.Mounts) != 2 || snap.Mounts[0].Mount != "/data" || snap.Mounts[1].Mount != "/" {
		t.Fatalf("mounts = %+v, want [/data /] in order", snap.Mounts)
	}
	if snap.Mounts[0].TotalBytes != 100<<30 {
		t.Errorf("mount /data total = %d, want %d", snap.Mounts[0].TotalBytes, 100<<30)
	}
}

func TestCollectUnavailableSources(t *testing.T) {
	proc := fakeProc{
		loadErr:   os.ErrNotExist,
		memErr:    errMissingField{field: "MemAvailable"},
		uptimeErr: errParseFailed{},
	}
	fs := fakeStatvfs{errs: map[string]error{"/": os.ErrNotExist}}
	c := New(proc, fs)
	snap := c.Collect(context.Background(), nil, testNow)

	if snap.Load.Available || snap.Load.ReasonCode != ReasonReadFailed || snap.Load.Reason == "" {
		t.Errorf("load = %+v, want unavailable read_failed with reason", snap.Load)
	}
	if snap.Memory.Available || snap.Memory.ReasonCode != ReasonMissingField {
		t.Errorf("memory = %+v, want unavailable missing_field", snap.Memory)
	}
	if snap.Uptime.Available || snap.Uptime.ReasonCode != ReasonParseFailed {
		t.Errorf("uptime = %+v, want unavailable parse_failed", snap.Uptime)
	}
	if len(snap.Mounts) != 1 || snap.Mounts[0].Available || snap.Mounts[0].ReasonCode != ReasonStatvfsFailed {
		t.Errorf("mounts = %+v, want [/ unavailable statvfs_failed]", snap.Mounts)
	}

	// Unavailable metrics must never surface as zero values in the report.
	out := Render(snap)
	for _, want := range []string{"load=UNAVAILABLE(", "mem=UNAVAILABLE(", "mount /: UNAVAILABLE(", "uptime=UNAVAILABLE("} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered report missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "load1=0.00") || strings.Contains(out, "mem_total=0.0 MiB") || strings.Contains(out, "uptime=0s") {
		t.Errorf("report must not render zero for unavailable metrics:\n%s", out)
	}
}

func TestCollectMountUnavailableDoesNotAffectOthers(t *testing.T) {
	fs := fakeStatvfs{
		samples: map[string]StatfsSample{"/": {TotalBytes: 10 << 30, FreeBytes: 4 << 30, AvailableBytes: 3 << 30}},
		errs:    map[string]error{"/missing": os.ErrNotExist},
	}
	c := New(okProc(), fs)
	snap := c.Collect(context.Background(), []string{"/", "/missing"}, testNow)
	if len(snap.Mounts) != 2 {
		t.Fatalf("mounts = %+v", snap.Mounts)
	}
	if !snap.Mounts[0].Available {
		t.Error("root mount should be available")
	}
	if snap.Mounts[1].Available || snap.Mounts[1].ReasonCode != ReasonStatvfsFailed {
		t.Errorf("missing mount = %+v, want unavailable", snap.Mounts[1])
	}
	out := Render(snap)
	if !strings.Contains(out, "mount /: total=") || !strings.Contains(out, "mount /missing: UNAVAILABLE(") {
		t.Errorf("render should mix available and unavailable mounts:\n%s", out)
	}
}

func TestCollectCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c := New(okProc(), okStatvfs())
	snap := c.Collect(ctx, nil, testNow)
	if snap.Load.Available || snap.Memory.Available || snap.Uptime.Available {
		t.Error("all metrics must be unavailable on cancelled context")
	}
	if snap.Load.ReasonCode != ReasonCancelled || len(snap.Mounts) != 1 || snap.Mounts[0].ReasonCode != ReasonCancelled {
		t.Errorf("reason codes = %+v %+v", snap.Load, snap.Mounts)
	}
}

func TestResolveMounts(t *testing.T) {
	if got := ResolveMounts(nil); len(got) != 1 || got[0] != "/" {
		t.Errorf("ResolveMounts(nil) = %v, want [/]", got)
	}
	if got := ResolveMounts([]string{}); len(got) != 1 || got[0] != "/" {
		t.Errorf("ResolveMounts([]) = %v, want [/]", got)
	}
	in := []string{"/a", "/b"}
	got := ResolveMounts(in)
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("ResolveMounts = %v", got)
	}
	got[0] = "/mutated"
	if in[0] != "/a" {
		t.Error("ResolveMounts must copy, not alias the input")
	}
}

func TestSanitizeReasonBounded(t *testing.T) {
	long := errors.New(strings.Repeat("x", 500) + "\nsecond line")
	r := sanitizeReason(ReasonReadFailed, long)
	if len(r) > maxReasonLen {
		t.Errorf("reason length %d exceeds bound %d", len(r), maxReasonLen)
	}
	if strings.Contains(r, "\n") {
		t.Error("reason must be single-line")
	}
	if !strings.HasPrefix(r, ReasonReadFailed+": ") {
		t.Errorf("reason should start with code, got %q", r)
	}
}

// --- parser tests against fixtures ---

const fixturesRoot = "../../tests/fixtures/metrics"

func TestParseLoadAvgFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixturesRoot, "procfs", "loadavg"))
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	s, err := parseLoadAvg(data)
	if err != nil {
		t.Fatalf("parseLoadAvg: %v", err)
	}
	if s.Load1 != 1.234 || s.Load5 != 0.987 || s.Load15 != 0.765 {
		t.Errorf("loadavg = %+v, want 1.234/0.987/0.765", s)
	}
}

func TestParseMeminfoFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixturesRoot, "procfs", "meminfo"))
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	s, err := parseMeminfo(data)
	if err != nil {
		t.Fatalf("parseMeminfo: %v", err)
	}
	wantTotal := uint64(8589934080) * 1024
	wantAvail := uint64(4294967296) * 1024
	if s.TotalBytes != wantTotal || s.AvailableBytes != wantAvail {
		t.Errorf("meminfo = %+v, want total=%d avail=%d", s, wantTotal, wantAvail)
	}
}

func TestParseUptimeFixture(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(fixturesRoot, "procfs", "uptime"))
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	d, err := parseUptime(data)
	if err != nil {
		t.Fatalf("parseUptime: %v", err)
	}
	if d != 86400*time.Second {
		t.Errorf("uptime = %v, want 86400s", d)
	}
}

func TestParseMalformed(t *testing.T) {
	if _, err := parseLoadAvg([]byte("not numbers at all")); err == nil {
		t.Error("malformed loadavg must fail")
	}
	if _, err := parseLoadAvg([]byte("1.0 2.0")); err == nil {
		t.Error("short loadavg must fail")
	}
	if _, err := parseMeminfo([]byte("MemTotal: abc kB\n")); err == nil {
		t.Error("non-numeric MemTotal must fail")
	}
	if _, err := parseMeminfo([]byte("MemTotal: 100 MB\n")); err == nil {
		t.Error("non-kB unit must fail")
	}
	if _, err := parseUptime([]byte("")); err == nil {
		t.Error("empty uptime must fail")
	}
	if _, err := parseUptime([]byte("-5.0 1.0")); err == nil {
		t.Error("negative uptime must fail")
	}
}

func TestParseMeminfoMissingFields(t *testing.T) {
	// MemTotal present, MemAvailable absent -> missing_field, never guessed zero.
	noAvail, err := os.ReadFile(filepath.Join(fixturesRoot, "procfs-no-memavailable", "meminfo"))
	if err != nil {
		t.Fatalf("fixture missing: %v", err)
	}
	if _, err := parseMeminfo(noAvail); err == nil {
		t.Error("missing MemAvailable must fail")
	} else if reasonCodeOf(err) != ReasonMissingField {
		t.Errorf("reason code = %q, want %q", reasonCodeOf(err), ReasonMissingField)
	}
	// No MemTotal at all.
	if _, err := parseMeminfo([]byte("MemFree: 1 kB\n")); err == nil {
		t.Error("missing MemTotal must fail")
	}
}

func TestProcFSReaderWithFixtures(t *testing.T) {
	p := NewProcReader(filepath.Join(fixturesRoot, "procfs"))
	load, err := p.ReadLoadAvg()
	if err != nil || load.Load1 != 1.234 {
		t.Errorf("ReadLoadAvg = %+v, %v", load, err)
	}
	mem, err := p.ReadMeminfo()
	if err != nil || mem.TotalBytes != uint64(8589934080)*1024 {
		t.Errorf("ReadMeminfo = %+v, %v", mem, err)
	}
	up, err := p.ReadUptime()
	if err != nil || up != 86400*time.Second {
		t.Errorf("ReadUptime = %v, %v", up, err)
	}
}

func TestProcFSReaderMalformedFixtures(t *testing.T) {
	p := NewProcReader(filepath.Join(fixturesRoot, "procfs-malformed"))
	if _, err := p.ReadLoadAvg(); err == nil {
		t.Error("malformed loadavg fixture must error")
	}
	if _, err := p.ReadMeminfo(); err == nil {
		t.Error("malformed meminfo fixture must error")
	}
	if _, err := p.ReadUptime(); err == nil {
		t.Error("malformed uptime fixture must error")
	}
}

func TestCollectAbsentFixtureSourceReportsUnavailable(t *testing.T) {
	p := NewProcReader(filepath.Join(fixturesRoot, "does-not-exist"))
	c := New(p, fakeStatvfs{})
	snap := c.Collect(context.Background(), []string{"/nope"}, testNow)
	if snap.Load.Available || snap.Memory.Available || snap.Uptime.Available {
		t.Error("absent proc source must mark metrics unavailable")
	}
	if len(snap.Mounts) != 1 || snap.Mounts[0].Available {
		t.Error("absent mount must be unavailable")
	}
	out := Render(snap)
	if !strings.Contains(out, "UNAVAILABLE(") {
		t.Errorf("rendered report must contain UNAVAILABLE:\n%s", out)
	}
}

// --- rendering ---

func TestRenderFullSnapshot(t *testing.T) {
	c := New(okProc(), okStatvfs())
	snap := c.Collect(context.Background(), []string{"/", "/data"}, testNow)
	out := Render(snap)

	wants := []string{
		"metrics:",
		"load1=1.23 load5=0.99 load15=0.77",
		"mem_total=16384.0 MiB mem_available=4096.0 MiB mem_used=12288.0 MiB",
		"mount /: total=204800.0 MiB available=61440.0 MiB used=122880.0 MiB",
		"mount /data: total=102400.0 MiB available=40960.0 MiB used=51200.0 MiB",
		"uptime=90061s",
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("render missing %q:\n%s", w, out)
		}
	}
}

func TestRenderOmitsCollectedAt(t *testing.T) {
	local := time.Date(2026, 11, 5, 11, 0, 0, 0, time.FixedZone("UTC+2", 2*3600))
	c := New(okProc(), okStatvfs())
	snap := c.Collect(context.Background(), nil, local)
	// The stored instant stays UTC; the rendered report shows no timestamp at all
	// (the daily report header carries the only date/time).
	if snap.CollectedAt.Location() != time.UTC || !snap.CollectedAt.Equal(local) {
		t.Errorf("CollectedAt = %v, want UTC instant %v", snap.CollectedAt, local.UTC())
	}
	if strings.Contains(Render(snap), "collected_at") {
		t.Errorf("render must not contain a collected_at line:\n%s", Render(snap))
	}
}

func TestFormatHelpers(t *testing.T) {
	if got := FormatLoad(1.234); got != "1.23" {
		t.Errorf("FormatLoad = %q", got)
	}
	if got := FormatMiB(1024 * 1024); got != "1.0 MiB" {
		t.Errorf("FormatMiB = %q", got)
	}
	if got := FormatMiB(0); got != "0.0 MiB" {
		t.Errorf("FormatMiB(0) = %q", got)
	}
	if got := FormatUptime(90061 * time.Second); got != "90061s" {
		t.Errorf("FormatUptime = %q", got)
	}
}

// --- runtime validation against the real host /proc and root mount ---

func TestCollectRealSystem(t *testing.T) {
	if _, err := os.Stat("/proc/loadavg"); err != nil {
		t.Skip("no /proc on this host")
	}
	c := NewWithDefaults()
	snap := c.Collect(context.Background(), nil, time.Now())
	if !snap.Load.Available {
		t.Errorf("real load should be available, got %+v", snap.Load)
	}
	if !snap.Memory.Available || snap.Memory.TotalBytes == 0 || snap.Memory.UsedBytes > snap.Memory.TotalBytes {
		t.Errorf("real memory invalid: %+v", snap.Memory)
	}
	if !snap.Uptime.Available || snap.Uptime.Uptime <= 0 {
		t.Errorf("real uptime invalid: %+v", snap.Uptime)
	}
	if len(snap.Mounts) != 1 || !snap.Mounts[0].Available || snap.Mounts[0].Mount != "/" {
		t.Errorf("real root mount = %+v", snap.Mounts)
	}
	if snap.Mounts[0].TotalBytes == 0 || snap.Mounts[0].UsedBytes > snap.Mounts[0].TotalBytes {
		t.Errorf("real mount usage invalid: %+v", snap.Mounts[0])
	}
	out := Render(snap)
	if !strings.Contains(out, "load1=") || !strings.Contains(out, "mem_total=") || !strings.Contains(out, "mount /:") || !strings.Contains(out, "uptime=") {
		t.Errorf("real render incomplete:\n%s", out)
	}
	t.Logf("real host metrics render:\n%s", out)
}

func TestRealStatvfsRoot(t *testing.T) {
	s, err := RealStatvfs{}.Statvfs("/")
	if err != nil {
		t.Fatalf("statvfs /: %v", err)
	}
	if s.TotalBytes == 0 || s.TotalBytes < s.FreeBytes || s.FreeBytes < s.AvailableBytes {
		t.Errorf("inconsistent statfs sample: %+v", s)
	}
	if _, err := (RealStatvfs{}).Statvfs("/definitely/not/a/mount-vshm"); err == nil {
		t.Error("statvfs on missing path must fail")
	}
}
