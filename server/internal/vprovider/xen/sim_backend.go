// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package xen

import (
	"fmt"
	"sync"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// simBackend is a pure-Go, in-memory XAPI model — the moral equivalent of an XAPI
// mock server, faked in Go. It lets the Xen provider be constructed and
// conformance-tested with CGO_ENABLED=0 and WITHOUT a real XenServer/XCP-ng host
// (and with no XAPI SDK in go.mod). It models a tiny subset of XAPI: a pool
// grouping two hosts, three VMs carrying XAPI power_state tokens
// (Running/Running/Halted, exercising the state mapping), one SR and one network,
// all addressed by opaque refs. It is concurrency-safe.
type simBackend struct {
	mu        sync.RWMutex
	closed    bool
	pl        *xapiPool
	hosts     map[string]*xapiHost
	vms       map[string]*xapiVM
	srs       []*xapiSR
	nets      []*xapiNetwork
	snapshots map[string][]vp.Snapshot
	order     []string // stable VM ref order for deterministic listing
}

// newSimBackend returns a seeded in-memory XAPI model: 1 pool (=cluster), 2 hosts,
// 3 VMs (Running/Running/Halted — exercising power_state normalization), 1 SR,
// 1 network. The seed mirrors a realistic small XCP-ng pool.
func newSimBackend() *simBackend {
	b := &simBackend{
		hosts:     map[string]*xapiHost{},
		vms:       map[string]*xapiVM{},
		snapshots: map[string][]vp.Snapshot{},
	}
	b.seed()
	return b
}

func (b *simBackend) seed() {
	const (
		host1 = "OpaqueRef:host-1"
		host2 = "OpaqueRef:host-2"
		srRef = "OpaqueRef:sr-1"
		netRf = "OpaqueRef:net-1"
	)
	for _, h := range []*xapiHost{
		{Ref: host1, UUID: "uuid-host-1", NameLabel: "xcp-host-1", Enabled: true, Live: true,
			CPUCount: 24, CPUMHz: 2900, MemoryTotB: 128 * bytesPerGB, MemoryFreB: 96 * bytesPerGB,
			Version: "XCP-ng 8.3 (xapi 24.x)"},
		{Ref: host2, UUID: "uuid-host-2", NameLabel: "xcp-host-2", Enabled: true, Live: true,
			CPUCount: 24, CPUMHz: 2900, MemoryTotB: 128 * bytesPerGB, MemoryFreB: 112 * bytesPerGB,
			Version: "XCP-ng 8.3 (xapi 24.x)"},
	} {
		b.hosts[h.Ref] = h
	}

	b.pl = &xapiPool{
		Ref: "OpaqueRef:pool-1", UUID: "uuid-pool-1", NameLabel: "xcp-pool",
		MasterRef: host1, HostRefs: []string{host1, host2}, HAEnabled: true,
	}

	b.srs = []*xapiSR{{
		Ref: srRef, UUID: "uuid-sr-1", NameLabel: "Local-NFS", Type: "nfs",
		PhysSizeB: 4096 * bytesPerGB, PhysUtilB: 1096 * bytesPerGB,
		HostRefs: []string{host1, host2}, Shared: true, Accessible: true,
	}}
	b.nets = []*xapiNetwork{{
		Ref: netRf, UUID: "uuid-net-1", NameLabel: "Pool-wide network associated with eth0",
		Bridge: "xenbr0", VLAN: 0,
	}}

	seeds := []struct {
		state xapiPowerState
		host  string
	}{
		{psRunning, host1},
		{psRunning, host1},
		{psHalted, host2},
	}
	for i, s := range seeds {
		ref := fmt.Sprintf("OpaqueRef:vm-seed-%d", i+1)
		v := &xapiVM{
			Ref:        ref,
			UUID:       fmt.Sprintf("uuid-vm-%d", i+1),
			NameLabel:  fmt.Sprintf("xen-vm-%d", i+1),
			PowerState: s.state,
			ResidentOn: s.host,
			VCPUsMax:   2,
			MemoryB:    4 * bytesPerGB,
			OSDistro:   "Debian GNU/Linux 12",
			HVM:        true,
			UEFI:       true,
			VBDs: []xapiVBD{{
				Ref: ref + "-vbd0", Device: "0", VDIRef: ref + "-vdi0", SRRef: srRef,
				VirtualB: 40 * bytesPerGB, Path: fmt.Sprintf("/var/run/sr-mount/%s.vhd", ref),
			}},
			VIFs: []xapiVIF{{
				Ref: ref + "-vif0", MAC: fmt.Sprintf("aa:bb:cc:00:00:0%d", i+1),
				NetworkRef: netRf, Model: "netfront", Attached: true,
			}},
			IPs:     []string{fmt.Sprintf("10.0.0.1%d", i+1)},
			Created: 1700000000,
		}
		b.vms[ref] = v
		b.order = append(b.order, ref)
	}
}

func (b *simBackend) version() string { return "XCP-ng 8.3 (xapi 24.x)" }

func (b *simBackend) healthy() bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return !b.closed
}

