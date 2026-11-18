# Operations Guide

## Installation

### Systemd Service

```bash
sudo cp packaging/vshm.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now vshm
```

### Manual Installation

```bash
sudo cp vshm /usr/bin/
sudo chown root:vshm /usr/bin/vshm
sudo chmod 755 /usr/bin/vshm
```

## Configuration

### Example Configuration

```toml
[[checks]]
  check_id = "systemd-check"
  type = "systemd"
  target = "multi-user.target"
  interval = "5m"
  timeout = "30s"

[[checks]]
  check_id = "http-check"
  type = "http"
  url = "https://example.com/health"
  interval = "1m"
  timeout = "10s"

[[checks]]
  check_id = "ping-check"
  type = "ping"
  host = "192.168.1.1"
  interval = "5m"
  timeout = "10s"
```

### Configuration Location

- **Path**: `/etc/vshm/vshm.toml`
- **Owner**: root
- **Group**: vshm
- **Permissions**: 0640

## Secrets

### Secret Files

```bash
sudo mkdir -p /etc/vshm/secrets
sudo chown root:vshm /etc/vshm/secrets
sudo chmod 750 /etc/vshm/secrets
```

### MySQL Credentials

Create `/etc/vshm/secrets/mysql.ini`:

```ini
[mysql]
user=monitor
password=secret
host=localhost
port=3306
database=monitoring
```

## Monitoring

### Check Status

```bash
systemctl status vshm
```

### View Logs

```bash
journalctl -u vshm -f
```

### Database Inspection

```bash
sqlite3 /var/lib/vshm/vshm.db
```

## Troubleshooting

### Configuration Errors

```bash
./vshm validate-config
```

### Test Single Check

```bash
./vshm once --check systemd-check
```

### Manual Run

```bash
sudo -u vshm /usr/bin/vshm
```

## Maintenance

### Update

```bash
sudo systemctl stop vshm
sudo cp vshm /usr/bin/
sudo systemctl start vshm
```

### Database Backup

```bash
sudo cp /var/lib/vshm/vshm.db /backup/vshm.db
```

### Database Pruning

```bash
sqlite3 /var/lib/vshm/vshm.db "DELETE FROM results_history WHERE completed_at_utc < datetime('now', '-30 days');"
```

## Security

### User Creation

```bash
sudo useradd -r -s /bin/false vshm
```

### File Permissions

```bash
sudo chown -R root:vshm /etc/vshm
sudo chown -R vshm:vshm /var/lib/vshm
sudo chmod 700 /var/lib/vshm
```
