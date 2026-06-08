// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package hyperv

import (
	"fmt"
	"sync"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// simBackend is a pure-Go, in-memory Hyper-V/WMI fake — the moral equivalent of a
// recorded WMI fixture / mock of the root\virtualization\v2 namespace, faked in Go.
// It lets the Hyper-V provider be constructed and conformance-tested with
// CGO_ENABLED=0 on Linux/alpine and WITHOUT a Windows Hyper-V host (and without any
// WMI dependency in go.mod). It models a tiny but realistic Hyper-V inventory: one
// Failover Cluster, two cluster-node hosts, three VMs whose native WMI EnabledState
// ints exercise the mapping (2=Enabled/running, 2=Enabled/running, 3=Disabled/
// stopped), one Cluster Shared Volume (CSV) + one SMB share, one external virtual
// switch. VM ids use GUID-like Msvm_ComputerSystem.Name values. Concurrency-safe.
type simBackend struct {
	mu        sync.RWMutex
	closed    bool
	hosts     map[string]*hypervHost
	vms       map[string]*hypervVM
	clusters  map[string]*hypervCluster
	storage   []*hypervStorage
	switches  []*hypervSwitch
	snapshots map[string][]vp.Snapshot
	order     []string // stable VM id order for deterministic listing
}

const simVersion = "Microsoft Hyper-V Server 2022 (10.0.20348) / WMI root\\virtualization\\v2"

// newSimBackend returns a seeded in-memory Hyper-V model: 1 failover cluster,
// 2 nodes, 3 VMs (Enabled/Enabled/Disabled), 1 CSV + 1 SMB share, 1 external switch.
func newSimBackend() *simBackend {
	b := &simBackend{
		hosts:     map[string]*hypervHost{},
		vms:       map[string]*hypervVM{},
		clusters:  map[string]*hypervCluster{},
		snapshots: map[string][]vp.Snapshot{},
	}
	b.seed()
	return b
}

func (b *simBackend) seed() {
	const clusterID = "cluster-1"
	b.clusters[clusterID] = &hypervCluster{
		ClusterID: clusterID, Name: "HV-FAILOVER-CL",
		NodeIDs: []string{"node-1", "node-2"}, HAEnabled: true,
	}

	for _, h := range []*hypervHost{
		{HostID: "node-1", Name: "HV-NODE-01", ClusterID: clusterID,
			NodeState: "Up", CPUCores: 32, CPUMHz: 3200,
			MemoryBytes: 256 * (1 << 30), MemUsedMB: 64 * 1024, Version: simVersion},
		{HostID: "node-2", Name: "HV-NODE-02", ClusterID: clusterID,
			NodeState: "Up", CPUCores: 32, CPUMHz: 3200,
			MemoryBytes: 256 * (1 << 30), MemUsedMB: 48 * 1024, Version: simVersion},
	} {
		b.hosts[h.HostID] = h
	}

	b.storage = []*hypervStorage{
		{StorageID: "csv-1", Name: "ClusterStorage Volume1", Type: "csv",
			Path: "C:\\ClusterStorage\\Volume1", CapacityBytes: 4 * 1024 * bytesPerGB,
			FreeBytes: 3 * 1024 * bytesPerGB, HostIDs: []string{"node-1", "node-2"}, Accessible: true},
		{StorageID: "smb-1", Name: "SMB3 VM Store", Type: "smb",
			Path: "\\\\fileserver\\vmstore", CapacityBytes: 8 * 1024 * bytesPerGB,
			FreeBytes: 6 * 1024 * bytesPerGB, HostIDs: []string{"node-1", "node-2"}, Accessible: true},
	}
	b.switches = []*hypervSwitch{
		{SwitchID: "switch-1", Name: "External vSwitch", Type: "external", VLAN: 0},
	}

	// Native WMI EnabledState ints: 2 (Enabled/running), 2, 3 (Disabled/stopped).
	seeds := []struct {
		state enabledState
		host  string
		gen   int
	}{
		{enabledRunning, "node-1", 2},
		{enabledRunning, "node-1", 2},
		{enabledStopped, "node-2", 1},
	}
	for i, s := range seeds {
		// GUID-like Msvm_ComputerSystem.Name
		vmID := fmt.Sprintf("5FD3F32E-0000-4000-8000-00000000000%d", i+1)
		vm := &hypervVM{
			VMID:       vmID,
			Name:       fmt.Sprintf("hyperv-vm-%d", i+1),
			State:      s.state,
			HostID:     s.host,
			ClusterID:  clusterID,
			VCPUs:      4,
			MemoryMB:   8192,
			GuestOS:    "Windows Server 2022",
			Generation: s.gen,
			Disks: []hypervDisk{{
				Index: 0, Label: "Hard Drive 0",
				Path:      fmt.Sprintf("C:\\ClusterStorage\\Volume1\\%s\\%s.vhdx", vmID, vmID),
				StorageID: "csv-1", Format: vp.DiskVHDX, SizeBytes: 60 * bytesPerGB,
			}},
			NICs: []hypervNIC{{
				Index: 0, MAC: fmt.Sprintf("00:15:5D:00:00:0%d", i+1),
				SwitchID: "switch-1", Connected: true,
			}},
			GuestIPs: []string{fmt.Sprintf("10.0.0.2%d", i+1)},
			Created:  1700000000,
		}
		b.vms[vmID] = vm
		b.order = append(b.order, vmID)
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

func (b *simBackend) listHosts() []*hypervHost {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*hypervHost, 0, len(b.hosts))
	for _, h := range b.hosts {
		cp := *h
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getHost(hostID string) (*hypervHost, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	h, ok := b.hosts[hostID]
	if !ok {
		return nil, false
	}
	cp := *h
	return &cp, true
}

func (b *simBackend) listVMs() []*hypervVM {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*hypervVM, 0, len(b.vms))
	// stable order: seeded order first, then any newly created (appended) ids
	for _, id := range b.order {
		if vm, ok := b.vms[id]; ok {
			out = append(out, vm)
		}
	}
	return out
}

func (b *simBackend) getVM(vmID string) (*hypervVM, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	vm, ok := b.vms[vmID]
	return vm, ok
}

func (b *simBackend) listClusters() []*hypervCluster {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*hypervCluster, 0, len(b.clusters))
	for _, c := range b.clusters {
		cp := *c
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getCluster(clusterID string) (*hypervCluster, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	c, ok := b.clusters[clusterID]
	if !ok {
		return nil, false
	}
	cp := *c
	return &cp, true
}

func (b *simBackend) listStorage() []*hypervStorage {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*hypervStorage(nil), b.storage...)
}

func (b *simBackend) listSwitches() []*hypervSwitch {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*hypervSwitch(nil), b.switches...)
}

func (b *simBackend) createVM(vm *hypervVM) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.vms[vm.VMID]; !exists {
		b.order = append(b.order, vm.VMID)
	}
	b.vms[vm.VMID] = vm
}

func (b *simBackend) destroyVM(vmID string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.vms, vmID)
	delete(b.snapshots, vmID)
	for i, id := range b.order {
		if id == vmID {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
}

func (b *simBackend) setState(vmID string, s enabledState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if vm, ok := b.vms[vmID]; ok {
		vm.State = s
	}
}

func (b *simBackend) vmsOnHost(hostID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, vm := range b.vms {
		if vm.HostID == hostID {
			n++
		}
	}
	return n
}

func (b *simBackend) listSnapshots(vmID string) []vp.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.snapshots[vmID]
	out := make([]vp.Snapshot, len(src))
	copy(out, src)
	return out
}

func (b *simBackend) createSnapshot(vmID string, snap vp.Snapshot) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := range b.snapshots[vmID] {
		b.snapshots[vmID][i].IsCurrent = false
	}
	b.snapshots[vmID] = append(b.snapshots[vmID], snap)
}

func (b *simBackend) setCurrentSnapshot(vmID, snapID string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	found := false
	for i := range b.snapshots[vmID] {
		isCur := b.snapshots[vmID][i].ID == snapID
		b.snapshots[vmID][i].IsCurrent = isCur
		if isCur {
			found = true
		}
	}
	return found
}

// compile-time assertion: *simBackend satisfies wmiBackend.
var _ wmiBackend = (*simBackend)(nil)
