# Operations Guide

v1 scope: Linux amd64 with systemd only. The agent is a single C-free binary at
`/usr/bin/vshm`, supervised by the packaged unit
`/usr/lib/systemd/system/vshm.service`, running as the dedicated non-root
`vshm:vshm` account (no capabilities; ping works unprivileged — no `CAP_NET_RAW`).

## Commands

| Command | Effect | Exit |
|---|---|---|
| `vshm validate-config [config]` | schema/values/duplicate-ID/range/path/secret-permission validation; no network, no send | 0 ok / 1 invalid |
| `vshm validate-matrix [cli] [config] [room]` | non-sending Matrix compatibility check | 0 compatible / 1 blocked |
| `vshm check [ID] [config]` | run the named check, or all checks when ID is omitted, once with the configured timeout; **no incident state change, no send**, nothing persisted | 0 all passed / 1 any failed or unknown ID |
| `vshm once [config] [stateDir]` | single full monitoring pass: every check exactly once on the worker pool, observations recorded through the incident/outbox pipeline (persisted), one sanitized line per check plus a summary; when the pass is done, pending events are delivered immediately (fail-closed if Matrix not validated) | 0 run / 1 infrastructure error |
| `vshm status [stateDir]` | last observations/ages, incidents, pending/delivery/discard counts and reasons, storage fault, DB health | 0 / 1 |
| `vshm test-matrix [cli] [room] [message]` | the **only** command that sends an explicit test message | 0 sent / 1 failed |
| `vshm run [config] [stateDir]` | the agent in foreground (what systemd runs): all checks run promptly at startup; failure/recovery messages are sent as soon as they are recorded; the current-date report is sent via Matrix right after the startup pass (date key prevents duplicates); afterwards checks/reports follow the configured schedule | 0 on clean shutdown / nonzero on error or cleanup failure |

Errors are nonzero and sanitized (no secrets, SQL text, or row values in output).
Default paths: config `/etc/vshm/vshm.toml`, state `/var/lib/vshm`.

## Installation

```bash
# 1. Service account and protected paths
sudo useradd -r -s /usr/sbin/nologin vshm
sudo install -d -o root -g vshm -m 0750 /etc/vshm/secrets
sudo install -o root -g vshm -m 0640 /etc/vshm/vshm.toml.example /etc/vshm/vshm.toml
# secret files: /etc/vshm/secrets/*.ini, root:vshm 0640, never group/other-writable

# 2. Release binary + unit (or: make install DESTDIR=/)
sudo install -m 0755 dist/vshm-linux-amd64 /usr/bin/vshm
sudo install -m 0644 packaging/vshm.service /usr/lib/systemd/system/vshm.service

# 3. Validate before starting; then enable/start
sudo /usr/bin/vshm validate-config
sudo /usr/bin/vshm validate-matrix     # failure blocks SENDING only, not monitoring
sudo systemctl daemon-reload
sudo systemctl enable --now vshm
sudo systemctl status vshm
sudo /usr/bin/vshm status
```

No language runtime or package installation is needed on the target. The unit
uses `User=vshm`, `Group=vshm`, `ExecStart=/usr/bin/vshm run`,
`Restart=on-failure`, `RestartSec=5s`, `UMask=0077`, `StateDirectory=vshm`
(0700), `ReadWritePaths=/var/lib/vshm` plus the full sandbox set
(`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`,
kernel/CG protections, `LockPersonality`, `RestrictSUIDSGID`,
`RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` — AF_UNIX keeps the
read-only system bus reachable, AF_INET/AF_INET6 permit Matrix delivery).
Journal is the sole logging destination. Only systemd supervision restarts
vshm; it never remediates monitored targets. Verify a packaged unit with
`systemd-analyze verify` (all directives require systemd ≥ 247; on older
systemd the unit fails to load rather than silently dropping protections).

## Configuration

- **Path**: `/etc/vshm/vshm.toml`, `root:vshm` `0640` (see `config/example.toml`).
- Unknown keys, duplicate check IDs, malformed values, invalid ranges, missing
  files and insecure secret permissions are hard errors (`vshm validate-config`).
- **Config changes require a restart** (`sudo systemctl restart vshm`);
  a restart preserves incident state, report date keys and the outbox —
  nothing is reset or caught up.
- Invalid config makes the new process fail clearly without altering persisted
  monitoring state.
- `[metrics] mounts` omitted/empty means `["/"]` (root mount) at collection time.
- `[storage]` keys (`pending_max_age`, `min_available_bytes`) are validated, but
  the running agent currently opens storage with the package defaults
  (`pending_max_age` 24h, `max_pending` 1000) — configured values are not yet
  applied at runtime, and `min_available_bytes` is not enforced (the store never
  probes free disk space). See `docs/wp02-completion.md` known limitations.

