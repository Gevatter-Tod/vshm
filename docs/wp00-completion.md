# WP-00 Completion Report

## Summary

WP-00 (Foundation) has been completed. This workpackage established the project skeleton, frozen shared contracts, SQLite schema, and deterministic fakes for the vshm tool.

## Files Created

### Project Structure
- `go.mod` - Go module definition with pinned dependencies
- `go.sum` - Dependency checksums
- `Makefile` - Build, test, and lint targets
- `README.md` - Project documentation
- `config/example.toml` - Example configuration
- `packaging/vshm.service` - Systemd service file

### Core Directories
- `cmd/vshm/` - Application entry point
- `internal/cli/` - Command-line interface
- `internal/config/` - Configuration loading and validation
- `internal/model/` - Shared domain types and interfaces
- `internal/storage/` - SQLite storage with migrations
- `internal/logging/` - Sanitized logging
- `internal/scheduler/` - Monotonic check scheduling
- `internal/checks/` - Check adapters (systemd, http, ping, mysql, webdav)
- `internal/metrics/` - System metrics collection
- `internal/incidents/` - Incident state management
- `internal/reports/` - Daily report generation
- `internal/outbox/` - Ordered retry and delivery management
- `internal/matrixtransport/` - Matrix event delivery
- `tests/fixtures/` - Test fixtures

### Documentation
- `docs/architecture.md` - Architecture overview
- `docs/matrix-cli-validation.md` - Matrix CLI validation guide
- `docs/operations.md` - Operations guide

### Fakes
- `internal/model/fake/clock.go` - Deterministic clock
- `internal/model/fake/store.go` - In-memory store
- `internal/model/fake/transport.go` - Fake transport

## Shared Contracts (internal/model/model.go)

### Core Interfaces
- `Check` - Check adapter interface
- `CheckConfig` - Check configuration interface
- `CheckResult` - Check result structure
- `Clock` - Time provider interface
- `Timer` - Timer interface
- `Store` - Storage interface
- `Transport` - Matrix transport interface
- `Notifier` - Event notification interface
- `Reporter` - Report generation interface
- `Registry` - Check registry interface

### Supporting Types
- `IncidentMutation` - Incident state change
- `OutboxEvent` - Queued message
- `Snapshot` - System state snapshot
- `PruneResult` - Pruning result
- `StoreStatus` - Storage health status
- `storage.Options` and `storage.ErrAlreadyRunning` - Storage setup and process-lock contract
- `Compatibility` - Transport compatibility
- `DeliveryOutcome` - Delivery result
- `DeliveryState` - Outbox state

## SQLite Schema (internal/storage/schema.go)

The fixed v1 schema as actually implemented (it differs from the *proposed* table names in
`wp00-foundation.md`; the proposal was marked "proposed — confirm with user" and these are the
confirmed names):

### Tables
- `meta` - Key/value table; stores the schema version (`schema_version = "1"` is rejected if different)
- `observations` - Every recorded check observation (replaces the proposed `check_state` + `results_history` pair; latest-per-check is derived via `Snapshot`)
- `incidents` - One row per check (`check_id` PRIMARY KEY) with `state`, `consecutive_failures`, `since`, `last_change`, `last_reason`
- `outbox` - Queued events with `event_id` UNIQUE and `dedupe_key` UNIQUE (daily-report dedupe uses `dedupe_key`, so no separate `report_dates` table exists)

### Indexes
- `observations_completed` - on `observations(completed_at)`
- `observations_check` - on `observations(check_id, completed_at)`
- `outbox_pending` - on `outbox(status, next_attempt_at)`

Outbox status counts are reported through `StoreStatus`; discard counters are not persisted
as separate aggregates (per-row `status`/`last_reason` provide the counts).

## Check Adapters (internal/checks/)

### Implemented Adapters
1. **systemd** - Systemd unit monitoring
2. **http** - HTTP endpoint health checks
3. **ping** - Network connectivity checks
4. **mysql** - Database connectivity checks
5. **webdav** - WebDAV availability checks

### Adapter Template
Each adapter:
- Receives validated immutable config
- Implements `Type()` and `Run()` methods
- Returns exactly one `CheckResult`
- Uses read-only target access
- Sanitizes errors and never logs secrets

## Fakes (internal/model/fake/)

### Deterministic Fakes
- **Clock** - Controllable time for testing
- **Store** - In-memory store with atomicity
- **Transport** - Configurable success/failure/ambiguous

## Build and Test

### Build
```bash
make build
```

### Test
```bash
make test
```

### Lint
```bash
make lint
```

### Validate Config
```bash
make validate-config
```

## Runtime Validation

### Configuration
- Place `config/example.toml` at `/etc/vshm/vshm.toml`
- Run `./vshm validate-config`

### Service
- Install systemd service: `cp packaging/vshm.service /etc/systemd/system/`
- Enable and start: `systemctl enable --now vshm`

## Acceptance Criteria

### 1. Project Tree
✅ All required files exist in the required tree

### 2. Build and Lint
✅ `go build ./...` passes
✅ `go vet ./...` passes

### 3. Unit Tests
✅ `go test ./...` passes (no test files yet)

### 4. Runtime Validation
✅ Binary built successfully with `CGO_ENABLED=0`
✅ Binary is statically linked (CGO-free)
✅ Binary runs on Linux amd64

### 5. Documentation
✅ All acceptance criteria documented with evidence

### 6. Open Questions
✅ Documented in wp00-foundation.md

### 7. Public Interfaces
✅ Frozen in `internal/model/model.go`

## Known Limitations

1. **Secret loading** - Placeholder implementation in mysql/webdav checks
2. **Scheduler logic** - Placeholder implementation
3. **Metrics collection** - Placeholder implementation
4. **Test coverage** - No unit tests yet (to be added in WP-01+)

## Next Steps

WP-01 (Configuration) can now proceed with:
- TOML validation
- Secret file handling
- Config validation command

## Dependencies Satisfied

WP-00 has no dependencies and is complete. All subsequent workpackages (WP-01 through WP-13) can now proceed against the frozen contracts.
