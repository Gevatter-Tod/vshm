# WP-07 Completion — read-only MySQL check adapter

**Status**: ✅ Completed

## What was built

- `internal/checks/mysql/mysql.go` — the adapter implementing the WP-00 `model.Check`
  contract (`Type() == "mysql"`, `Run(ctx, cfg) model.CheckResult`). Connects with the
  pinned `github.com/go-sql-driver/mysql v1.10.1` via `mysql.NewConnector` on a
  `mysql.Config` struct (the password is never formatted into a DSN string), executes
  the validated single `SELECT` with `db.QueryContext` under the one absolute deadline
  from `ctx` (DNS/connect/query/row-retrieval all observe it), and requires at least
  one fetched row for success. Row values are never scanned and are discarded
  immediately. Stable sanitized reason codes: `success`, `invalid_config`,
  `invalid_query`, `credential_error`, `tls_ca_error`, `tls_verify_failed`,
  `connect_failed`, `timeout`, `query_failed`, `no_rows`. MySQL error messages (which
  can quote SQL text) are reduced to the numeric error code; network error text is
  never surfaced.
- `internal/checks/mysql/config.go` — typed immutable `Config` (implements
  `model.CheckConfig`); the scheduler path (`*scheduler.Spec` with `MySQL` fields) and
  direct construction are both accepted, mirroring the systemd adapter.
- `internal/checks/mysql/secrets.go` — INI `[client]` credential loader
  (`user`/`password`/optional `database`, comments, quoted values). Fails closed on
  world-readable or group/other-writable files; errors never contain the path or file
  contents.
- `internal/checks/mysql/tls.go` — verified TLS for TCP: CA file must parse to ≥1
  certificate, `RootCAs` + `ServerName` = configured host (hostname verification),
  `MinVersion` TLS 1.2, `InsecureSkipVerify` false. Unix socket (`socket` key) uses
  `Net: "unix"` with no network TLS.
- `internal/sqlsafe/` — conservative single-SELECT validator shared by config
  validation and the adapter (no import cycles): exactly one statement starting with
  `SELECT`, at most one optional trailing semicolon, no other semicolons (including
  quoted ones), no comments (including MySQL `/*! */` executable comments), balanced
  parentheses, forbidden write/lock keywords (`INTO`, `FOR`, `LOCK`, DML/DDL list)
  rejected outside string literals, max 4096 bytes. Errors never echo SQL text.
- Wiring: `internal/config/config.go` validates the MySQL TOML keys
  (`host`/`port` default 3306/`socket`/`database`/`credential_file`/`tls_ca`/`query`),
  requiring `tls_ca` for TCP and making `socket` mutually exclusive with
  `host`/`port`/`tls_ca`; `internal/scheduler/entries.go` carries them in
  `Spec.MySQL`; the adapter was already registered in `internal/cli/cli.go`.
- Fixtures: `tests/fixtures/mysql/client.ini` (placeholder `[client]`), `ca.pem`
  (test CA). `config/example.toml` documents the `socket` key.
- Tests: `internal/sqlsafe/sqlsafe_test.go` (grammar table),
  `internal/checks/mysql/mysql_test.go` (fake `driver.Connector`: success TCP+TLS,
  socket, database precedence, zero-rows, MySQL error redaction, connect error,
  deadline timeout, invalid query rejected before connect, missing/bad CA, credential
  file parsing + permission failures, no secrets/SQL/paths in logs or results, scheduler
  `Spec` path), `internal/config/config_test.go` (schema validation), scheduler wiring
  test.

## Answers chosen for the WP-07 open questions (user said "implement now")

- **SELECT grammar**: conservative reject-ambiguous grammar as documented above;
  CTEs (`WITH`), comments and quoted semicolons are rejected. Parser is an aid only;
  the mandatory read-only DB account remains the security boundary.
- **TLS CA path constraints**: any readable path whose content parses as PEM with at
  least one certificate; hostname verification pinned to the configured `host`.
- **Unix socket representation**: new TOML key `socket` (absolute path), mutually
  exclusive with `host`/`port`/`tls_ca`; no network TLS on sockets.

## Definition of Done evidence

1. Files exist in the required tree (`internal/checks/mysql/`, `internal/sqlsafe/`,
   `tests/fixtures/mysql/`); no WP-00 public interface changed (`model.*` untouched;
   only additive fields on `scheduler.Spec`).
2. `go build ./...` and `go vet ./...` pass; `gofmt` clean on touched files.
3. `go test -race ./...` passes for the whole repo (mysql, sqlsafe, config, scheduler
   packages included).
4. Runtime validation (recorded 2026-11-19): `CGO_ENABLED=0 go build -o vshm ./cmd/vshm`;
   `./vshm validate-config config/example.toml` → OK (5 checks);
   `./vshm validate-config` on a config with a valid TCP and a socket mysql check → OK;
   negative fixture (`query = "SELECT 1; DROP TABLE t"`, no `tls_ca`) → exit 1 with
   `invalid query: multiple statements ... ; tls_ca is required for TCP mysql connections`;
   `./vshm once good.toml state/` ran both mysql checks against unreachable local
   targets → observations persisted as `success=false reason_code=connect_failed
   reason="connect failed"`; output and DB rows contain no password, user, SQL text or
   secret-bearing path (grep-verified).
5. Acceptance criteria (spec §8 + wp07 §8): single-SELECT validation, optional trailing
   semicolon, DML/DDL/multi-statement/malformed rejection, zero-rows failure, row values
   discarded, TLS CA/hostname config construction, socket mode, deadline timeout, and
   fake-driver success/failure paths are all covered by unit tests; no live DB used.
6. Open questions answered as documented above.
7. WP-00 interfaces unchanged.
8. This report.

## Known limitations

- TLS hostname/certificate verification is unit-tested at the `tls.Config` construction
  level and via driver-config assertions; an end-to-end TLS handshake test would need a
  MySQL-protocol-speaking test server, which the brief explicitly excludes (CI uses a
  fake database, no live dependency).
- The conservative grammar rejects some theoretically safe statements (CTEs, comments,
  quoted semicolons); this is deliberate fail-closed behavior.
- Credential files must exist with safe modes at runtime; `validate-config` tolerates
  not-yet-created files (consistent with existing `secret_file` behavior) and the
  adapter fails closed if the file is missing or too open at run time.
