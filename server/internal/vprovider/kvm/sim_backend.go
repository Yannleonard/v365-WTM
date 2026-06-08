// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package kvm

import (
	"fmt"
	"sync"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// simBackend is a pure-Go, in-memory libvirt model — the moral equivalent of the
// libvirt `test://` driver, faked in Go. It lets the KVM provider be constructed
// and conformance-tested with CGO_ENABLED=0 and WITHOUT libvirtd. It models a tiny
// subset of libvirt: a handful of domains carrying libvirt-style state ints, two
// nodes/hosts, a storage pool and a virtual network. It is concurrency-safe.
type simBackend struct {
	mu        sync.RWMutex
	closed    bool
	nodes     map[string]*libvirtNode
	domains   map[string]*libvirtDomain
	pools     []*libvirtPool
	nets      []*libvirtNet
	snapshots map[string][]vp.Snapshot
	order     []string // stable domain UUID order for deterministic listing
}

// newSimBackend returns a seeded in-memory libvirt model: 2 hosts, 3 domains
// (running, running, shutoff — exercising the state mapping), 1 storage pool,
// 1 network. The seed mirrors a realistic libvirt host pair.
func newSimBackend() *simBackend {
	b := &simBackend{
		nodes:     map[string]*libvirtNode{},
		domains:   map[string]*libvirtDomain{},
		snapshots: map[string][]vp.Snapshot{},
	}
	b.seed()
	return b
}

func (b *simBackend) seed() {
	for _, n := range []*libvirtNode{
		{ID: "node-1", Name: "kvm-node-1", Online: true, CPUs: 32, MHz: 3400,
			MemoryKB: 128 * 1024 * 1024, UsedKB: 32 * 1024 * 1024, Version: "QEMU-8.2.0/libvirt-10.0.0"},
		{ID: "node-2", Name: "kvm-node-2", Online: true, CPUs: 32, MHz: 3400,
			MemoryKB: 128 * 1024 * 1024, UsedKB: 16 * 1024 * 1024, Version: "QEMU-8.2.0/libvirt-10.0.0"},
	} {
		b.nodes[n.ID] = n
	}

	b.pools = []*libvirtPool{{
		UUID: "pool-default", Name: "default", Type: "dir",
		CapBytes: 4 * 1024 * bytesPerGB, AvailBytes: 3 * 1024 * bytesPerGB,
		Active: true, Hosts: []string{"node-1", "node-2"},
	}}
	b.nets = []*libvirtNet{{
		UUID: "net-default", Name: "default", Mode: "nat", VLAN: 0,
	}}

	// libvirt-native states: 1=running, 1=running, 5=shutoff.
	seeds := []struct {
		state libvirtState
		host  string
	}{
		{domRunning, "node-1"},
		{domRunning, "node-1"},
		{domShutoff, "node-2"},
	}
	for i, s := range seeds {
		uuid := fmt.Sprintf("dom-seed-%d", i+1)
		d := &libvirtDomain{
			UUID:     uuid,
			Name:     fmt.Sprintf("kvm-vm-%d", i+1),
			State:    s.state,
			HostID:   s.host,
			VCPUs:    2,
			MemoryKB: 4 * 1024 * 1024, // 4 GiB in KiB
			OSType:   "linux",
			Firmware: vp.FirmwareUEFI,
			Disks: []libvirtDisk{{
				Target: "vda", Driver: "qcow2", Source: fmt.Sprintf("/var/lib/libvirt/images/%s.qcow2", uuid),
				Pool: "pool-default", CapBytes: 40 * bytesPerGB,
			}},
			NICs: []libvirtNIC{{
				MAC: fmt.Sprintf("52:54:00:00:00:0%d", i+1), Network: "net-default", Model: "virtio", Link: true,
			}},
			IPs:     []string{fmt.Sprintf("192.168.122.1%d", i+1)},
			Created: 1700000000,
		}
		b.domains[uuid] = d
		b.order = append(b.order, uuid)
	}
}

func (b *simBackend) version() string { return "QEMU-8.2.0/libvirt-10.0.0" }

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

func (b *simBackend) listNodes() []*libvirtNode {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*libvirtNode, 0, len(b.nodes))
	for _, n := range b.nodes {
		cp := *n
		out = append(out, &cp)
	}
	return out
}

