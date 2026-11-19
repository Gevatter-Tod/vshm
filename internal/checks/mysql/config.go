package mysql

import (
	"errors"

	"github.com/Gevatter-Tod/vshm/internal/scheduler"
)

// DefaultPort is the TCP port used when the check config does not set one.
const DefaultPort = 3306

// Config is the typed immutable MySQL check configuration. The scheduler path
// hands adapters a *scheduler.Spec; Run accepts both. Fields are unexported so
// the struct stays immutable after construction and implements model.CheckConfig.
type Config struct {
	checkID        string
	host           string
	port           int
	socket         string
	database       string
	credentialFile string
	tlsCA          string
	query          string
}

// NewConfig creates a MySQL check config. Exactly one of host (TCP, requires
// tlsCA) or socket (local Unix socket, no network TLS) must be set.
func NewConfig(checkID, host string, port int, socket, database, credentialFile, tlsCA, query string) *Config {
	return &Config{
		checkID:        checkID,
		host:           host,
		port:           port,
		socket:         socket,
		database:       database,
		credentialFile: credentialFile,
		tlsCA:          tlsCA,
		query:          query,
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
func (c *Config) CheckType() string { return "mysql" }

// fromSpec converts the scheduler's generic spec into a typed mysql Config.
func fromSpec(s *scheduler.Spec) (*Config, error) {
	if s == nil || s.MySQL == nil {
		return nil, errors.New("mysql check config missing from spec")
	}
	m := s.MySQL
	return &Config{
		checkID:        s.ID,
		host:           m.Host,
		port:           m.Port,
		socket:         m.Socket,
		database:       m.Database,
		credentialFile: m.CredentialFile,
		tlsCA:          m.TLSCA,
		query:          m.Query,
	}, nil
}
