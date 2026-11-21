# WP-13 CLI & Deployment — Completion Report

## Overview

WP-13 wires the CLI/application, hardens the systemd packaging, provides the
operations/install/upgrade runbook, and adds the end-to-end acceptance suite, as
specified in `docs/wp13-cli-deployment.md` and `docs/vshm-spec.md` §6–§8.

## What was built

| File | Status | Description |
|------|--------|-------------|
| `cmd/vshm/main.go` | Modified | All seven commands dispatched: `validate-config`, `validate-matrix`, `check [ID] [config]`, `once [config] [stateDir]`, `status [stateDir]`, `test-matrix`, `run [config] [stateDir]`; signal handling; nonzero + sanitized errors; usage line |
| `internal/cli/cli.go` | Modified | New `RunCheck` (named/all checks once, per-check timeout as the single absolute deadline, **no store access** — no incident mutation, no send, nothing persisted; one sanitized line per check + summary; nonzero if any check failed or ID unknown). `RunAgent` now wires the full agent: scheduler (WP-03) + daily report scheduler (WP-11) + outbox delivery loop (WP-12) on the fail-closed Matrix transport; Matrix validation runs asynchronously (never a startup prerequisite — delivery fails closed while monitoring continues); report-time default normalized |
| `internal/config/config.go` | Modified | Unknown-key detection fixed (release gate): `toml.Decode` + `MetaData.Undecoded()` — unknown top-level/section/check keys are now hard errors (the previous stub never detected anything) |
| `internal/config/config_test.go` | Modified | Tests: unknown keys rejected (top-level/agent/check), `config/example.toml` loads and validates |
| `packaging/vshm.service` | Rewritten | Spec §6 unit: `User/Group=vshm`, `ExecStart=/usr/bin/vshm run`, `Restart=on-failure`, `RestartSec=5s`, `UMask=0077`, `StateDirectory=vshm` + `StateDirectoryMode=0700`, `ReadWritePaths=/var/lib/vshm`, `NoNewPrivileges`, `PrivateTmp`, `ProtectSystem=strict`, `ProtectHome`, `ProtectKernelTunables/Modules`, `ProtectControlGroups`, `LockPersonality`, `RestrictSUIDSGID`, `RestrictAddressFamilies=AF_UNIX AF_INET AF_INET6` (AF_UNIX = read-only system bus, AF_INET/6 = network), journal-only output |
| `Makefile` | Modified | `build`, `test` (`-race`), `lint`, `check`, `clean`, `validate-config`, `release` (CGO_ENABLED=0 GOOS=linux GOARCH=amd64 → `dist/vshm-linux-amd64`), `install` (DESTDIR-aware → `/usr/bin/vshm`, `/usr/lib/systemd/system/vshm.service`), `verify-unit` |
| `tests/e2e_test.go` | Created | End-to-end acceptance (spec §8, hermetic: local `httptest` target, temp SQLite, no Matrix account — `HOME`/`XDG_CONFIG_HOME` redirected so the transport fails closed locally): `once` records observations + one distinct failure event + open incident, recovery event on next success; `check` exit semantics + no DB side effects; `run` start/stop with prompt startup observation persisted and zero deliveries; second instance on the same state dir refused |
| `.gitignore` | Created | `/vshm`, `/dist/`, `*.test`, `/once/` |
| `vshm`, `http.test`, `once/` | Removed | Stray build artifact / test binary / local state DB were tracked in git by mistake; removed from index and worktree |
| `README.md` | Modified | WP-13 status, command summary, build/test section |
| `docs/operations.md` | Rewritten | Full install (account, `root:vshm 0750` secrets dir, `0640` config, binary+unit paths, validate-config + validate-matrix, enable/start), command table, day-to-day, per-user state-dir fallback, upgrade (backup `/var/lib/vshm` on the same host only, never copy crypto identity, stop→install→validate→start→status), troubleshooting |
| `docs/architecture.md` | Modified | CLI command contract + `run` wiring; deployment section (unit, paths, hardening) |
| `docs/vshm-spec.md` | Modified | `vshm once` added to the §6 command list (user decision recorded in `docs/wp13-cli-deployment.md`) |
| `docs/wp13-cli-deployment.md` | Modified | `once` open question marked answered; command list and runtime-validation steps updated |
| `docs/wp13-completion.md` | Created | This report |

## Design decisions

1. **`vshm once` is in the spec now**: the user confirmed adding it to
   `docs/vshm-spec.md` §6 (closing the DoD/spec inconsistency). It is a single
   full monitoring pass that persists observations/incidents/outbox events and
   prints one sanitized line per check plus a summary — distinct from
   `vshm check [ID]`, which has no side effects.
2. **`vshm check` argument order is `[ID] [config]`** (spec-literal `check [ID]`
   first; config path second, defaulting to `/etc/vshm/vshm.toml`). A failed
   check makes `check` exit nonzero (diagnostic semantics); infrastructure
   errors also exit nonzero.
3. **Matrix validation is asynchronous in `run`**: `transport.Validate` can
   block on subprocess/network, and the spec requires prompt startup and that
   Matrix availability is not a startup prerequisite. `Send` validates lazily
   and fails closed, so delivery is blocked until compatible while monitoring
   and persistence continue. An async validation goroutine (60 s bound) only
   produces an early journal signal with a sanitized category.
4. **Read-only system bus**: no systemd directive makes D-Bus read-only; the
   unit keeps `AF_UNIX` in `RestrictAddressFamilies` (bus socket reachable)
   while `ProtectSystem=strict` keeps `/run` read-only, and networking is
   permitted via `AF_INET AF_INET6`. All directives verified on systemd 259.
