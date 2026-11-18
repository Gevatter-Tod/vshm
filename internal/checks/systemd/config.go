package systemd

import "time"

// systemdConfig is a typed immutable check config for direct adapter use and
// tests. The scheduler path hands adapters a *scheduler.Spec instead; Run
// accepts both.
type systemdConfig struct {
	checkID string
	target  string
	timeout time.Duration
}

// NewSystemdConfig creates a systemd check config for the given unit.
func NewSystemdConfig(checkID, target string, timeout time.Duration) *systemdConfig {
	return &systemdConfig{checkID: checkID, target: target, timeout: timeout}
}

// CheckID returns the check ID.
func (c *systemdConfig) CheckID() string { return c.checkID }

// CheckType returns the check type.
func (c *systemdConfig) CheckType() string { return "systemd" }
