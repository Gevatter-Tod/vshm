package webdav

import (
	"errors"
	"time"

	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

// Config is the typed immutable WebDAV check configuration. The scheduler path
// hands adapters a *scheduler.Spec; Run accepts both. Fields are unexported so
// the struct stays immutable after construction and implements model.CheckConfig.
type Config struct {
	checkID        string
	url            string
	depth          int // exactly 0 or 1; always sent as the Depth header, never unbounded
	credentialFile string
	timeout        time.Duration
}

// NewConfig creates a WebDAV check config. depth must be 0 or 1. credentialFile
// is optional and only honored for https URLs.
func NewConfig(checkID, url string, depth int, credentialFile string, timeout time.Duration) *Config {
	return &Config{
		checkID:        checkID,
		url:            url,
		depth:          depth,
		credentialFile: credentialFile,
		timeout:        timeout,
	}
}

// CheckID returns the check ID.
func (c *Config) CheckID() string {
	if c == nil {
		return ""
	}
	return c.checkID
}

// CheckType returns the check type.
func (c *Config) CheckType() string { return "webdav" }

// fromSpec converts the scheduler's generic spec into a typed webdav Config.
func fromSpec(s *scheduler.Spec) (*Config, error) {
	if s == nil || s.WebDAV == nil {
		return nil, errors.New("webdav check config missing from spec")
	}
	w := s.WebDAV
	return &Config{
		checkID:        s.ID,
		url:            w.URL,
		depth:          w.Depth,
		credentialFile: w.CredentialFile,
		timeout:        s.Timeout,
	}, nil
}
