// Package sim provides an in-memory HypervisorProvider used as the zero-hardware
// simulator for CI and as the reference implementation the conformance suite is
// first validated against. Each real provider (kvm/hyperv/xen/esxi) is validated
// against the SAME conformance suite using its own simulator (libvirt test://,
// vcsim, WMI mock, XAPI mock); this sim is the baseline that proves the suite and
// the contract are themselves coherent.
package sim

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// FullCaps is every capability bit set — the sim supports the entire contract so
// the conformance suite can exercise every method.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// Provider is an in-memory HypervisorProvider.
type Provider struct {
	id   string
	kind vp.HypervisorKind
	caps vp.CapabilityMatrix

	mu        sync.RWMutex
	hosts     map[string]vp.Host
	vms       map[string]*vp.VM
	clusters  map[string]vp.Cluster
	storage   map[string]vp.StoragePool
	networks  map[string]vp.Network
	snapshots map[string][]vp.Snapshot // vmID -> snapshots
	tracker   *vp.TaskTracker
	seq       int64
	closed    bool
}

// Option configures a sim Provider.
type Option func(*Provider)

// WithCaps overrides the capability matrix (default: FullCaps). Used to simulate
// read-only or partial hypervisors and confirm capability gating.
func WithCaps(c vp.CapabilityMatrix) Option { return func(p *Provider) { p.caps = c } }

// WithKind overrides the reported hypervisor kind (default: KindKVM).
func WithKind(k vp.HypervisorKind) Option { return func(p *Provider) { p.kind = k } }

// New returns a seeded in-memory provider (1 cluster, 2 hosts, 3 VMs, storage,
// networks) so inventory reads return non-trivial data out of the box.
func New(id string, opts ...Option) *Provider {
	p := &Provider{
		id:        id,
		kind:      vp.KindKVM,
		caps:      FullCaps,
		hosts:     map[string]vp.Host{},
		vms:       map[string]*vp.VM{},
		clusters:  map[string]vp.Cluster{},
		storage:   map[string]vp.StoragePool{},
		networks:  map[string]vp.Network{},
		snapshots: map[string][]vp.Snapshot{},
		tracker:   vp.NewTaskTracker(),
	}
	for _, o := range opts {
		o(p)
	}
	p.seed()
	return p
}

func (p *Provider) seed() {
	cl := vp.Cluster{ID: "cluster-1", Name: "sim-cluster", Kind: p.kind, ProviderID: p.id,
		HostIDs: []string{"host-1", "host-2"}, HAEnabled: true}
	p.clusters[cl.ID] = cl
	for _, h := range []vp.Host{
		{ID: "host-1", Name: "sim-host-1", Kind: p.kind, ProviderID: p.id, ClusterID: cl.ID,
			State: vp.NodeUp, CPUCores: 16, MemoryMB: 65536, VMCount: 2, Version: "sim-1.0"},
		{ID: "host-2", Name: "sim-host-2", Kind: p.kind, ProviderID: p.id, ClusterID: cl.ID,
			State: vp.NodeUp, CPUCores: 16, MemoryMB: 65536, VMCount: 1, Version: "sim-1.0"},
	} {
		p.hosts[h.ID] = h
	}
	p.storage["ds-1"] = vp.StoragePool{ID: "ds-1", Name: "sim-datastore", Kind: p.kind,
		ProviderID: p.id, Type: "nfs", CapacityGB: 4096, FreeGB: 3000, Accessible: true,
		HostIDs: []string{"host-1", "host-2"}}
	p.networks["net-1"] = vp.Network{ID: "net-1", Name: "sim-vmnet", Kind: p.kind,
		ProviderID: p.id, Type: "bridge", VLAN: 0}
	for i, st := range []vp.VMState{vp.StateRunning, vp.StateRunning, vp.StateStopped} {
		id := fmt.Sprintf("vm-%d", i+1)
		host := "host-1"
		if i == 2 {
			host = "host-2"
		}
		p.vms[id] = &vp.VM{
			ID: id, Name: fmt.Sprintf("sim-vm-%d", i+1), Kind: p.kind, ProviderID: p.id,
			HostID: host, ClusterID: cl.ID, State: st, StateRaw: string(st),
			VCPUs: 2, MemoryMB: 4096, GuestOS: "linux", Firmware: vp.FirmwareUEFI,
			Disks: []vp.Disk{{ID: id + "-d0", Format: vp.DiskQcow2, CapacityGB: 40, StorageID: "ds-1"}},
			NICs:  []vp.NIC{{ID: id + "-n0", MAC: "52:54:00:00:00:0" + fmt.Sprint(i), NetworkID: "net-1", Connected: true}},
			CreatedAt: time.Unix(1700000000, 0).UTC(),
		}
	}
}

