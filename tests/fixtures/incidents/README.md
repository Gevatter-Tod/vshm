# WP-10 incident fixtures

Used to exercise `vshm once` against a local, scripted HTTP target and then
inspect the persisted observations/incidents/outbox via `vshm status` and the
SQLite outbox table. No Matrix account or network target is needed.

- `healthy.toml` — http check expecting status 200 (succeeds against
  `python3 -m http.server 18099 --bind 127.0.0.1`).
- `failing.toml` — same `check_id` (`local-http`) expecting status 500
  (always fails against the same server).

Sequence that reproduces the WP-10 semantics against one state dir:

```sh
vshm once tests/fixtures/incidents/failing.toml /tmp/vshm-wp10-state   # failure event 1, incident opens
vshm once tests/fixtures/incidents/failing.toml /tmp/vshm-wp10-state   # failure event 2 (distinct ID), incident stays open
vshm once tests/fixtures/incidents/healthy.toml /tmp/vshm-wp10-state   # recovery event, incident closes
vshm once tests/fixtures/incidents/healthy.toml /tmp/vshm-wp10-state   # silent: no new event
vshm status /tmp/vshm-wp10-state
```
