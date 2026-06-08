// Package inventory aggregates BOTH domains — virtual machines (vprovider) and
// containers/workloads (provider/cache) — into a single, unified view. This is
// what makes UniHV "one console": a caller asks the inventory for everything and
// gets VMs, hosts, clusters, storage, networks AND containers side by side,
// regardless of hypervisor or orchestrator. It is the read aggregator the unified
// API + dashboard consume.
//
// It NEVER imports a hypervisor/orchestrator SDK; it composes the two registries.
package inventory

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// Unified is the merged cross-domain inventory snapshot returned to the API/UI.
type Unified struct {
	// VM domain.
	VMs      []vprovider.VM          `json:"vms"`
	Hosts    []vprovider.Host        `json:"hosts"`
	Clusters []vprovider.Cluster     `json:"clusters"`
	Storage  []vprovider.StoragePool `json:"storage"`
	Networks []vprovider.Network     `json:"networks"`

	// Container domain (normalized workloads from the cache snapshots).
	Workloads []UnifiedWorkload `json:"workloads"`

	// Counts is a cheap summary for the dashboard header.
	Counts Counts `json:"counts"`

	// Degraded providers (failed a read); the rest of the data is still valid.
	Degraded []string `json:"degraded,omitempty"`

	GeneratedAt time.Time `json:"generatedAt"`
}

// UnifiedWorkload is a container-domain workload tagged with its source host id,
// so the unified view can show "which container on which host" next to VMs.
type UnifiedWorkload struct {
	HostID string `json:"hostId"`
	provider.Workload
}

// ContainerHostSnapshot is the per-host container inventory the aggregator needs.
// The api layer adapts cache.Snapshot into this shape so the inventory package
// stays decoupled from cache internals.
type ContainerHostSnapshot struct {
	HostID    string
	Workloads []provider.Workload // docker containers
	Swarm     []provider.Workload // swarm tasks
	Kube      []provider.Workload // k8s pods
	Degraded  bool
}

// Counts summarizes the unified inventory for the dashboard.
type Counts struct {
	VMs            int `json:"vms"`
	VMsRunning     int `json:"vmsRunning"`
	Hosts          int `json:"hosts"`
	Clusters       int `json:"clusters"`
	Containers     int `json:"containers"`
	ContainersUp   int `json:"containersUp"`
	HypervisorProviders int `json:"hypervisorProviders"`
	ContainerHosts int `json:"containerHosts"`
}

// VMProviders is the read surface the aggregator needs from the VM registry
// (satisfied by *vprovider.Registry, narrowed for testability).
type VMProviders interface {
	List() []vprovider.HypervisorProvider
}

// ContainerSnapshots is the read surface the aggregator needs from the container
// cache. The api layer implements it with a thin adapter over *cache.Manager.
type ContainerSnapshots interface {
	ContainerHostSnapshots() []ContainerHostSnapshot
}

// Aggregator merges the two domains. Safe for concurrent use.
type Aggregator struct {
	vms  VMProviders
	cont ContainerSnapshots

	// timeout bounds each per-provider VM read so a slow/hung hypervisor cannot
	// stall the whole unified read.
	timeout time.Duration
}

// New builds an Aggregator over the VM registry and container cache. Either may
// be nil (a deployment with only one domain still works).
func New(vms VMProviders, cont ContainerSnapshots) *Aggregator {
	return &Aggregator{vms: vms, cont: cont, timeout: 8 * time.Second}
}

