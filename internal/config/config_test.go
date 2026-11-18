package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testConfigTOML = `
[agent]
host_name = "test-host"
interval = "1m"
timeout = "10s"
report_time = "09:00"
room_id = "!room:example.org"

[[checks]]
check_id = "systemd-check"
type = "systemd"
target = "caddy.service"

[[checks]]
check_id = "http-check"
type = "http"
url = "https://example.org/health"
expected_status = 200
contains = "ok"

[[checks]]
check_id = "ping-check"
type = "ping"
host = "192.0.2.1"
`

func TestLoadParsesCheckKeys(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, testConfigTOML))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Checks) != 3 {
		t.Fatalf("expected 3 checks, got %d", len(cfg.Checks))
	}
	sd := cfg.Checks[0]
	if sd.Target != "caddy.service" {
		t.Errorf("systemd target not parsed: %q", sd.Target)
	}
	hc := cfg.Checks[1]
	if hc.BodyContains != "ok" {
		t.Errorf("http contains not parsed into BodyContains: %q", hc.BodyContains)
	}
	if hc.ExpectedStatus != 200 || hc.URL != "https://example.org/health" {
		t.Errorf("http fields wrong: %+v", hc)
	}
	if cfg.Checks[2].Host != "192.0.2.1" {
		t.Errorf("ping host not parsed: %q", cfg.Checks[2].Host)
	}
	if err := Validate(cfg); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}
}

func TestValidateRequiresSystemdTarget(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
[agent]
host_name = "test-host"

[[checks]]
check_id = "systemd-check"
type = "systemd"
`))
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(cfg)
	if err == nil {
		t.Fatal("expected validation error for missing systemd target")
	}
	if !strings.Contains(err.Error(), "target is required for systemd checks") {
		t.Errorf("unexpected error: %v", err)
	}
}

func writeTempConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "vshm.toml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