5. **Unknown-key detection repaired** in `internal/config` (`toml.Decode` +
   `Undecoded()`), required by the §8 release gate ("unknown keys fail clearly").

## Acceptance evidence (spec §8 + package-specific)

- `go build ./...`, `go vet ./...` clean; `go test -race ./...` — 15 packages ok,
  incl. new `tests` e2e package (4 tests) and new config unknown-key tests.
- Release: `make release` → `dist/vshm-linux-amd64`, `file` reports
  "ELF 64-bit … statically linked" (CGO_ENABLED=0, C-free), Go 1.27.3,
  pinned modules in `go.mod`/`go.sum`.
- `validate-config`: example config passes; unknown key, duplicate check ID,
  interval range violation, world-readable secret file each fail with exit 1
  and a clear sanitized message.
- Runtime validation with the built binary against a local fake HTTP target
  (`python3 -m http.server` on 127.0.0.1): `version` 0; `validate-config` 0;
  `check ok-http` 0 / `check fail-http` 1 / `check nope` 1 (unknown ID lists
  configured IDs); `once` exit 0 with per-check lines + `once: 2 checks run,
  1 failed`; `status` shows observations=2, pending=1 after `once` and
  observations=4, pending=2 after a 6 s `run` (startup pass recorded, failure
  + recovery persisted); `run` exits 0 on SIGTERM; `validate-matrix` in a
  hermetic HOME exits 1 with a clear blocked result; unknown command exits 1.
  Real `test-matrix` was **not** run (requires explicit approval + a validated
  host profile per `docs/matrix-cli-validation.md`).
- `systemd-analyze verify --root=<staged root> vshm.service` exits 0 with no
  findings for the unit (all sandbox directives valid on systemd 259);
  `systemd-analyze security --offline` reports exposure 7.0 MEDIUM with the
  full spec-mandated directive set applied.

## Definition of Done checklist (common DoD, overview.md)

- [x] 1. All files from the WP-13 "files to create/touch" list exist in the
      required tree (`cmd/vshm/main.go`, `internal/cli/`, `packaging/vshm.service`,
      `README.md`, `docs/operations.md`, `docs/architecture.md`,
      `docs/matrix-cli-validation.md`, `Makefile`, `tests/`); stray tracked
      artifacts removed
- [x] 2. `go build ./...` and `go vet ./...` pass
- [x] 3. `go test -race ./...` passes (whole repo, incl. `tests/e2e_test.go`)
- [x] 4. Runtime validation performed and recorded above (`validate-config`,
      `validate-matrix` blocked result, `check`, `once`, `status`, `run` vs a
      local fake target)
- [x] 5. Acceptance criteria ticked with evidence (above)
- [x] 6. Open questions asked and answered: `once` added to spec §6 (user);
      remaining questions (systemd version/D-Bus syntax) resolved by verification
      on systemd 259 — unit fails to load rather than dropping protections on
      older systemd; Matrix send-gate belongs to WP-12 and stays a precondition
      for enabling delivery
- [x] 7. WP-00 public interfaces unchanged (only additions inside `internal/cli`;
      the `internal/config` change is a bug fix required by the §8 gate, no
      signature changed)
- [x] 8. This completion report

## Addendum — startup delivery behavior (user-confirmed after initial implementation)

The user clarified the expected `vshm run` behavior: all checks execute right
away, and once that pass is done the report (and any failure messages) are sent
via Matrix immediately rather than at the next 30 s outbox tick. Changes:

- `internal/outbox`: `Manager.Flush(ctx)` processes one pending batch on demand
  (mutex-guarded, coalesces with the loop); `runLoop` now also processes one
  batch immediately at start, so events pending before a restart are retried
  promptly.
- `internal/reports`: exported `ReportNow(ctx)` (idempotent per local date via
  the dedupe key).
- `internal/cli`: `passObserver` wraps the incident observer — after every
  recorded observation it fires an async outbox flush (spec §1 "alert
  immediately after the observation completes"), and it closes a `startupDone`
  channel once every configured check has produced its startup observation.
  `RunAgent` then calls `ReportNow` + `Flush` (first report sent immediately
  after the startup pass; afterwards `Schedule` = next-future-only, no catch-up)
  and `RunOnce` flushes pending events before exiting. `RunAgent`/`RunOnce`
  accept a `WithTransportFactory` option so tests inject a fake transport.
- Spec updated: `docs/vshm-spec.md` §6 (`once`/`run` semantics) and §5
  (user-confirmed exception: first report at startup after the prompt pass,
  date key still prevents duplicates).
- Evidence: `internal/cli/cli_test.go` (RunOnce sends the failure message when
  the pass is done and marks it delivered; fail-closed transport keeps it
  pending; RunAgent sends the startup report after the first pass),
  `internal/outbox` `TestFlushDeliversPendingImmediately`, plus a live 40 s
  `vshm run` showing the failure event and `report-<date>` enqueued+flushed
  immediately after the startup pass (sends fail closed here because the
  sandbox Matrix account is not logged in; on a validated host they succeed).

## Known limitations

1. Real Matrix `test-matrix` send not exercised here — requires operator
   approval and a target-validated profile (`docs/matrix-cli-validation.md`);
   until then delivery fails closed and events stay pending (by design).
2. `vshm run` was validated in foreground for seconds, not under a live systemd
   manager in this environment; the unit itself passes `systemd-analyze verify`.
3. `systemd-analyze security` MEDIUM score reflects spec-external hardening
   (e.g. `ProtectKernelLogs`, `MemoryDenyWriteExecute`) that the spec does not
   require; the spec's directive list is fully applied.
