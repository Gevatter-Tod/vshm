# vshm — host monitor agent

Very Simple Host Monitor

One independent Linux amd64 agent per server. Observes configured checks,
records observations/incidents in local SQLite, and queues plain‑text events and
daily reports for the host's Matrix account via `matrix-commander-rs` (E2EE
mandatory). No coordinator, UI, API, remediation, or alternate transport.

## Status

V 0.0.1: All base functions have been implemented.

## Toolchain (fixed)

- Go **1.27.3**, `CGO_ENABLED=0`, Linux amd64 only.
- Pinned: `github.com/BurntSushi/toml v1.6.0`, `github.com/go-sql-driver/mysql v1.10.1`,
  `modernc.org/sqlite v1.61.1` (pure Go).

## Build / test

```bash
make build        # CGO_ENABLED=0 go build -o vshm ./cmd/vshm
make test         # go test -race ./...
make lint         # go vet ./...
make release      # dist/vshm-linux-amd64 (C-free linux/amd64 artifact)
make verify-unit  # systemd-analyze verify packaging/vshm.service
```

## Build / test