### Example

```toml
[agent]
host_name = "my-server"
interval = "30m"      # default 30m, range 1m-7d
timeout = "10s"       # default 10s, range 1-300s, must be < interval
report_time = "09:00" # daily report, server-local HH:MM
room_id = "!roomid:matrix.org"

[[checks]]
check_id = "http-check"
type = "http"
url = "https://example.com/health"
expected_status = 200
interval = "1m"
timeout = "10s"
```

Check types: `systemd` (`target`), `http` (`url`, `expected_status`, `contains`,
`credential_file`), `ping` (`host`), `mysql` (`host`/`port`/`tls_ca` or
`socket`, `database`, `credential_file`, single read-only `query`),
`webdav` (`url` https, `depth` 0|1, `credential_file`).

## Secrets

```bash
sudo install -d -o root -g vshm -m 0750 /etc/vshm/secrets
# e.g. /etc/vshm/secrets/mysql.ini — root:vshm 0640
```

```ini
[mysql]
user=monitor
password=secret
host=localhost
port=3306
database=monitoring
```

Secrets never appear in command arguments, logs, reports, source control or
the sample config.

## Day-to-day

```bash
systemctl status vshm                 # service state
journalctl -u vshm -f                 # logs (sole destination, bounded/sanitized)
/usr/bin/vshm status                  # observations, incidents, outbox counts, storage fault
/usr/bin/vshm check http-check        # ad-hoc single check, no side effects
/usr/bin/vshm check                   # all checks, no side effects
/usr/bin/vshm once                    # one full monitoring pass (records state)
/usr/bin/vshm validate-config
/usr/bin/vshm validate-matrix         # non-sending compatibility check
/usr/bin/vshm test-matrix             # explicit encrypted test send — only after approval
```

### Manual run as a non-service user (non-root)

The deployed agent runs as `vshm:vshm` and uses `/var/lib/vshm` (owned
`vshm:vshm` 0700). Any other non-root user cannot write there; in that case
`vshm` falls back to a per-user state dir (`$XDG_STATE_HOME/vshm`, default
`~/.local/state/vshm`, mode 0700) and prints a notice. You can also choose
explicitly:

```bash
./vshm run /etc/vshm/vshm.toml /tmp/vshm-state   # foreground diagnosis
./vshm once /etc/vshm/vshm.toml /tmp/vshm-state
./vshm status /tmp/vshm-state
```

`/etc/vshm/vshm.toml` is `root:vshm` 0640, so a non-root user must be in the
`vshm` group (or pass a readable copy as the config argument). Each state dir
has its own SQLite database — don't mix them if you need continuous incident
history. A second instance on the same state dir is refused (lock file).

## Upgrade

```bash
# /var/lib/vshm is sensitive host-specific data (incl. Matrix crypto state):
# back it up on the same host only — NEVER copy crypto identity to another host.
sudo systemctl stop vshm
sudo cp -a /var/lib/vshm /var/backups/vshm-$(date +%F)/
sudo install -m 0755 dist/vshm-linux-amd64 /usr/bin/vshm
sudo install -m 0644 packaging/vshm.service /usr/lib/systemd/system/vshm.service
sudo systemctl daemon-reload
sudo /usr/bin/vshm validate-config      # explicit migrations/validation
sudo /usr/bin/vshm validate-matrix
sudo systemctl start vshm
sudo /usr/bin/vshm status               # verify: incidents/outbox preserved
```

## Troubleshooting

```bash
/usr/bin/vshm validate-config                    # configuration errors
/usr/bin/vshm check <id>                         # single check, no side effects
/usr/bin/vshm status                             # storage fault, pending/delivery/discard counts+reasons
journalctl -u vshm -n 200                        # sanitized, bounded logs
sqlite3 /var/lib/vshm/vshm.db "SELECT ..."       # read-only inspection (stop vshm first)
```

Pruning (90-day observations, 30-day finished outbox events, 24h pending expiry,
oldest-first drop of pending rows beyond the 1000-row bound with reason
`queue_full`) is automatic; see `docs/architecture.md`. There is **no** low-space
eviction in v1: the store never probes free disk space, and
`storage.min_available_bytes` is accepted/validated in the config but not
enforced. Disk-full/`ENOSPC` write errors surface as a storage fault in
`vshm status` and the journal; the agent logs and continues without pruning or
retrying the failed write.
