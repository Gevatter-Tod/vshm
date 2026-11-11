package scheduler

import (
	"fmt"
	"sort"
	"sync"

	"github.com/Gevatter-Tod/vshm/internal/model"
)

// Registry is a thread-safe model.Registry keyed by stable check type.
type Registry struct {
	mu     sync.RWMutex
	checks map[string]model.Check
}

var _ model.Registry = (*Registry)(nil)

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{checks: make(map[string]model.Check)}
}

// Register adds an adapter under checkType. Duplicate or empty types are rejected.
func (r *Registry) Register(checkType string, check model.Check) error {
	if checkType == "" {
		return fmt.Errorf("register check: empty type")
	}
	if check == nil {
		return fmt.Errorf("register check %q: nil adapter", checkType)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.checks[checkType]; ok {
		return fmt.Errorf("register check %q: already registered", checkType)
	}
	r.checks[checkType] = check
	return nil
}

// Get returns the adapter registered for checkType.
func (r *Registry) Get(checkType string) (model.Check, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	c, ok := r.checks[checkType]
	return c, ok
}

// Types returns the registered check types in sorted order.
func (r *Registry) Types() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	types := make([]string, 0, len(r.checks))
	for t := range r.checks {
		types = append(types, t)
	}
	sort.Strings(types)
	return types
}
