# Matrix CLI Validation

## Overview

vshm uses the externally installed `matrix-commander-rs` CLI for encrypted Matrix event delivery. This document describes the validation requirements.

## Requirements

1. **matrix-commander-rs** must be installed on the host
2. **Profile configuration** must exist at `/etc/matrix-commander/profile.ini`
3. **Profile validation** must succeed before vshm starts

## Validation Steps

### 1. Check CLI Availability

```bash
which matrix-commander-rs
```

### 2. Validate Profile

```bash
matrix-commander-rs validate-profile
```

### 3. Test Event Delivery

```bash
matrix-commander-rs send-event --test
```

## Configuration

The Matrix profile must include:

- `server`: Matrix server URL
- `user`: Matrix user ID
- `device`: Device name
- `encryption`: E2EE enabled

## Error Handling

vshm handles Matrix delivery errors gracefully:

- **Failure**: Retry with exponential backoff
- **Ambiguous**: Mark event as ambiguous, skip retry
- **Expired**: Evict after retention period

## Security

- Profile credentials never logged
- Profile path never exposed in error messages
- E2EE mandatory for all events