func (b *simBackend) close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	return nil
}

func (b *simBackend) listHosts() []*xapiHost {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*xapiHost, 0, len(b.hosts))
	for _, h := range b.hosts {
		cp := *h
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getHost(ref string) (*xapiHost, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h, ok := b.hosts[ref]
	if !ok {
		return nil, false
	}
	cp := *h
	return &cp, true
}

func (b *simBackend) listVMs() []*xapiVM {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*xapiVM, 0, len(b.vms))
	// stable order: seeded order first, then any newly created (appended) refs
	for _, ref := range b.order {
		if v, ok := b.vms[ref]; ok {
			out = append(out, v)
		}
	}
	return out
}

func (b *simBackend) getVM(ref string) (*xapiVM, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	v, ok := b.vms[ref]
	return v, ok
}

func (b *simBackend) listSRs() []*xapiSR {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*xapiSR(nil), b.srs...)
}

func (b *simBackend) listNetworks() []*xapiNetwork {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*xapiNetwork(nil), b.nets...)
}

func (b *simBackend) createVM(v *xapiVM) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.vms[v.Ref]; !exists {
		b.order = append(b.order, v.Ref)
	}
	b.vms[v.Ref] = v
}

func (b *simBackend) destroyVM(ref string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.vms, ref)
	delete(b.snapshots, ref)
	for i, id := range b.order {
		if id == ref {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}

func (b *simBackend) setPowerState(ref string, s xapiPowerState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if v, ok := b.vms[ref]; ok {
		v.PowerState = s
	}
}

func (b *simBackend) vmsOnHost(hostRef string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, v := range b.vms {
		if v.ResidentOn == hostRef {
			n++
		}
	}
	return n
}

func (b *simBackend) listSnapshots(ref string) []vp.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.snapshots[ref]
	out := make([]vp.Snapshot, len(src))
	copy(out, src)
	return out
}

func (b *simBackend) createSnapshot(ref string, snap vp.Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.snapshots[ref] {
		b.snapshots[ref][i].IsCurrent = false
	}
	b.snapshots[ref] = append(b.snapshots[ref], snap)
}

func (b *simBackend) setCurrentSnapshot(ref, snapID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	found := false
	for i := range b.snapshots[ref] {
		isCur := b.snapshots[ref][i].ID == snapID
		b.snapshots[ref][i].IsCurrent = isCur
		if isCur {
			found = true
		}
	}
	return found
}

func (b *simBackend) pool() *xapiPool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.pl == nil {
		return nil
	}
	cp := *b.pl
	cp.HostRefs = append([]string(nil), b.pl.HostRefs...)
	return &cp
}

// compile-time assertion: *simBackend satisfies xapiBackend.
var _ xapiBackend = (*simBackend)(nil)
