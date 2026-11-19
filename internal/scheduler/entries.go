package scheduler

import (
	"fmt"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/config"
	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Defaults and limits from the specification.
const (
	DefaultInterval = 30 * time.Minute
	DefaultTimeout  = 10 * time.Second
	MinInterval     = time.Minute
	MaxInterval     = 7 * 24 * time.Hour
	MinTimeout      = time.Second
	MaxTimeout      = 300 * time.Second
)

// Spec is the immutable model.CheckConfig handed to adapters by EntriesFromConfig.
type Spec struct {
	ID             string
	Typ            string
	URL            string
	ExpectedStatus int
	BodyContains   string
	CredentialFile string
	Timeout        time.Duration
	// Ping/systemd shared field
	Target string
	// MySQL-specific fields (nil unless Typ == "mysql")
	MySQL *MySQLSpec
}

// MySQLSpec carries the typed MySQL check fields into the mysql adapter.
type MySQLSpec struct {
	Host           string
	Port           int    // 0 means default 3306 (TCP only)
	Socket         string // local Unix socket; mutually exclusive with Host/Port/TLSCA
	Database       string // explicit check database overrides the credential-file database
	CredentialFile string
	TLSCA          string // required for TCP
	Query          string // single read-only SELECT
}

// CheckID returns the check ID.
func (s *Spec) CheckID() string {
	if s == nil {
		return ""
	}
	return s.ID
}

// CheckType returns the check type.
func (s *Spec) CheckType() string {
	if s == nil {
		return ""
	}
	return s.Typ
}

// pingHost returns the ping target host, or empty string if not a ping check.
func (s *Spec) PingHost() string {
	if s == nil {
		return ""
	}
	return s.Target
}

// EntriesFromConfig resolves every configured check into a schedulable Entry:
// the agent-level interval/timeout apply unless the check overrides them, and the
// adapter is looked up in reg by check type. The resolved limits are re-validated
// here because the effective pair (timeout < interval) depends on the override mix.
func EntriesFromConfig(cfg *config.Config, reg model.Registry) ([]Entry, error) {
	interval, timeout := cfg.Agent.Interval, cfg.Agent.Timeout
	if interval == 0 {
		interval = DefaultInterval
	}
	if timeout == 0 {
		timeout = DefaultTimeout
	}

	entries := make([]Entry, 0, len(cfg.Checks))
	for i, c := range cfg.Checks {
		iv, to := interval, timeout
		var err error
		if c.Interval != "" {
			if iv, err = time.ParseDuration(c.Interval); err != nil {
				return nil, fmt.Errorf("checks[%d] %q: invalid interval: %w", i, c.CheckID, err)
			}
		}
		if c.Timeout != "" {
			if to, err = time.ParseDuration(c.Timeout); err != nil {
				return nil, fmt.Errorf("checks[%d] %q: invalid timeout: %w", i, c.CheckID, err)
			}
		}
		if iv < MinInterval || iv > MaxInterval {
			return nil, fmt.Errorf("checks[%d] %q: interval %v outside %v..%v", i, c.CheckID, iv, MinInterval, MaxInterval)
		}
		if to < MinTimeout || to > MaxTimeout {
			return nil, fmt.Errorf("checks[%d] %q: timeout %v outside %v..%v", i, c.CheckID, to, MinTimeout, MaxTimeout)
		}
		if to >= iv {
			return nil, fmt.Errorf("checks[%d] %q: timeout %v must be less than interval %v", i, c.CheckID, to, iv)
		}
		adapter, ok := reg.Get(c.Type)
		if !ok {
			return nil, fmt.Errorf("checks[%d] %q: no adapter registered for type %q", i, c.CheckID, c.Type)
		}

		// Pass check-specific fields to spec
		var httpFields struct {
			url            string
			expectedStatus int
			bodyContains   string
			credentialFile string
		}
		target := ""
		if c.Type == "http" {
			httpFields.url = c.URL
			httpFields.expectedStatus = c.ExpectedStatus
			httpFields.bodyContains = c.BodyContains
			httpFields.credentialFile = c.CredentialFile
		}
		if c.Type == "ping" {
			target = c.Host
		}
		if c.Type == "systemd" {
			target = c.Target
		}
		var mys *MySQLSpec
		if c.Type == "mysql" {
			mys = &MySQLSpec{
				Host:           c.Host,
				Port:           c.Port,
				Socket:         c.Socket,
				Database:       c.Database,
				CredentialFile: c.CredentialFile,
				TLSCA:          c.TLSCA,
				Query:          c.Query,
			}
		}

		entries = append(entries, Entry{
			Check:    adapter,
			Config:   &Spec{ID: c.CheckID, Typ: c.Type, URL: httpFields.url, ExpectedStatus: httpFields.expectedStatus, BodyContains: httpFields.bodyContains, CredentialFile: httpFields.credentialFile, Target: target, MySQL: mys},
			Interval: iv,
			Timeout:  to,
		})
	}
	return entries, nil
}
