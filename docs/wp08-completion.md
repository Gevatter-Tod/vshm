# WP-08 Completion — read-only bounded-depth WebDAV check adapter

**Status**: ✅ Completed

## What was built

- `internal/checks/webdav/webdav.go` — the adapter implementing the WP-00 `model.Check`
  contract (`Type() == "webdav"`, `Run(ctx, cfg) model.CheckResult`). It sends exactly one
  read-only `PROPFIND` (never PUT/DELETE/MOVE/MKCOL/COPY) with a constant small
  `allprop` body and a `Depth` header that is **always exactly "0" or "1"** (never
  `infinity`). Success requires HTTP **207** plus a well-formed XML body whose root is
  `{DAV:}multistatus` with at least one `response` containing a non-empty `href`, where
  **every** response has at least one `propstat` with a 2xx `status`. The body is capped
  at 1 MiB (read with `LimitReader(cap+1)`), response processing is bounded (body cap +
  10 000-response guard), and all network + XML processing runs under the single absolute
  deadline from `ctx` plus the configured client timeout. TLS verification is mandatory
  (`InsecureSkipVerify: false`, min TLS 1.2). Stable sanitized reason codes: `success`,
  `invalid_config`, `invalid_url`, `credential_not_https`, `credential_error`,
  `timeout`, `request_failed`, `bad_status`, `body_too_large`, `malformed_xml`,
  `wrong_root`, `no_response`, `no_href`, `no_successful_propstat`, `too_many_responses`.
  Reasons never contain the body, the URL (which could embed userinfo), credentials, or
  secret-bearing paths; HTTP status and XML structure errors are separate codes; network
  error text is never surfaced.
- `internal/checks/webdav/xml.go` — streaming `encoding/xml` validator with explicit
  namespace/local-name checks (matches the `DAV:` namespace URI, not the prefix), strict
  mode; external entities are not resolved by `encoding/xml`. Sentinel errors mapped to
  reason codes; no body content escapes.
- `internal/checks/webdav/config.go` — typed immutable `Config` (implements
  `model.CheckConfig`); both direct construction and the scheduler path
  (`*scheduler.Spec` with `WebDAV` fields) are accepted, mirroring the mysql adapter.
- `internal/checks/webdav/secrets.go` — INI `[basic]` credential loader
  (`username`/`password`, comments, quoted values). Fails closed on world-readable or
  group/other-writable files; errors never contain the path or file contents.
- Wiring: `internal/config/config.go` gained the `depth` TOML key and
  `validateWebDAVCheck` (url required with http/https scheme, `depth` exactly 0 or 1,
  `credential_file` only with an https URL + shared secret-file rules — Basic is never
  allowed over plain HTTP); `internal/scheduler/entries.go` carries the fields in
  `Spec.WebDAV` (`WebDAVSpec{URL, Depth, CredentialFile}`); the adapter was already
  registered in `internal/cli/cli.go`. `config/example.toml` already documented
  `depth`/`credential_file` and now validates.
- Fixtures: `tests/fixtures/webdav/basic.ini` (placeholder `[basic]`, mode 0640),
  `tests/fixtures/webdav/multistatus-207.xml` (valid sample 207 body, also served by the
  fake endpoint in runtime validation).
- Tests: `internal/checks/webdav/webdav_test.go` (httptest fake PROPFIND endpoints) and
  `internal/config/config_test.go` (schema validation).

## Answers chosen for the WP-08 open questions (§7, asked before implementing)

- **Response-body cap**: **1 MiB**, the same safe cap as the HTTP check
  (`maxBodySize = 1024*1024`); larger bodies fail with `body_too_large`.
- **Redirects**: **follow redirects** (Go client default) for WebDAV — this overrides the
  §5 proposal "redirects disabled"; the user explicitly chose "Follow redirects" when
  asked. Note the Go client downgrades 301/302/303 on non-GET/HEAD methods to GET (so a
  302 to a non-207 endpoint simply fails with `bad_status` — safe), while 307/308 preserve
  PROPFIND; the client strips `Authorization` on cross-host redirects.