func (b *simBackend) getNode(id string) (*libvirtNode, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n, ok := b.nodes[id]
	if !ok {
		return nil, false
	}
	cp := *n
	return &cp, true
}

func (b *simBackend) listDomains() []*libvirtDomain {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]*libvirtDomain, 0, len(b.domains))
	// stable order: seeded order first, then any newly defined (appended) UUIDs
	for _, uuid := range b.order {
		if d, ok := b.domains[uuid]; ok {
			out = append(out, d)
		}
	}
	return out
}

func (b *simBackend) getDomain(uuid string) (*libvirtDomain, bool) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	d, ok := b.domains[uuid]
	return d, ok
}

func (b *simBackend) listPools() []*libvirtPool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*libvirtPool(nil), b.pools...)
}

func (b *simBackend) listNets() []*libvirtNet {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return append([]*libvirtNet(nil), b.nets...)
}

func (b *simBackend) defineDomain(d *libvirtDomain) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.domains[d.UUID]; !exists {
		b.order = append(b.order, d.UUID)
	}
	b.domains[d.UUID] = d
	return nil
}

func (b *simBackend) undefineDomain(uuid string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.domains, uuid)
	delete(b.snapshots, uuid)
	for i, id := range b.order {
		if id == uuid {
			b.order = append(b.order[:i], b.order[i+1:]...)
			break
		}
	}
	return nil
}

func (b *simBackend) setDomainState(uuid string, s libvirtState) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if d, ok := b.domains[uuid]; ok {
		d.State = s
	}
	return nil
}

// reconfigureDomain mutates the in-memory model's vCPU / memory (the sim has no
// max-vcpu limit, so any positive value is accepted — the conformance suite only
// exercises valid specs).
func (b *simBackend) reconfigureDomain(uuid string, vcpus *int, memMB *int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.domains[uuid]
	if !ok {
		return vp.ErrNotFound
	}
	if vcpus != nil {
		d.VCPUs = *vcpus
	}
	if memMB != nil {
		d.MemoryKB = *memMB * 1024
	}
	return nil
}

// markTemplate toggles the in-memory domain's template flag + label (Lot 4A).
func (b *simBackend) markTemplate(uuid string, isTemplate bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	d, ok := b.domains[uuid]
	if !ok {
		return vp.ErrNotFound
	}
	d.IsTemplate = isTemplate
	if isTemplate {
		if d.Labels == nil {
			d.Labels = map[string]string{}
		}
		d.Labels[labelTemplate] = "true"
	} else {
		delete(d.Labels, labelTemplate)
	}
	return nil
}

func (b *simBackend) domainsOnHost(hostID string) int {
	b.mu.RLock()
	defer b.mu.RUnlock()
	n := 0
	for _, d := range b.domains {
		if d.HostID == hostID {
			n++
		}
	}
	return n
}

func (b *simBackend) listSnapshots(uuid string) []vp.Snapshot {
	b.mu.RLock()
	defer b.mu.RUnlock()
	src := b.snapshots[uuid]
	out := make([]vp.Snapshot, len(src))
	copy(out, src)
	return out
}

func (b *simBackend) createSnapshot(uuid string, snap vp.Snapshot, opts vp.SnapshotOptions) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	// Model a tree: the previous current snapshot becomes this one's parent.
	for i := range b.snapshots[uuid] {
		if b.snapshots[uuid][i].IsCurrent {
			snap.ParentID = b.snapshots[uuid][i].ID
		}
		b.snapshots[uuid][i].IsCurrent = false
	}
	snap.HasMemory = opts.Memory
	b.snapshots[uuid] = append(b.snapshots[uuid], snap)
	return nil
}

func (b *simBackend) setCurrentSnapshot(uuid, snapID string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	found := false
	for i := range b.snapshots[uuid] {
		isCur := b.snapshots[uuid][i].ID == snapID
		b.snapshots[uuid][i].IsCurrent = isCur
		if isCur {
			found = true
		}
	}
	return found, nil
}

func (b *simBackend) hostIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]string, 0, len(b.nodes))
	for id := range b.nodes {
		out = append(out, id)
	}
	return out
}

func (b *simBackend) clusterName() string { return "kvm-lab" }

// compile-time assertion: *simBackend satisfies libvirtBackend.
var _ libvirtBackend = (*simBackend)(nil)
