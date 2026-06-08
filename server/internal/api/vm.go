package api

import (
	"errors"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// vmProviderError maps a vprovider error to the canonical API error, mirroring the
// container side's ErrUnsupported->405 / ErrNotFound->404 / ErrConflict->409 /
// ErrInvalidSpec->422 contract (ADR-UNIHV-002 §"uniform errors").
func vmProviderError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, vprovider.ErrUnsupported):
		return authz.ErrMethodNotAllowed
	case errors.Is(err, vprovider.ErrNotFound):
		return authz.ErrNotFound
	case errors.Is(err, vprovider.ErrConflict):
		return authz.ErrConflict
	case errors.Is(err, vprovider.ErrInvalidSpec):
		return authz.Errorf(authz.ErrValidation, "Invalid specification for this hypervisor.")
	default:
		return err
	}
}

// scopeFromProvider derives the RBAC scope from the {providerID} URL param so VM
// actions can be host/provider-scoped (a global grant still matches everything),
// mirroring scopeFromHost on the container side.
func scopeFromProvider(r *http.Request) authz.Scope {
	if id := chi.URLParam(r, "providerID"); id != "" {
		return authz.Scope{Type: "host", ID: id}
	}
	return authz.Scope{Type: "global"}
}

// resolveVMProvider fetches the provider named by {providerID}, or writes 404.
func (s *Server) resolveVMProvider(w http.ResponseWriter, r *http.Request) (vprovider.HypervisorProvider, bool) {
	id := chi.URLParam(r, "providerID")
	p, ok := s.vreg.Get(id)
	if !ok {
		authz.WriteError(w, r, authz.Errorf(authz.ErrNotFound, "Unknown hypervisor provider: "+id))
		return nil, false
	}
	return p, true
}

// --- unified inventory ---

// UnifiedInventory returns the merged VM + container inventory (the single-pane view).
func (s *Server) UnifiedInventory(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	u := s.agg.All(ctx, time.Now().UTC())
	ok(w, u)
}

// --- providers & clusters ---

// VMProviders lists the registered hypervisor providers + their capabilities (so
// the UI greys out unsupported actions pre-flight, like /providers for containers).
func (s *Server) VMProviders(w http.ResponseWriter, r *http.Request) {
	type provInfo struct {
		ID           string   `json:"id"`
		Kind         string   `json:"kind"`
		Capabilities []string `json:"capabilities"`
	}
	out := []provInfo{}
	for _, p := range s.vreg.List() {
		out = append(out, provInfo{ID: p.ID(), Kind: string(p.Kind()), Capabilities: p.Capabilities().Strings()})
	}
	ok(w, out)
}

// VMClusters lists clusters for a provider with their topology-capable flag.
func (s *Server) VMClusters(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	cls, err := p.ListClusters(ctx)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, cls)
}

// VMClusterTopology returns a cluster's node/placement topology.
func (s *Server) VMClusterTopology(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	top, err := p.GetClusterTopology(ctx, chi.URLParam(r, "clusterID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, top)
}

// --- VM reads ---

// VMs lists the VMs of a provider.
func (s *Server) VMs(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	vms, err := p.ListVMs(ctx, vprovider.ListOptions{
		HostID:    r.URL.Query().Get("host"),
		ClusterID: r.URL.Query().Get("cluster"),
		State:     vprovider.VMState(r.URL.Query().Get("state")),
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, vms)
}

// VMDetailHandler returns one VM with its raw engine document.
func (s *Server) VMDetailHandler(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	d, err := p.GetVM(ctx, chi.URLParam(r, "vmID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, d)
}

// VMSnapshots lists a VM's snapshots.
func (s *Server) VMSnapshots(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	snaps, err := p.ListSnapshots(ctx, chi.URLParam(r, "vmID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, snaps)
}

// VMMetrics returns a VM's metric series.
func (s *Server) VMMetrics(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	ms, err := p.GetMetrics(ctx, chi.URLParam(r, "vmID"), vprovider.MetricWindow{StepSecond: 30})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, ms)
}

// VMHosts lists a provider's hosts.
func (s *Server) VMHosts(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	hosts, err := p.ListHosts(ctx)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, hosts)
}

// VMStorage lists a provider's storage pools/datastores.
func (s *Server) VMStorage(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	sp, err := p.ListStorage(ctx)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, sp)
}

// VMNetworks lists a provider's networks.
func (s *Server) VMNetworks(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	nets, err := p.ListNetworks(ctx)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, nets)
}
