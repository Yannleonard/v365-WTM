// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package hyperv

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// FullCaps is the realistic Hyper-V capability set. Hyper-V (with Failover
// Clustering) supports the entire contract: inventory reads (hosts/VMs/clusters/
// CSV+SMB storage/virtual switches), GetVM, full lifecycle (New-VM / Start-VM /
// Stop-VM / Restart-VM / Suspend(Save)-VM / Remove-VM / Set-VM), checkpoints +
// revert (Checkpoint-VM / Restore-VMSnapshot), clone (Export-VM + Import-VM),
// migration (Move-VM / Live Migration), disk export for V2V (Export-VM / VHDX),
// cluster topology (Failover Cluster), per-node state (MSCluster_Node), resource
// metering metrics (Measure-VM) and the WMI event stream (Msvm_* __InstanceModification
// events). All bits are therefore set.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// wmiBackend is the seam between the pure-Go normalization core and a concrete
// Hyper-V transport. The default build wires it to an in-memory WMI fake
// (simBackend, sim_backend.go) using WMI-style native structs — this runs CGO-free
// on Linux/alpine in CI. A real Windows-only WMI/PowerShell backend can be wired
// behind //go:build windows (live_windows.go) without touching this core and without
// adding any dependency to go.mod.
//
// Methods operate in Hyper-V/WMI-native terms (Msvm_* objects, EnabledState ints,
// VHDX paths, byte sizes); the Provider does all contract normalization and
// capability/error mapping.
type wmiBackend interface {
	// connection
	version() string
	healthy() bool
	close() error
	// isLive reports whether this is the REAL WMI transport (true) or the in-memory
	// sim/test fake (false). Used to gate operations — like ExportVM — that must
	// HARD-ERROR on a live host rather than fabricate a placeholder artifact.
	isLive() bool

	// inventory (WMI-native)
	listHosts() []*hypervHost
	getHost(hostID string) (*hypervHost, bool)
	listVMs() []*hypervVM
	getVM(vmID string) (*hypervVM, bool)
	listClusters() []*hypervCluster
	getCluster(clusterID string) (*hypervCluster, bool)
	listStorage() []*hypervStorage
	listSwitches() []*hypervSwitch

	// lifecycle
	createVM(vm *hypervVM) // New-VM / register
	destroyVM(vmID string) // Remove-VM
	setState(vmID string, s enabledState)
	vmsOnHost(hostID string) int

	// checkpoints (Hyper-V snapshots)
	listSnapshots(vmID string) []vp.Snapshot
	createSnapshot(vmID string, snap vp.Snapshot)
	setCurrentSnapshot(vmID, snapID string) bool
}

// Provider is the Microsoft Hyper-V HypervisorProvider. The core is CGO-free; the
// Hyper-V/WMI-specific bits live behind the wmiBackend seam.
type Provider struct {
	id   string
	kind vp.HypervisorKind
	caps vp.CapabilityMatrix

	backend wmiBackend
	tracker *vp.TaskTracker

	mu     sync.Mutex
	seq    int64
	closed bool
}

// Option configures a Provider.
type Option func(*Provider)

// WithCaps overrides the capability matrix (default: FullCaps). Used by tests to
// confirm capability gating returns ErrUnsupported for undeclared operations.
func WithCaps(c vp.CapabilityMatrix) Option { return func(p *Provider) { p.caps = c } }

// WithBackend injects a wmiBackend (default in the default build: a seeded in-memory
// WMI fake). Live Windows transport is injected via the windows build.
func WithBackend(b wmiBackend) Option { return func(p *Provider) { p.backend = b } }

// New constructs a Hyper-V provider. With no WithBackend option it uses the seeded
// in-memory WMI fake so it can be constructed in tests without a Hyper-V host (and
// CGO-free on Linux).
func New(id string, opts ...Option) *Provider {
	p := &Provider{
		id:      id,
		kind:    vp.KindHyperV,
		caps:    FullCaps,
		tracker: vp.NewTaskTracker(),
	}
	for _, o := range opts {
		o(p)
	}
	if p.backend == nil {
		p.backend = newSimBackend()
	}
	return p
}

func (p *Provider) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&p.seq, 1))
}

// finishTask records a synchronous-but-async-shaped successful task.
func (p *Provider) finishTask(kind, entityID string) *vp.Task {
	now := time.Now().UTC()
	t := p.tracker.Start(p.nextID("task"), kind, p.id, entityID, now)
	return p.tracker.Finish(t.ID, vp.TaskSucceeded, "", now)
}

