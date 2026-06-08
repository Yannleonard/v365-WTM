package vprovider

import "sync"

// Registry holds the active hypervisor providers, keyed by Provider.ID(). The API
// and unified inventory resolve a VM/host to its owning provider via ProviderID.
// Adding a hypervisor host = registering another HypervisorProvider; no API/UI
// change. Mirrors the container-domain provider.Registry. Concurrency-safe.
type Registry struct {
	mu        sync.RWMutex
	providers map[string]HypervisorProvider
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{providers: make(map[string]HypervisorProvider)}
}

// Register adds or replaces a provider (keyed by its ID()).
func (r *Registry) Register(p HypervisorProvider) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.providers[p.ID()] = p
}

// Deregister removes a provider by id and returns it for cleanup, if present.
func (r *Registry) Deregister(id string) (HypervisorProvider, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[id]
	if ok {
		delete(r.providers, id)
	}
	return p, ok
}

// Get returns the provider with the given id.
func (r *Registry) Get(id string) (HypervisorProvider, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.providers[id]
	return p, ok
}

// List returns all registered providers (order unspecified).
func (r *Registry) List() []HypervisorProvider {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]HypervisorProvider, 0, len(r.providers))
	for _, p := range r.providers {
		out = append(out, p)
	}
	return out
}

// Len returns the number of registered providers.
func (r *Registry) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.providers)
}