func (p *Provider) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&p.seq, 1))
}

// --- identity / health ---

func (p *Provider) Kind() vp.HypervisorKind         { return p.kind }
func (p *Provider) ID() string                      { return p.id }
func (p *Provider) Capabilities() vp.CapabilityMatrix { return p.caps }

func (p *Provider) HealthCheck(ctx context.Context) (vp.HealthStatus, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.closed {
		return vp.HealthStatus{Healthy: false, Message: "closed", CheckedAt: time.Now().UTC()}, nil
	}
	return vp.HealthStatus{Healthy: true, Version: "sim-1.0", CheckedAt: time.Now().UTC()}, nil
}

func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	return nil
}

// --- inventory ---

func (p *Provider) ListHosts(ctx context.Context) ([]vp.Host, error) {
	if !p.caps.Has(vp.CapListHosts) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]vp.Host, 0, len(p.hosts))
	for _, h := range p.hosts {
		out = append(out, h)
	}
	return out, nil
}

func (p *Provider) ListVMs(ctx context.Context, opts vp.ListOptions) ([]vp.VM, error) {
	if !p.caps.Has(vp.CapListVMs) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]vp.VM, 0, len(p.vms))
	for _, v := range p.vms {
		if opts.HostID != "" && v.HostID != opts.HostID {
			continue
		}
		if opts.ClusterID != "" && v.ClusterID != opts.ClusterID {
			continue
		}
		if opts.State != "" && v.State != opts.State {
			continue
		}
		out = append(out, *v)
	}
	return out, nil
}

func (p *Provider) GetVM(ctx context.Context, id string) (*vp.VMDetail, error) {
	if !p.caps.Has(vp.CapGetVM) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	v, ok := p.vms[id]
	if !ok {
		return nil, vp.ErrNotFound
	}
	raw, _ := json.Marshal(map[string]any{"simNative": true, "id": id, "state": v.State})
	return &vp.VMDetail{VM: *v, Raw: raw}, nil
}

func (p *Provider) ListClusters(ctx context.Context) ([]vp.Cluster, error) {
	if !p.caps.Has(vp.CapListClusters) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]vp.Cluster, 0, len(p.clusters))
	for _, c := range p.clusters {
		out = append(out, c)
	}
	return out, nil
}

func (p *Provider) ListStorage(ctx context.Context) ([]vp.StoragePool, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]vp.StoragePool, 0, len(p.storage))
	for _, s := range p.storage {
		out = append(out, s)
	}
	return out, nil
}

func (p *Provider) ListNetworks(ctx context.Context) ([]vp.Network, error) {
	if !p.caps.Has(vp.CapListNetworks) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]vp.Network, 0, len(p.networks))
	for _, n := range p.networks {
		out = append(out, n)
	}
	return out, nil
}

// --- lifecycle ---