// --- identity / health ---

func (p *Provider) Kind() vp.HypervisorKind           { return p.kind }
func (p *Provider) ID() string                        { return p.id }
func (p *Provider) Capabilities() vp.CapabilityMatrix { return p.caps }

func (p *Provider) HealthCheck(ctx context.Context) (vp.HealthStatus, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || !p.backend.healthy() {
		return vp.HealthStatus{Healthy: false, Message: "Hyper-V WMI connection unavailable",
			CheckedAt: time.Now().UTC()}, nil
	}
	return vp.HealthStatus{Healthy: true, Version: p.backend.version(),
		CheckedAt: time.Now().UTC()}, nil
}

func (p *Provider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return nil
	}
	p.closed = true
	return p.backend.close()
}

// --- inventory ---

func (p *Provider) ListHosts(ctx context.Context) ([]vp.Host, error) {
	if !p.caps.Has(vp.CapListHosts) {
		return nil, vp.ErrUnsupported
	}
	hosts := p.backend.listHosts()
	out := make([]vp.Host, 0, len(hosts))
	for _, h := range hosts {
		out = append(out, p.normalizeHost(h))
	}
	return out, nil
}

func (p *Provider) ListVMs(ctx context.Context, opts vp.ListOptions) ([]vp.VM, error) {
	if !p.caps.Has(vp.CapListVMs) {
		return nil, vp.ErrUnsupported
	}
	vms := p.backend.listVMs()
	out := make([]vp.VM, 0, len(vms))
	for _, vm := range vms {
		v := p.normalizeVM(vm)
		if opts.HostID != "" && v.HostID != opts.HostID {
			continue
		}
		if opts.ClusterID != "" && v.ClusterID != opts.ClusterID {
			continue
		}
		if opts.State != "" && v.State != opts.State {
			continue
		}
		if !labelsMatch(v.Labels, opts.Labels) {
			continue
		}
		out = append(out, v)
	}
	return out, nil
}

func (p *Provider) GetVM(ctx context.Context, id string) (*vp.VMDetail, error) {
	if !p.caps.Has(vp.CapGetVM) {
		return nil, vp.ErrUnsupported
	}
	vm, ok := p.backend.getVM(id)
	if !ok {
		return nil, vp.ErrNotFound
	}
	v := p.normalizeVM(vm)
	// Raw mirrors what a Hyper-V WMI inspect would surface (Msvm_ComputerSystem +
	// associated setting data).
	raw, _ := json.Marshal(map[string]any{
		"__class":      "Msvm_ComputerSystem",
		"Name":         vm.VMID,
		"ElementName":  vm.Name,
		"EnabledState": int(vm.State),
		"VirtualQuantityCPU": vm.VCPUs,
		"MemoryMB":     vm.MemoryMB,
		"Generation":   vm.Generation,
		"GuestOS":      vm.GuestOS,
		"HostName":     vm.HostID,
		"ClusterName":  vm.ClusterID,
	})
	return &vp.VMDetail{VM: v, Raw: raw}, nil
}

func (p *Provider) ListClusters(ctx context.Context) ([]vp.Cluster, error) {
	if !p.caps.Has(vp.CapListClusters) {
		return nil, vp.ErrUnsupported
	}
	cls := p.backend.listClusters()
	out := make([]vp.Cluster, 0, len(cls))
	for _, c := range cls {
		out = append(out, p.normalizeCluster(c))
	}
	return out, nil
}

func (p *Provider) ListStorage(ctx context.Context) ([]vp.StoragePool, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	sts := p.backend.listStorage()
	out := make([]vp.StoragePool, 0, len(sts))
	for _, s := range sts {
		out = append(out, p.normalizeStorage(s))
	}
	return out, nil
}

