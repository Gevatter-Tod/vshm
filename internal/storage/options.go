// Package storage provides storage configuration and shared storage errors.
package storage

import (
	"errors"
	"time"
)

// ErrAlreadyRunning indicates that another process holds the state-directory lock.
var ErrAlreadyRunning = errors.New("vshm is already running")

// Options configures retention and the maximum number of pending outbox rows.
type Options struct {
	ObservationRetention time.Duration
	DeliveredRetention   time.Duration
	PendingMaxAge        time.Duration
	MaxPending           int
}

// WithDefaults fills unset options with the storage package defaults.
func (o Options) WithDefaults() Options {
	if o.ObservationRetention <= 0 {
		o.ObservationRetention = 90 * 24 * time.Hour
	}
	if o.DeliveredRetention <= 0 {
		o.DeliveredRetention = 30 * 24 * time.Hour
	}
	if o.PendingMaxAge <= 0 {
		o.PendingMaxAge = 24 * time.Hour
	}
	if o.MaxPending <= 0 {
		o.MaxPending = 1000
	}
	return o
}
