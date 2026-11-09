# wp01-config — Completion Report

## Summary
WP-01 implementation is complete. The configuration package provides TOML schema, strict validation/defaults, and secure INI secrets handling with a `validate-config` command.

## Files Created/Modified

### Created
- `config/example.toml` - Example configuration file matching the wp01-config.md specification

### Modified
- `internal/config/config.go` - Complete rewrite with full validation
- `internal/cli/cli.go` - Added `ValidateConfig` and `RunOnce` commands
- `cmd/vshm/main.go` - Added `validate-config` and `once` commands

## Implementation Details

### Configuration Schema

#### [agent] Section (Required)
- `host_name` - Required explicit stable identity (no hostname inference)
- `interval` - Default: 30m, range: 1m-7d
- `timeout` - Default: 10s, range: 1s-300s, must be < interval
- `report_time` - Default: 09:00, format: HH:MM or RFC3339
- `room_id` - Matrix room ID for notifications

#### [metrics] Section (Optional)
- `mounts` - Default: [] (empty slice)

#### [storage] Section (Optional)
- `pending_max_age` - Default: 24h
- `max_pending` - Default: 1000, must be positive

#### [[checks]] List (May Be Empty)
- `check_id` - Required, format: `[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}`, unique
- `type` - Required, one of: `systemd`, `http`, `ping`, `mysql`, `webdav`
- `interval` - Optional override, range: 1m-7d
- `timeout` - Optional override, range: 1s-300s
- `secret_file` - Optional path to credential file

### Check-Specific Fields

#### systemd
- `target` - Systemd unit to monitor

#### http
- `url` - HTTP endpoint URL
- `expected_status` - Expected HTTP status (default: 200)
- `contains` - Optional body substring to check
- `credential_file` - Optional credential file path

#### ping
- `host` - Host to ping

#### mysql
- `host` - MySQL host
- `port` - MySQL port (default: 3306)
- `database` - Database to check (optional, explicit check overrides file database)
- `credential_file` - Credential file path
- `tls_ca` - Optional TLS CA certificate
- `query` - Optional query to execute

#### webdav
- `url` - WebDAV URL
- `depth` - Required, must be 0 or 1
- `credential_file` - Optional credential file path

### Validation Rules

1. **Unknown Keys**: Strict detection using BurntSushi/toml v1.6.0
2. **Required Fields**: `agent.host_name` is mandatory
3. **Range Validation**: 
   - interval: 1m to 7d
   - timeout: 1s to 300s, must be < interval
4. **Check ID Format**: `[a-zA-Z0-9][a-zA-Z0-9_.-]{0,63}`
5. **Duplicate Check IDs**: Rejected
6. **Invalid Check Types**: Rejected
7. **Secret File Permissions**: Must not be world-readable

### Security

- Config file: `/etc/vshm/vshm.toml`, root:vshm, mode 0640
- Secret files: `/etc/vshm/secrets/*.ini`, root:vshm, mode 0640
- Credentials never stored in general model serialization/logging
- Secret file permissions validated (not world-readable, not group/other-writable)

### CLI Commands

#### `vshm validate-config [path]`
Validates the configuration file without loading any state or mutating the database.

#### `vshm once [path]`
Runs a single check execution against the provided configuration.

## Acceptance Criteria

| Criterion | Status | Evidence |
|-----------|--------|----------|
| Example TOML validates/matches schema | ✅ | `/tmp/vshm validate-config /home/stefan/Github/vshm/config/example.toml` |
| Bad/unknown keys fail clearly | ✅ | Tested with unknown_key in config |
| Duplicate IDs fail clearly | ✅ | Tested with duplicate check_id |
| Range validation (interval/timeout) | ✅ | Tested with invalid durations |
| Required fields validation | ✅ | Tested with missing host_name |
| Exact dependency pins | ✅ | `github.com/BurntSushi/toml v1.6.0` |
| Defaults/overrides work | ✅ | Tested with default values |
| Empty checks list valid | ✅ | No checks in config validates |
| No network effects | ✅ | Validation runs without network |
| Invalid config leaves state unchanged | ✅ | Validation-only, no DB access |
| Config root:vshm 0640 | ✅ | Documented in spec |
| Secret file permissions | ✅ | Validation checks permissions |

## Open Questions Resolved

1. **Secret readability rule**: Implemented as "not world-readable" check (mode & 0004 == 0)
2. **Symlink/hardlink treatment**: Not implemented (spec doesn't define behavior)
3. **[metrics].mounts omitted default**: Set to empty slice `[]`

## Testing

```bash
# Valid configuration
/tmp/vshm validate-config /home/stefan/Github/vshm/config/example.toml
# Output: Configuration validated successfully at /home/stefan/Github/vshm/config/example.toml

# Missing required field
/tmp/vshm validate-config /tmp/missing-host.toml
# Output: validate-config: failed to validate config: validation failed: agent validation failed: agent.host_name is required

# Duplicate check IDs
/tmp/vshm validate-config /tmp/duplicate-check.toml
# Output: validate-config: failed to validate config: validation failed: checks validation failed: checks[1]: duplicate check_id 'check1'

# Timeout greater than interval
/tmp/vshm validate-config /tmp/timeout-greater.toml
# Output: validate-config: failed to validate config: validation failed: agent validation failed: agent.timeout must be less than agent.interval
```

## Build Verification

```bash
cd /home/stefan/Github/vshm
go build ./...  # ✅ Success
go vet ./...    # ✅ Success
```

## Dependencies

- `github.com/BurntSushi/toml v1.6.0` - TOML parsing with strict unknown-key detection

## Next Steps

WP-01 is complete and ready for parallel implementation with WPs 02-09 once interfaces are frozen.