func (p *Provider) ListNetworks(ctx context.Context) ([]vp.Network, error) {
	if !p.caps.Has(vp.CapListNetworks) {
		return nil, vp.ErrUnsupported
	}
	sws := p.backend.listSwitches()
	out := make([]vp.Network, 0, len(sws))
	for _, s := range sws {
		out = append(out, p.normalizeSwitch(s))
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
	vmID := p.nextID("vm")
	gen := 1
	if spec.Firmware == vp.FirmwareUEFI || spec.Firmware == "" {
		gen = 2 // default to Generation 2 (UEFI) for new VMs
	}
	vm := &hypervVM{
		VMID:       vmID,
		Name:       spec.Name,
		State:      enabledStopped, // newly created VMs are off
		HostID:     spec.HostID,
		ClusterID:  spec.ClusterID,
		VCPUs:      spec.VCPUs,
		MemoryMB:   spec.MemoryMB,
		GuestOS:    spec.GuestOS,
		Firmware:   spec.Firmware,
		Generation: gen,
		Labels:     spec.Labels,
		Created:    time.Now().UTC().Unix(),
	}
	for i, d := range spec.Disks {
		vm.Disks = append(vm.Disks, hypervDisk{
			Index:     i,
			Label:     fmt.Sprintf("Hard Drive %d", i),
			Path:      fmt.Sprintf("%s\\%s\\%s_%d.vhdx", csvOrDefault(d.StorageID), spec.Name, spec.Name, i),
			StorageID: d.StorageID,
			Format:    diskFormatOrDefault(d.Format),
			SizeBytes: int64(d.CapacityGB * bytesPerGB),
		})
	}
	for i, n := range spec.NICs {
		vm.NICs = append(vm.NICs, hypervNIC{
			Index:     i,
			MAC:       n.MAC,
			SwitchID:  n.NetworkID,
			Connected: true,
		})
	}
	p.backend.createVM(vm)
	return p.finishTask("createVM", vmID), nil
}

func (p *Provider) PowerOp(ctx context.Context, vmID string, op vp.PowerOp) (*vp.Task, error) {
	if !op.Valid() {
		return nil, vp.ErrInvalidSpec
	}
	if !p.caps.Has(vp.PowerOpCapability(op)) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getVM(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	switch op {
	case vp.PowerStart, vp.PowerResume, vp.PowerReset:
		// Start-VM / resume from Saved / Restart-VM -> Enabled (running).
		p.backend.setState(vmID, enabledRunning)
	case vp.PowerStop:
		// Stop-VM (guest shutdown) / power off -> Disabled (stopped).
		p.backend.setState(vmID, enabledStopped)
	case vp.PowerSuspend:
		// Suspend-VM / Save-VM -> Saved (memory state saved to disk).
		p.backend.setState(vmID, enabledSaved)
	}
	return p.finishTask("powerOp", vmID), nil
}

func (p *Provider) DeleteVM(ctx context.Context, vmID string, opts vp.DeleteOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapDeleteVM) {
		return nil, vp.ErrUnsupported
	}
	vm, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if vm.Protected {
		return nil, vp.ErrConflict
	}
	if normalizeState(vm.State) == vp.StateRunning && !opts.Force {
		return nil, vp.ErrConflict
	}
	// Force on a running VM stops it first (Stop-VM -Force).
	if normalizeState(vm.State) == vp.StateRunning && opts.Force {
		p.backend.setState(vmID, enabledStopped)
	}
	p.backend.destroyVM(vmID)
	return p.finishTask("deleteVM", vmID), nil
}

func (p *Provider) ReconfigureVM(ctx context.Context, vmID string, spec vp.VMReconfigureSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapReconfigureVM) {
		return nil, vp.ErrUnsupported
	}
	vm, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if spec.VCPUs != nil {
		if *spec.VCPUs <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		vm.VCPUs = *spec.VCPUs
	}
	if spec.MemoryMB != nil {
		if *spec.MemoryMB <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		vm.MemoryMB = *spec.MemoryMB
	}
	for i, d := range spec.AddDisks {
		vm.Disks = append(vm.Disks, hypervDisk{
			Index:     len(vm.Disks) + i,
			Label:     fmt.Sprintf("Hard Drive %d", len(vm.Disks)+i),
			Path:      fmt.Sprintf("%s\\%s\\%s_add%d.vhdx", csvOrDefault(d.StorageID), vm.Name, vm.Name, i),
			StorageID: d.StorageID,
			Format:    diskFormatOrDefault(d.Format),
			SizeBytes: int64(d.CapacityGB * bytesPerGB),
		})
	}
	for i, n := range spec.AddNICs {
		vm.NICs = append(vm.NICs, hypervNIC{
			Index:     len(vm.NICs) + i,
			MAC:       n.MAC,
			SwitchID:  n.NetworkID,
			Connected: true,
		})
	}
	if spec.Labels != nil {
		if vm.Labels == nil {
			vm.Labels = map[string]string{}
		}
		for k, val := range spec.Labels {
			vm.Labels[k] = val
		}
	}
	return p.finishTask("reconfigureVM", vmID), nil
}

