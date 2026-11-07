# Architecture

## Overview

vshm is a simple host monitoring agent that runs as a systemd service on Linux amd64 systems. It monitors configured checks, records observations locally in SQLite, and queues events for delivery via Matrix.

## Components

### Core Components

- **CLI** (`internal/cli/`): Command-line interface for validation, one-shot checks, and continuous monitoring
- **Config** (`internal/config/`): TOML configuration loading and validation
- **Model** (`internal/model/`): Shared domain types and interfaces
- **Storage** (`internal/storage/`): SQLite storage with migrations and process locking
- **Logging** (`internal/logging/`): Sanitized logging

### Check Adapters

- **Systemd** (`internal/checks/systemd/`): Monitors systemd unit states
- **HTTP** (`internal/checks/http/`): HTTP endpoint health checks
- **Ping** (`internal/checks/ping/`): Network connectivity checks
- **MySQL** (`internal/checks/mysql/`): Database connectivity checks
- **WebDAV** (`internal/checks/webdav/`): WebDAV availability checks

### Services

- **Scheduler** (`internal/scheduler/`): Monotonic cadence with fixed concurrency
- **Incidents** (`internal/incidents/`): Incident state management
- **Reports** (`internal/reports/`): Daily report generation
- **Outbox** (`internal/outbox/`): Ordered retry and delivery management
- **Matrix Transport** (`internal/matrixtransport/`): Encrypted Matrix event delivery
- **Metrics** (`internal/metrics/`): System metrics collection

## Data Flow

1. **Configuration**: Loaded from `/etc/vshm/vshm.toml` at startup
2. **Checks**: Run according to scheduler cadence
3. **Observations**: Stored in SQLite (`/var/lib/vshm/`)
4. **Incidents**: Created when checks fail persistently
5. **Outbox**: Events queued for Matrix delivery
6. **Matrix**: Encrypted events sent via `matrix-commander-rs`

## Security

- Config: `/etc/vshm/vshm.toml` (root:vshm, 0640)
- Secrets: `/etc/vshm/secrets/*.ini` (root:vshm, 0640)
- State: `/var/lib/vshm` (vshm:vshm, 0700)
- No CAP_NET_RAW for ping checks
- Read-only target access

## Build

```bash
CGO_ENABLED=0 go build -o vshm ./cmd/vshm
```

## Runtime

```bash
systemctl enable --now vshm
```