- **Malformed/non-2xx propstat edges**: a response-level bare `status` (without propstat)
  does **not** count as a successful propstat; a propstat `status` line must parse as
  `HTTP/x.y <2xx> ...` (e.g. `HTTP/1.1 200 OK`); mixed propstats (one 2xx + one 404) are OK;
  an unparseable status line is treated as non-2xx.

## Definition of Done evidence

1. Files exist in the required tree (`internal/checks/webdav/` now has
   `webdav.go`, `config.go`, `secrets.go`, `xml.go`, `webdav_test.go`;
   `tests/fixtures/webdav/`); no WP-00 public interface changed (`model.*` untouched; only
   additive fields on `scheduler.Spec` and `config.CheckConfig`).
2. `go build ./...`, `go vet ./...` (`make lint`) pass; `gofmt` clean on all touched files.
3. `go test -race ./...` passes for the whole repo, including the new webdav package
   (19 tests) and config validation tests.
4. Runtime validation (recorded 2026-11-20): `CGO_ENABLED=0 go build -o vshm ./cmd/vshm`;
   `./vshm validate-config config/example.toml` → OK (5 checks, webdav `depth = 0` now a
   known key); `./vshm validate-config /tmp/wp08-runtime.toml` → OK (4 webdav checks);
   negative configs rejected with exit 1 (`depth must be 0 or 1 for webdav checks`;
   `credential_file is only permitted with HTTPS (verified TLS) for webdav checks`).
   `./vshm once /tmp/wp08-runtime.toml /tmp/wp08-state` against a local fake PROPFIND
   endpoint (python `http.server`, serving `tests/fixtures/webdav/multistatus-207.xml`):
   `dav-ok-depth1`/`dav-ok-depth0` → `OK reason_code=success`; `dav-wrong-status` (200 +
   HTML) → `FAIL reason_code=bad_status reason="unexpected HTTP status 200, expected
   207"`; `dav-no-propstat` (207, all propstats 404) → `FAIL
   reason_code=no_successful_propstat`. The fake server's capture log shows only
   `method=PROPFIND` with `Depth='0'`/`Depth='1'` and no `Authorization` header;
   `strings` on the resulting `vshm.db` finds no secret/password material.
5. Acceptance criteria (wp08 §8): 207 valid multistatus, wrong root (HTML / wrong local
   name / wrong namespace), malformed XML, no response, no href, response missing a
   successful propstat, mixed 2xx+404 propstat success, wrong HTTP status (200/204/401/500),
   depth 0 and 1 on the wire, depth 2/-1 rejected, client timeout and ctx deadline →
   `timeout`, redirect followed (307 preserves PROPFIND), only PROPFIND ever sent,
   credentials only over verified HTTPS (Basic header asserted on TLS server; rejected
   over plain HTTP with no Authorization sent), mandatory TLS verification (untrusted
   test CA fails), credential file fail-closed permissions, reason redaction — all covered
   by unit tests.
6. Open questions asked and answered as documented above.
7. WP-00 interfaces unchanged.
8. This report.

## Known limitations

- Redirect following uses the Go client default (max 10 hops); 301/302/303 downgrade
  PROPFIND to GET per RFC/Go rules, so only 307/308 preserve the WebDAV method across a
  redirect. This is the user-confirmed §7 answer, not the §5 proposal.
- End-to-end verified-TLS behavior is unit-tested via `httptest.NewTLSServer` with an
  injected transport (production path keeps `InsecureSkipVerify: false`); a live WebDAV
  server is intentionally not used in CI.
- `depth` defaults to 0 when omitted (0 is the TOML zero value), but the `Depth` header is
  always sent explicitly, so the request is never unbounded.
