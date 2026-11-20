package config

import (
	"fmt"
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

func TestValidateMySQLCheckValid(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
[agent]
host_name = "test-host"

[[checks]]
check_id = "db-readiness"
type = "mysql"
host = "db.example.org"
port = 3306
database = "app"
credential_file = "/etc/vshm/secrets/db.ini"
tls_ca = "/etc/ssl/certs/ca-certificates.crt"
query = "SELECT 1;"

[[checks]]
check_id = "db-local"
type = "mysql"
socket = "/run/mysqld/mysqld.sock"
credential_file = "/etc/vshm/secrets/db-local.ini"
query = "SELECT 1"
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid mysql config rejected: %v", err)
	}
	mc := cfg.Checks[0]
	if mc.Port != 3306 || mc.Database != "app" || mc.TLSCA == "" || mc.Query != "SELECT 1;" {
		t.Errorf("mysql fields not parsed: %+v", mc)
	}
	if cfg.Checks[1].Socket != "/run/mysqld/mysqld.sock" {
		t.Errorf("socket not parsed: %+v", cfg.Checks[1])
	}
}

func TestValidateMySQLCheckErrors(t *testing.T) {
	base := `
[agent]
host_name = "test-host"

[[checks]]
check_id = "db"
type = "mysql"
%s
`
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing query", `host = "h"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"`, "query is required"},
		{"bad query DML", `host = "h"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `query = "DELETE FROM t"`, "invalid query"},
		{"bad query multi", `host = "h"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `query = "SELECT 1; SELECT 2"`, "invalid query"},
		{"missing credential_file", `host = "h"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `query = "SELECT 1"`, "credential_file is required"},
		{"missing host and socket", `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `query = "SELECT 1"`, "host is required"},
		{"TCP without tls_ca", `host = "h"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `query = "SELECT 1"`, "tls_ca is required"},
		{"port out of range", `host = "h"` + "\n" + `port = 70000` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `query = "SELECT 1"`, "port must be between"},
		{"socket not absolute", `socket = "run/mysqld.sock"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `query = "SELECT 1"`, "socket must be an absolute path"},
		{"socket plus tls_ca", `socket = "/run/mysqld/mysqld.sock"` + "\n" + `tls_ca = "/etc/ssl/certs/ca.crt"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `query = "SELECT 1"`, "tls_ca must not be set when socket is used"},
		{"socket plus host", `socket = "/run/mysqld/mysqld.sock"` + "\n" + `host = "h"` + "\n" + `credential_file = "/etc/vshm/secrets/x.ini"` + "\n" + `query = "SELECT 1"`, "host must not be set when socket is used"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTempConfig(t, fmt.Sprintf(base, tc.body)))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateWebDAVCheckValid(t *testing.T) {
	cfg, err := Load(writeTempConfig(t, `
[agent]
host_name = "test-host"

[[checks]]
check_id = "dav-https"
type = "webdav"
url = "https://dav.example.org/dav"
depth = 1
credential_file = "/etc/vshm/secrets/webdav.ini"

[[checks]]
check_id = "dav-http-plain"
type = "webdav"
url = "http://127.0.0.1:8080/dav"
depth = 0
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid webdav config rejected: %v", err)
	}
	if cfg.Checks[0].Depth != 1 || cfg.Checks[1].Depth != 0 {
		t.Errorf("depth not parsed: %+v / %+v", cfg.Checks[0], cfg.Checks[1])
	}
}

func TestValidateWebDAVCheckErrors(t *testing.T) {
	base := `
[agent]
host_name = "test-host"

[[checks]]
check_id = "dav"
type = "webdav"
%s
`
	cases := []struct {
		name    string
		body    string
		wantErr string
	}{
		{"missing url", `depth = 1`, "url is required for webdav checks"},
		{"bad scheme", `url = "ftp://example.com/dav"` + "\n" + `depth = 1`, "scheme must be http or https"},
		{"relative url", `url = "/dav"` + "\n" + `depth = 1`, "valid absolute"},
		{"depth 2", `url = "https://example.com/dav"` + "\n" + `depth = 2`, "depth must be 0 or 1"},
		{"depth -1", `url = "https://example.com/dav"` + "\n" + `depth = -1`, "depth must be 0 or 1"},
		{"credentials over http", `url = "http://example.com/dav"` + "\n" + `depth = 1` + "\n" + `credential_file = "/etc/vshm/secrets/webdav.ini"`, "only permitted with HTTPS"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := Load(writeTempConfig(t, fmt.Sprintf(base, tc.body)))
			if err != nil {
				t.Fatal(err)
			}
			err = Validate(cfg)
			if err == nil {
				t.Fatalf("expected validation error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}
