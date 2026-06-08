// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package esxi

import (
	"fmt"
	"sync"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// simBackend is a pure-Go, in-memory vSphere model — the moral equivalent of
// VMware's vcsim, faked in Go. It lets the ESXi provider be constructed and
// conformance-tested with CGO_ENABLED=0 and WITHOUT a vCenter/ESXi (and without
// govmomi in go.mod). It models a tiny but realistic vSphere inventory: one
// ClusterComputeResource with HA+DRS, two HostSystem hosts, three VirtualMachines
// (poweredOn/poweredOn/poweredOff — exercising the power-state mapping), one
// Datastore (VMFS), one port-group Network. Managed-object refs use vSphere-style
// ids ("vm-123", "host-11", "domain-c7", "datastore-1", "network-1"). It is
// concurrency-safe.
type simBackend struct {
	mu         sync.RWMutex
	closed     bool
	hosts      map[string]*vsphereHost
	vms        map[string]*vsphereVM
	clusters   map[string]*vsphereCluster
	datastores []*vsphereDatastore
	networks   []*vsphereNetwork
	snapshots  map[string][]vp.Snapshot
	order      []string // stable VM moRef order for deterministic listing
}

const simVersion = "VMware vCenter Server 8.0.2 / ESXi 8.0.2"

// newSimBackend returns a seeded in-memory vSphere model: 1 cluster (HA+DRS),
// 2 hosts, 3 VMs (poweredOn/poweredOn/poweredOff), 1 VMFS datastore, 1 port group.
func newSimBackend() *simBackend {
	b := &simBackend{
		hosts:     map[string]*vsphereHost{},
		vms:       map[string]*vsphereVM{},
		clusters:  map[string]*vsphereCluster{},
		snapshots: map[string][]vp.Snapshot{},
	}
	b.seed()
	return b
}

func (b *simBackend) seed() {
	const clusterID = "domain-c7"
	b.clusters[clusterID] = &vsphereCluster{
		MoRef: clusterID, Name: "vsphere-cluster", HostIDs: []string{"host-11", "host-12"},
		HA: true, DRS: true,
	}

	for _, h := range []*vsphereHost{
		{MoRef: "host-11", Name: "esxi-11.lab.local", ClusterID: clusterID,
			ConnectionState: "connected", NumCPUCores: 24, CPUMHz: 2900,
			MemoryBytes: 256 * (1 << 30), MemUsedMB: 64 * 1024, Version: simVersion},
		{MoRef: "host-12", Name: "esxi-12.lab.local", ClusterID: clusterID,
			ConnectionState: "connected", NumCPUCores: 24, CPUMHz: 2900,
			MemoryBytes: 256 * (1 << 30), MemUsedMB: 32 * 1024, Version: simVersion},
	} {
		b.hosts[h.MoRef] = h
	}

	b.datastores = []*vsphereDatastore{{
		MoRef: "datastore-1", Name: "datastore1", Type: "VMFS",
		CapacityBytes: 4 * 1024 * bytesPerGB, FreeBytes: 3 * 1024 * bytesPerGB,
		HostIDs: []string{"host-11", "host-12"}, Accessible: true,
	}}
	b.networks = []*vsphereNetwork{{
		MoRef: "network-1", Name: "VM Network", Type: "portgroup", VLAN: 0,
	}}

	// Native power states: poweredOn, poweredOn, poweredOff.
	seeds := []struct {
		power powerState
		host  string
	}{
		{powerOn, "host-11"},
		{powerOn, "host-11"},
		{powerOff, "host-12"},
	}
	for i, s := range seeds {
		moRef := fmt.Sprintf("vm-%d", 100+i+1)
		vm := &vsphereVM{
			MoRef:     moRef,
			Name:      fmt.Sprintf("vsphere-vm-%d", i+1),
			Power:     s.power,
			HostRef:   s.host,
			ClusterID: clusterID,
			NumCPU:    4,
			MemoryMB:  8192,
			GuestID:   "ubuntu64Guest",
			Firmware:  vp.FirmwareUEFI,
			Disks: []vsphereDisk{{
				Key: 0, Label: "Hard disk 1",
				VMDKPath:    fmt.Sprintf("[datastore1] %s/%s.vmdk", moRef, moRef),
				DatastoreID: "datastore-1", CapacityKB: 60 * bytesPerGB / 1024,
			}},
			NICs: []vsphereNIC{{
				Key: 0, MAC: fmt.Sprintf("00:50:56:9a:00:0%d", i+1),
				PortgroupID: "network-1", AdapterType: "vmxnet3", Connected: true,
			}},
			GuestIPs: []string{fmt.Sprintf("10.0.0.1%d", i+1)},
			Created:  1700000000,
		}
		b.vms[moRef] = vm
		b.order = append(b.order, moRef)
	}
}

func (b *simBackend) version() string { return simVersion }

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

func (b *simBackend) listHosts() []*vsphereHost {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*vsphereHost, 0, len(b.hosts))
	for _, h := range b.hosts {
		cp := *h
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getHost(moRef string) (*vsphereHost, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h, ok := b.hosts[moRef]
	if !ok {
		return nil, false
	}
	cp := *h
	return &cp, true
}

func (b *simBackend) listVMs() []*vsphereVM {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*vsphereVM, 0, len(b.vms))
	// stable order: seeded order first, then any newly created (appended) refs
	for _, moRef := range b.order {
		if vm, ok := b.vms[moRef]; ok {
			out = append(out, vm)
		}
	}
	return out
}

func (b *simBackend) getVM(moRef string) (*vsphereVM, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	vm, ok := b.vms[moRef]
	return vm, ok
}

func (b *simBackend) listClusters() []*vsphereCluster {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*vsphereCluster, 0, len(b.clusters))
	for _, c := range b.clusters {
		cp := *c
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getCluster(moRef string) (*vsphereCluster, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.clusters[moRef]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

func (b *simBackend) listDatastores() []*vsphereDatastore {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*vsphereDatastore(nil), b.datastores...)
}

func (b *simBackend) listNetworks() []*vsphereNetwork {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*vsphereNetwork(nil), b.networks...)
}

func (b *simBackend) createVM(vm *vsphereVM) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.vms[vm.MoRef]; !exists {
		b.order = append(b.order, vm.MoRef)
	}
	b.vms[vm.MoRef] = vm
}

func (b *simBackend) destroyVM(moRef string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.vms, moRef)
	delete(b.snapshots, moRef)
	for i, id := range b.order {
		if id == moRef {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}

func (b *simBackend) setPower(moRef string, s powerState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if vm, ok := b.vms[moRef]; ok {
		vm.Power = s
	}
}

func (b *simBackend) vmsOnHost(hostRef string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, vm := range b.vms {
		if vm.HostRef == hostRef {
			n++
		}
	}
	return n
}

func (b *simBackend) listSnapshots(moRef string) []vp.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.snapshots[moRef]
	out := make([]vp.Snapshot, len(src))
	copy(out, src)
	return out
}

func (b *simBackend) createSnapshot(moRef string, snap vp.Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.snapshots[moRef] {
		b.snapshots[moRef][i].IsCurrent = false
	}
	b.snapshots[moRef] = append(b.snapshots[moRef], snap)
}

func (b *simBackend) setCurrentSnapshot(moRef, snapID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	found := false
	for i := range b.snapshots[moRef] {
		isCur := b.snapshots[moRef][i].ID == snapID
		b.snapshots[moRef][i].IsCurrent = isCur
		if isCur {
			found = true
		}
	}
	return found
}

// compile-time assertion: *simBackend satisfies vsphereBackend.
var _ vsphereBackend = (*simBackend)(nil)
