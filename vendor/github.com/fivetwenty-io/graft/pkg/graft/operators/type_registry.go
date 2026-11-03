package operators

import (
	"sort"
	"sync"
)

// globalRegistry is the thread-safe singleton type registry.
var (
	globalRegistry     *ThreadSafeTypeRegistry
	globalRegistryOnce sync.Once
)

// ThreadSafeTypeRegistry is a thread-safe implementation of the type registry
// that supports hybrid operation: global defaults plus engine-specific overrides.
type ThreadSafeTypeRegistry struct {
	mu       sync.RWMutex
	handlers []TypeHandler
}

// NewThreadSafeTypeRegistry creates a new thread-safe type registry.
func NewThreadSafeTypeRegistry() *ThreadSafeTypeRegistry {
	return &ThreadSafeTypeRegistry{
		handlers: make([]TypeHandler, 0),
	}
}

// Register adds a new type handler to the registry with priority-based sorting.
func (r *ThreadSafeTypeRegistry) Register(handler TypeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers = append(r.handlers, handler)
	r.sortHandlers()
}

// RegisterAll adds multiple type handlers to the registry.
func (r *ThreadSafeTypeRegistry) RegisterAll(handlers ...TypeHandler) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers = append(r.handlers, handlers...)
	r.sortHandlers()
}

// sortHandlers sorts handlers by priority (highest first)
// Must be called with lock held.
func (r *ThreadSafeTypeRegistry) sortHandlers() {
	sort.Slice(r.handlers, func(i, j int) bool {
		return r.handlers[i].Priority() > r.handlers[j].Priority()
	})
}

// FindHandler finds the appropriate handler for the given operand types.
func (r *ThreadSafeTypeRegistry) FindHandler(aType, bType OperandType) TypeHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, handler := range r.handlers {
		if handler.CanHandle(aType, bType) {
			return handler
		}
	}
	return nil
}

// Handlers returns a copy of the registered handlers.
func (r *ThreadSafeTypeRegistry) Handlers() []TypeHandler {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make([]TypeHandler, len(r.handlers))
	copy(result, r.handlers)
	return result
}

// Clone creates a copy of this registry for engine-specific overrides.
func (r *ThreadSafeTypeRegistry) Clone() *ThreadSafeTypeRegistry {
	r.mu.RLock()
	defer r.mu.RUnlock()

	clone := NewThreadSafeTypeRegistry()
	clone.handlers = make([]TypeHandler, len(r.handlers))
	copy(clone.handlers, r.handlers)
	return clone
}

// Clear removes all handlers from the registry.
func (r *ThreadSafeTypeRegistry) Clear() {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.handlers = make([]TypeHandler, 0)
}

// GetGlobalThreadSafeRegistry returns the global thread-safe type registry singleton.
func GetGlobalThreadSafeRegistry() *ThreadSafeTypeRegistry {
	globalRegistryOnce.Do(func() {
		globalRegistry = NewThreadSafeTypeRegistry()
	})
	return globalRegistry
}

// EngineTypeRegistry wraps a base registry with engine-specific overrides
// This allows engines to have their own handler configurations while falling back
// to the global defaults.
type EngineTypeRegistry struct {
	base      *ThreadSafeTypeRegistry
	overrides *ThreadSafeTypeRegistry
}

// NewEngineTypeRegistry creates a new engine-specific registry that wraps the global registry.
func NewEngineTypeRegistry() *EngineTypeRegistry {
	return &EngineTypeRegistry{
		base:      GetGlobalThreadSafeRegistry(),
		overrides: NewThreadSafeTypeRegistry(),
	}
}

// NewEngineTypeRegistryWithBase creates an engine registry with a custom base registry.
func NewEngineTypeRegistryWithBase(base *ThreadSafeTypeRegistry) *EngineTypeRegistry {
	return &EngineTypeRegistry{
		base:      base,
		overrides: NewThreadSafeTypeRegistry(),
	}
}

// RegisterOverride adds an engine-specific handler that takes precedence over global handlers.
func (r *EngineTypeRegistry) RegisterOverride(handler TypeHandler) {
	r.overrides.Register(handler)
}

// FindHandler finds the appropriate handler, checking overrides first then base.
func (r *EngineTypeRegistry) FindHandler(aType, bType OperandType) TypeHandler {
	// First check engine-specific overrides
	if handler := r.overrides.FindHandler(aType, bType); handler != nil {
		return handler
	}
	// Fall back to base registry
	return r.base.FindHandler(aType, bType)
}

// GetBase returns the base registry.
func (r *EngineTypeRegistry) GetBase() *ThreadSafeTypeRegistry {
	return r.base
}

// GetOverrides returns the overrides registry.
func (r *EngineTypeRegistry) GetOverrides() *ThreadSafeTypeRegistry {
	return r.overrides
}