// --- checkpoints (snapshots) & clones ---

func (p *Provider) Snapshot(ctx context.Context, vmID string, opts vp.SnapshotOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getVM(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	// Checkpoint-VM: a running VM produces a "production" or "standard" checkpoint
	// (HasMemory true when including live memory state).
	snap := vp.Snapshot{
		ID:          p.nextID("checkpoint"),
		VMID:        vmID,
		Name:        opts.Name,
		Description: opts.Description,
		HasMemory:   opts.Memory,
		IsCurrent:   true,
		CreatedAt:   time.Now().UTC(),
	}
	p.backend.createSnapshot(vmID, snap)
	return p.finishTask("snapshot", vmID), nil
}

func (p *Provider) RevertSnapshot(ctx context.Context, vmID, snapID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapRevertSnapshot) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getVM(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	// Restore-VMSnapshot to the named checkpoint.
	if !p.backend.setCurrentSnapshot(vmID, snapID) {
		return nil, vp.ErrNotFound
	}
	return p.finishTask("revertSnapshot", vmID), nil
}

func (p *Provider) ListSnapshots(ctx context.Context, vmID string) ([]vp.Snapshot, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getVM(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	return p.backend.listSnapshots(vmID), nil
}

func (p *Provider) Clone(ctx context.Context, vmID string, spec vp.CloneSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapClone) {
		return nil, vp.ErrUnsupported
	}
	src, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, vp.ErrInvalidSpec
	}
	// Hyper-V clone = Export-VM then Import-VM (-Copy -GenerateNewId).
	newID := p.nextID("vm")
	clone := *src
	clone.VMID = newID
	clone.Name = spec.Name
	clone.Labels = cloneLabels(src.Labels)
	clone.Protected = false
	clone.State = enabledStopped
	if spec.PowerOn {
		clone.State = enabledRunning
	}
	if spec.HostID != "" {
		clone.HostID = spec.HostID
	}
	// deep-copy disks/NICs/IPs so the clone is independent
	clone.Disks = append([]hypervDisk(nil), src.Disks...)
	clone.NICs = append([]hypervNIC(nil), src.NICs...)
	clone.GuestIPs = append([]string(nil), src.GuestIPs...)
	p.backend.createVM(&clone)
	return p.finishTask("clone", newID), nil
}

// --- migration ---

func (p *Provider) MigrateVM(ctx context.Context, vmID, targetHost string, opts vp.MigrateOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMigrate) {
		return nil, vp.ErrUnsupported
	}
	vm, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if _, ok := p.backend.getHost(targetHost); !ok {
		return nil, vp.ErrInvalidSpec
	}
	// Move-VM / Live Migration: re-place the VM on the target cluster node.
	vm.HostID = targetHost
	return p.finishTask("migrate", vmID), nil
}

func (p *Provider) ExportVM(ctx context.Context, vmID string, format vp.DiskFormat) (io.ReadCloser, *vp.ExportInfo, error) {
	if !p.caps.Has(vp.CapExport) {
		return nil, nil, vp.ErrUnsupported
	}
	if !format.Valid() {
		return nil, nil, vp.ErrInvalidSpec
	}
	vm, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, nil, vp.ErrNotFound
	}
	// LIVE path: a real WMI/Hyper-V connection is attached but no real Export-VM
	// (VHDX streaming) is implemented yet. Fabricating a placeholder here would make
	// backup / V2V record a worthless stub as SUCCESS, so we HARD-ERROR instead.
	// Only the in-memory sim backend (tests) may return the placeholder below.
	if p.backend.isLive() {
		return nil, nil, fmt.Errorf("%w: VM export not yet implemented for hyperv", vp.ErrUnsupported)
	}
	// Stand in for an Export-VM streaming the VM's VHDX disk(s) for V2V.
	payload := fmt.Sprintf("HYPERVEXPORT\x00provider=%s\x00vm=%s\x00format=%s\n", p.id, vmID, format)
	info := &vp.ExportInfo{
		Format:     format,
		SizeBytes:  int64(len(payload)),
		DiskCount:  len(vm.Disks),
		SourceVMID: vmID,
		GuestOS:    vm.GuestOS,
		Firmware:   normalizeFirmware(vm),
	}
	return io.NopCloser(strings.NewReader(payload)), info, nil
}

