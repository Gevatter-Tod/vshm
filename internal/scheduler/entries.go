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

// spec is the immutable model.CheckConfig handed to adapters by EntriesFromConfig.
type spec struct {
	id, typ string
}

func (s spec) CheckID() string   { return s.id }
func (s spec) CheckType() string { return s.typ }

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
		entries = append(entries, Entry{
			Check:    adapter,
			Config:   spec{id: c.CheckID, typ: c.Type},
			Interval: iv,
			Timeout:  to,
		})
	}
	return entries, nil
}