func (p *Provider) CreateVM(ctx context.Context, spec vp.VMSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapCreateVM) {
		return nil, vp.ErrUnsupported
	}
	if strings.TrimSpace(spec.Name) == "" || spec.VCPUs <= 0 || spec.MemoryMB <= 0 {
		return nil, vp.ErrInvalidSpec
	}
	now := time.Now().UTC()
	id := p.nextID("vm")
	v := &vp.VM{ID: id, Name: spec.Name, Kind: p.kind, ProviderID: p.id, HostID: spec.HostID,
		ClusterID: spec.ClusterID, State: vp.StateStopped, StateRaw: "stopped",
		VCPUs: spec.VCPUs, MemoryMB: spec.MemoryMB, GuestOS: spec.GuestOS,
		Firmware: spec.Firmware, Labels: spec.Labels, CreatedAt: now}
	for i, d := range spec.Disks {
		v.Disks = append(v.Disks, vp.Disk{ID: fmt.Sprintf("%s-d%d", id, i), Format: d.Format, CapacityGB: d.CapacityGB, StorageID: d.StorageID})
	}
	for i, n := range spec.NICs {
		v.NICs = append(v.NICs, vp.NIC{ID: fmt.Sprintf("%s-n%d", id, i), NetworkID: n.NetworkID, Model: n.Model, MAC: n.MAC, Connected: true})
	}
	p.mu.Lock()
	p.vms[id] = v
	p.mu.Unlock()
	t := p.tracker.Start(p.nextID("task"), "createVM", p.id, id, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) PowerOp(ctx context.Context, vmID string, op vp.PowerOp) (*vp.Task, error) {
	if !op.Valid() {
		return nil, vp.ErrInvalidSpec
	}
	if !p.caps.Has(vp.PowerOpCapability(op)) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	v, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	switch op {
	case vp.PowerStart, vp.PowerResume:
		v.State = vp.StateRunning
	case vp.PowerStop:
		v.State = vp.StateStopped
	case vp.PowerReset:
		v.State = vp.StateRunning
	case vp.PowerSuspend:
		v.State = vp.StateSuspended
	}
	v.StateRaw = string(v.State)
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "powerOp", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) DeleteVM(ctx context.Context, vmID string, opts vp.DeleteOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapDeleteVM) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	v, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	if v.Protected {
		p.mu.Unlock()
		return nil, vp.ErrConflict
	}
	if v.State == vp.StateRunning && !opts.Force {
		p.mu.Unlock()
		return nil, vp.ErrConflict
	}
	delete(p.vms, vmID)
	delete(p.snapshots, vmID)
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "deleteVM", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) ReconfigureVM(ctx context.Context, vmID string, spec vp.VMReconfigureSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapReconfigureVM) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	v, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	if spec.VCPUs != nil {
		v.VCPUs = *spec.VCPUs
	}
	if spec.MemoryMB != nil {
		v.MemoryMB = *spec.MemoryMB
	}
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "reconfigureVM", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

// --- snapshots & clones ---

func (p *Provider) Snapshot(ctx context.Context, vmID string, opts vp.SnapshotOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	v, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	snap := vp.Snapshot{ID: p.nextID("snap"), VMID: vmID, Name: opts.Name,
		Description: opts.Description, HasMemory: opts.Memory, IsCurrent: true, CreatedAt: time.Now().UTC()}
	for i := range p.snapshots[vmID] {
		p.snapshots[vmID][i].IsCurrent = false
	}
	p.snapshots[vmID] = append(p.snapshots[vmID], snap)
	v.SnapshotCount = len(p.snapshots[vmID])
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "snapshot", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) RevertSnapshot(ctx context.Context, vmID, snapID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapRevertSnapshot) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	snaps, ok := p.snapshots[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	found := false
	for i := range snaps {
		snaps[i].IsCurrent = snaps[i].ID == snapID
		if snaps[i].ID == snapID {
			found = true
		}
	}
	p.mu.Unlock()
	if !found {
		return nil, vp.ErrNotFound
	}
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "revertSnapshot", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) ListSnapshots(ctx context.Context, vmID string) ([]vp.Snapshot, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	if _, ok := p.vms[vmID]; !ok {
		return nil, vp.ErrNotFound
	}
	out := make([]vp.Snapshot, len(p.snapshots[vmID]))
	copy(out, p.snapshots[vmID])
	return out, nil
}

func (p *Provider) Clone(ctx context.Context, vmID string, spec vp.CloneSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapClone) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	src, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	id := p.nextID("vm")
	clone := *src
	clone.ID = id
	clone.Name = spec.Name
	clone.State = vp.StateStopped
	if spec.PowerOn {
		clone.State = vp.StateRunning
	}
	clone.StateRaw = string(clone.State)
	clone.SnapshotCount = 0
	p.vms[id] = &clone
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "clone", p.id, id, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

// --- migration ---

func (p *Provider) MigrateVM(ctx context.Context, vmID, targetHost string, opts vp.MigrateOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMigrate) {
		return nil, vp.ErrUnsupported
	}
	p.mu.Lock()
	v, ok := p.vms[vmID]
	if !ok {
		p.mu.Unlock()
		return nil, vp.ErrNotFound
	}
	if _, ok := p.hosts[targetHost]; !ok {
		p.mu.Unlock()
		return nil, vp.ErrInvalidSpec
	}
	v.HostID = targetHost
	p.mu.Unlock()
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), "migrate", p.id, vmID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now), nil
}