// All returns the merged cross-domain inventory. VM reads run concurrently per
// provider and are bounded by the aggregator timeout; container reads come from
// the in-memory cache snapshots (already non-blocking). A provider that errors is
// recorded in Degraded and skipped, never failing the whole read.
func (a *Aggregator) All(ctx context.Context, nowUTC time.Time) Unified {
	u := Unified{GeneratedAt: nowUTC}

	// --- VM domain (concurrent per provider) ---
	if a.vms != nil {
		providers := a.vms.List()
		var mu sync.Mutex
		var wg sync.WaitGroup
		for _, p := range providers {
			wg.Add(1)
			go func(p vprovider.HypervisorProvider) {
				defer wg.Done()
				pctx, cancel := context.WithTimeout(ctx, a.timeout)
				defer cancel()
				vms, hosts, clusters, storage, nets, degraded := readVMProvider(pctx, p)
				mu.Lock()
				u.VMs = append(u.VMs, vms...)
				u.Hosts = append(u.Hosts, hosts...)
				u.Clusters = append(u.Clusters, clusters...)
				u.Storage = append(u.Storage, storage...)
				u.Networks = append(u.Networks, nets...)
				if degraded != "" {
					u.Degraded = append(u.Degraded, degraded)
				}
				mu.Unlock()
			}(p)
		}
		wg.Wait()
		u.Counts.HypervisorProviders = len(providers)
	}

	// --- container domain (from cache snapshots) ---
	if a.cont != nil {
		for _, snap := range a.cont.ContainerHostSnapshots() {
			u.Counts.ContainerHosts++
			for _, set := range [][]provider.Workload{snap.Workloads, snap.Swarm, snap.Kube} {
				for _, wl := range set {
					u.Workloads = append(u.Workloads, UnifiedWorkload{HostID: snap.HostID, Workload: wl})
				}
			}
			if snap.Degraded {
				u.Degraded = append(u.Degraded, "container-host:"+snap.HostID)
			}
		}
	}

	// --- counts + deterministic ordering ---
	u.Counts.VMs = len(u.VMs)
	for i := range u.VMs {
		if u.VMs[i].State == vprovider.StateRunning {
			u.Counts.VMsRunning++
		}
	}
	u.Counts.Hosts = len(u.Hosts)
	u.Counts.Clusters = len(u.Clusters)
	u.Counts.Containers = len(u.Workloads)
	for i := range u.Workloads {
		if u.Workloads[i].State == provider.StateRunning {
			u.Counts.ContainersUp++
		}
	}
	sortUnified(&u)
	return u
}

// readVMProvider reads one provider's inventory, gating each call on its declared
// capability. Returns a non-empty degraded id if any required read errored.
func readVMProvider(ctx context.Context, p vprovider.HypervisorProvider) (
	vms []vprovider.VM, hosts []vprovider.Host, clusters []vprovider.Cluster,
	storage []vprovider.StoragePool, nets []vprovider.Network, degraded string) {

	caps := p.Capabilities()
	bad := false
	if caps.Has(vprovider.CapListVMs) {
		if v, err := p.ListVMs(ctx, vprovider.ListOptions{}); err == nil {
			vms = v
		} else {
			bad = true
		}
	}
	if caps.Has(vprovider.CapListHosts) {
		if h, err := p.ListHosts(ctx); err == nil {
			hosts = h
		} else {
			bad = true
		}
	}
	if caps.Has(vprovider.CapListClusters) {
		if c, err := p.ListClusters(ctx); err == nil {
			clusters = c
		} else {
			bad = true
		}
	}
	if caps.Has(vprovider.CapListStorage) {
		if s, err := p.ListStorage(ctx); err == nil {
			storage = s
		} else {
			bad = true
		}
	}
	if caps.Has(vprovider.CapListNetworks) {
		if n, err := p.ListNetworks(ctx); err == nil {
			nets = n
		} else {
			bad = true
		}
	}
	if bad {
		degraded = "hypervisor:" + p.ID()
	}
	return
}

func sortUnified(u *Unified) {
	sort.Slice(u.VMs, func(i, j int) bool { return u.VMs[i].ID < u.VMs[j].ID })
	sort.Slice(u.Hosts, func(i, j int) bool { return u.Hosts[i].ID < u.Hosts[j].ID })
	sort.Slice(u.Clusters, func(i, j int) bool { return u.Clusters[i].ID < u.Clusters[j].ID })
	sort.Slice(u.Storage, func(i, j int) bool { return u.Storage[i].ID < u.Storage[j].ID })
	sort.Slice(u.Networks, func(i, j int) bool { return u.Networks[i].ID < u.Networks[j].ID })
	sort.Slice(u.Workloads, func(i, j int) bool {
		if u.Workloads[i].HostID != u.Workloads[j].HostID {
			return u.Workloads[i].HostID < u.Workloads[j].HostID
		}
		return u.Workloads[i].ID < u.Workloads[j].ID
	})
	sort.Strings(u.Degraded)
}