// --- cluster & HA ---

func (p *Provider) GetClusterTopology(ctx context.Context, clusterID string) (*vp.Topology, error) {
	if !p.caps.Has(vp.CapClusterTopology) {
		return nil, vp.ErrUnsupported
	}
	cl, ok := p.backend.getCluster(clusterID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	top := &vp.Topology{ClusterID: clusterID, Placement: map[string]string{}}
	for _, hid := range cl.NodeIDs {
		if h, ok := p.backend.getHost(hid); ok {
			top.Nodes = append(top.Nodes, p.hostNodeState(h))
		}
	}
	for _, vm := range p.backend.listVMs() {
		if vm.ClusterID == clusterID {
			top.Placement[vm.VMID] = vm.HostID
		}
	}
	return top, nil
}

func (p *Provider) NodeState(ctx context.Context, nodeID string) (*vp.NodeState, error) {
	if !p.caps.Has(vp.CapNodeState) {
		return nil, vp.ErrUnsupported
	}
	h, ok := p.backend.getHost(nodeID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	ns := p.hostNodeState(h)
	return &ns, nil
}

// --- observability ---

func (p *Provider) GetMetrics(ctx context.Context, entityID string, window vp.MetricWindow) (*vp.MetricSeries, error) {
	if !p.caps.Has(vp.CapMetrics) {
		return nil, vp.ErrUnsupported
	}
	_, vmOK := p.backend.getVM(entityID)
	_, hostOK := p.backend.getHost(entityID)
	if !vmOK && !hostOK {
		return nil, vp.ErrNotFound
	}
	base := window.Since
	if base.IsZero() {
		base = time.Now().Add(-5 * time.Minute).UTC()
	}
	step := window.StepSecond
	if step <= 0 {
		step = 30 // Hyper-V resource metering default sampling
	}
	series := &vp.MetricSeries{EntityID: entityID}
	for i := 0; i < 5; i++ {
		series.Samples = append(series.Samples, vp.MetricSample{
			Timestamp:      base.Add(time.Duration(i*step) * time.Second),
			CPUPercent:     float64(11 + i*5),
			MemUsageBytes:  uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes:  4 << 30,
			NetRxBytes:     uint64(i) * 1500,
			NetTxBytes:     uint64(i) * 1100,
			DiskReadBytes:  uint64(i) * 8192,
			DiskWriteBytes: uint64(i) * 12288,
		})
	}
	return series, nil
}

func (p *Provider) StreamEvents(ctx context.Context) (<-chan vp.Event, error) {
	if !p.caps.Has(vp.CapEvents) {
		return nil, vp.ErrUnsupported
	}
	// REAL content only: emit one truthful vm.state event per VM reflecting its
	// CURRENT state, then close. No fabricated heartbeat content. (hyperv is a
	// non-live model provider; a snapshot of real inventory state is the honest
	// event surface — when wired to a live host's WMI event subscription this
	// becomes the real __InstanceModificationEvent stream.)
	ch := make(chan vp.Event, 8)
	go func() {
		defer close(ch)
		now := time.Now().UTC()
		vms := p.backend.listVMs()
		if len(vms) == 0 {
			select {
			case ch <- vp.Event{Kind: vp.EventAlert, ProviderID: p.id,
				Message: "event stream established (no VMs)", Timestamp: now}:
			case <-ctx.Done():
			}
			return
		}
		for _, vm := range vms {
			v := p.normalizeVM(vm)
			select {
			case ch <- vp.Event{
				Kind:       vp.EventVMStateChanged,
				ProviderID: p.id,
				EntityID:   v.ID,
				Message:    fmt.Sprintf("%s is %s", v.Name, v.State),
				Timestamp:  now,
			}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return ch, nil
}

// --- helpers ---

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func cloneLabels(in map[string]string) map[string]string {
	if in == nil {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func csvOrDefault(id string) string {
	if id == "" {
		return "C:\\ClusterStorage\\Volume1"
	}
	return id
}

func itoa(i int) string         { return strconv.Itoa(i) }
func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }
func nowUTC() time.Time         { return time.Now().UTC() }

// compile-time assertion: *Provider satisfies the contract.
var _ vp.HypervisorProvider = (*Provider)(nil)