func (p *Provider) ExportVM(ctx context.Context, vmID string, format vp.DiskFormat) (io.ReadCloser, *vp.ExportInfo, error) {
	if !p.caps.Has(vp.CapExport) {
		return nil, nil, vp.ErrUnsupported
	}
	if !format.Valid() {
		return nil, nil, vp.ErrInvalidSpec
	}
	p.mu.RLock()
	v, ok := p.vms[vmID]
	p.mu.RUnlock()
	if !ok {
		return nil, nil, vp.ErrNotFound
	}
	// Emit a small deterministic byte stream standing in for a disk image.
	payload := []byte(fmt.Sprintf("SIMDISK:%s:%s:%s\n", p.id, vmID, format))
	info := &vp.ExportInfo{Format: format, SizeBytes: int64(len(payload)), DiskCount: len(v.Disks),
		SourceVMID: vmID, GuestOS: v.GuestOS, Firmware: v.Firmware}
	return io.NopCloser(strings.NewReader(string(payload))), info, nil
}

// --- cluster & HA ---

func (p *Provider) GetClusterTopology(ctx context.Context, clusterID string) (*vp.Topology, error) {
	if !p.caps.Has(vp.CapClusterTopology) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	cl, ok := p.clusters[clusterID]
	if !ok {
		return nil, vp.ErrNotFound
	}
	top := &vp.Topology{ClusterID: clusterID, Placement: map[string]string{}}
	for _, hid := range cl.HostIDs {
		h := p.hosts[hid]
		top.Nodes = append(top.Nodes, vp.NodeState{NodeID: hid, State: h.State, VMCount: h.VMCount, UpdatedAt: time.Now().UTC()})
	}
	for id, v := range p.vms {
		if v.ClusterID == clusterID {
			top.Placement[id] = v.HostID
		}
	}
	return top, nil
}

func (p *Provider) NodeState(ctx context.Context, nodeID string) (*vp.NodeState, error) {
	if !p.caps.Has(vp.CapNodeState) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	h, ok := p.hosts[nodeID]
	if !ok {
		return nil, vp.ErrNotFound
	}
	return &vp.NodeState{NodeID: nodeID, State: h.State, VMCount: h.VMCount, UpdatedAt: time.Now().UTC()}, nil
}

// --- observability ---

func (p *Provider) GetMetrics(ctx context.Context, entityID string, window vp.MetricWindow) (*vp.MetricSeries, error) {
	if !p.caps.Has(vp.CapMetrics) {
		return nil, vp.ErrUnsupported
	}
	p.mu.RLock()
	_, vok := p.vms[entityID]
	_, hok := p.hosts[entityID]
	p.mu.RUnlock()
	if !vok && !hok {
		return nil, vp.ErrNotFound
	}
	base := window.Since
	if base.IsZero() {
		base = time.Now().Add(-5 * time.Minute).UTC()
	}
	step := window.StepSecond
	if step <= 0 {
		step = 30
	}
	series := &vp.MetricSeries{EntityID: entityID}
	for i := 0; i < 5; i++ {
		series.Samples = append(series.Samples, vp.MetricSample{
			Timestamp: base.Add(time.Duration(i*step) * time.Second),
			CPUPercent: float64(10 + i*5), MemUsageBytes: uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes: 4 << 30, NetRxBytes: uint64(i) * 1000, NetTxBytes: uint64(i) * 800,
		})
	}
	return series, nil
}

func (p *Provider) StreamEvents(ctx context.Context) (<-chan vp.Event, error) {
	if !p.caps.Has(vp.CapEvents) {
		return nil, vp.ErrUnsupported
	}
	ch := make(chan vp.Event)
	go func() {
		defer close(ch)
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		for i := 0; ; i++ {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case ch <- vp.Event{Kind: vp.EventAlert, ProviderID: p.id,
					Message: fmt.Sprintf("sim heartbeat %d", i), Timestamp: time.Now().UTC()}:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	return ch, nil
}

// compile-time assertion: *Provider satisfies the contract.
var _ vp.HypervisorProvider = (*Provider)(nil)
