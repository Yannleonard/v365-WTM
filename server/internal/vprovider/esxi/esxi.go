// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package esxi

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

// FullCaps is the realistic vSphere/ESXi capability set. vCenter/ESXi supports the
// entire contract: inventory reads (hosts/VMs/clusters/datastores/networks),
// GetVM, full lifecycle (create/power start,stop,reset,suspend,resume/delete/
// reconfigure), snapshots + revert, clone (full & linked), migration (vMotion /
// relocate), disk export for V2V (OVF/VMDK), cluster topology (DRS/HA), per-host
// node state, performance metrics (PerformanceManager) and the event stream
// (EventManager / property collector). All bits are therefore set.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// vsphereBackend is the seam between the pure-Go normalization core and a concrete
// vSphere transport. The default build wires it to an in-memory simulator
// (simBackend, sim_backend.go) — the moral equivalent of vcsim faked in Go. A live
// pure-Go govmomi client can be wired behind //go:build vsphere_live (live.go)
// without touching this core and without adding govmomi to go.mod for the default
// build.
//
// Methods operate in vSphere-native terms (managed-object refs, powerState tokens,
// MB/KB/byte sizes); the Provider does all contract normalization and
// capability/error mapping.
type vsphereBackend interface {
	// connection
	version() string
	healthy() bool
	close() error

	// inventory (vSphere-native)
	listHosts() []*vsphereHost
	getHost(moRef string) (*vsphereHost, bool)
	listVMs() []*vsphereVM
	getVM(moRef string) (*vsphereVM, bool)
	listClusters() []*vsphereCluster
	getCluster(moRef string) (*vsphereCluster, bool)
	listDatastores() []*vsphereDatastore
	listNetworks() []*vsphereNetwork

	// lifecycle
	createVM(vm *vsphereVM) // create/register a new VM
	destroyVM(moRef string) // delete a VM
	setPower(moRef string, s powerState)
	vmsOnHost(hostRef string) int

	// snapshots
	listSnapshots(moRef string) []vp.Snapshot
	createSnapshot(moRef string, snap vp.Snapshot)
	setCurrentSnapshot(moRef, snapID string) bool
}

// Provider is the VMware ESXi/vSphere HypervisorProvider. The core is CGO-free; the
// vSphere-specific bits live behind the vsphereBackend seam.
type Provider struct {
	id   string
	kind vp.HypervisorKind
	caps vp.CapabilityMatrix

	backend vsphereBackend
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

// WithBackend injects a vsphereBackend (default in the default build: a seeded
// in-memory simulator). Live transport is injected via the vsphere_live build.
func WithBackend(b vsphereBackend) Option { return func(p *Provider) { p.backend = b } }

// New constructs a vSphere provider. With no WithBackend option it uses the seeded
// in-memory simulator so it can be constructed in tests without a vCenter/ESXi.
func New(id string, opts ...Option) *Provider {
	p := &Provider{
		id:      id,
		kind:    vp.KindVMware,
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
		return vp.HealthStatus{Healthy: false, Message: "vSphere connection unavailable",
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
	// Raw mirrors what a vSphere inspect would surface (managed-object view).
	raw, _ := json.Marshal(map[string]any{
		"moRef":          vm.MoRef,
		"name":           vm.Name,
		"powerState":     vm.Power.raw(),
		"numCPU":         vm.NumCPU,
		"memoryMB":       vm.MemoryMB,
		"guestId":        vm.GuestID,
		"firmware":       vm.Firmware,
		"runtime.host":   vm.HostRef,
		"resourcePool":   vm.ClusterID,
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
	dss := p.backend.listDatastores()
	out := make([]vp.StoragePool, 0, len(dss))
	for _, d := range dss {
		out = append(out, p.normalizeDatastore(d))
	}
	return out, nil
}

func (p *Provider) ListNetworks(ctx context.Context) ([]vp.Network, error) {
	if !p.caps.Has(vp.CapListNetworks) {
		return nil, vp.ErrUnsupported
	}
	nets := p.backend.listNetworks()
	out := make([]vp.Network, 0, len(nets))
	for _, n := range nets {
		out = append(out, p.normalizeNetwork(n))
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
	moRef := p.nextID("vm")
	vm := &vsphereVM{
		MoRef:     moRef,
		Name:      spec.Name,
		Power:     powerOff, // freshly created VMs are powered off
		HostRef:   spec.HostID,
		ClusterID: spec.ClusterID,
		NumCPU:    spec.VCPUs,
		MemoryMB:  spec.MemoryMB,
		GuestID:   spec.GuestOS,
		Firmware:  spec.Firmware,
		Labels:    spec.Labels,
		Created:   time.Now().UTC().Unix(),
	}
	for i, d := range spec.Disks {
		vm.Disks = append(vm.Disks, vsphereDisk{
			Key:         i,
			Label:       fmt.Sprintf("Hard disk %d", i+1),
			VMDKPath:    fmt.Sprintf("[%s] %s/%s_%d.vmdk", dsOrDefault(d.StorageID), spec.Name, spec.Name, i),
			DatastoreID: d.StorageID,
			CapacityKB:  int64(d.CapacityGB * bytesPerGB / 1024),
		})
	}
	for i, n := range spec.NICs {
		vm.NICs = append(vm.NICs, vsphereNIC{
			Key:         i,
			MAC:         n.MAC,
			PortgroupID: n.NetworkID,
			AdapterType: nicModelOrDefault(n.Model),
			Connected:   true,
		})
	}
	p.backend.createVM(vm)
	return p.finishTask("createVM", moRef), nil
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
		// PowerOnVM_Task / reset / resume from suspend -> poweredOn.
		p.backend.setPower(vmID, powerOn)
	case vp.PowerStop:
		// ShutdownGuest / PowerOffVM_Task -> poweredOff.
		p.backend.setPower(vmID, powerOff)
	case vp.PowerSuspend:
		// SuspendVM_Task -> suspended (memory state saved).
		p.backend.setPower(vmID, powerSuspended)
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
	if normalizeState(vm.Power) == vp.StateRunning && !opts.Force {
		return nil, vp.ErrConflict
	}
	// Force on a running VM powers it off first (PowerOffVM_Task).
	if normalizeState(vm.Power) == vp.StateRunning && opts.Force {
		p.backend.setPower(vmID, powerOff)
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
		vm.NumCPU = *spec.VCPUs
	}
	if spec.MemoryMB != nil {
		if *spec.MemoryMB <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		vm.MemoryMB = *spec.MemoryMB
	}
	for i, d := range spec.AddDisks {
		vm.Disks = append(vm.Disks, vsphereDisk{
			Key:         len(vm.Disks) + i,
			Label:       fmt.Sprintf("Hard disk %d", len(vm.Disks)+i+1),
			VMDKPath:    fmt.Sprintf("[%s] %s/%s_add%d.vmdk", dsOrDefault(d.StorageID), vm.Name, vm.Name, i),
			DatastoreID: d.StorageID,
			CapacityKB:  int64(d.CapacityGB * bytesPerGB / 1024),
		})
	}
	for i, n := range spec.AddNICs {
		vm.NICs = append(vm.NICs, vsphereNIC{
			Key:         len(vm.NICs) + i,
			MAC:         n.MAC,
			PortgroupID: n.NetworkID,
			AdapterType: nicModelOrDefault(n.Model),
			Connected:   true,
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

// --- snapshots & clones ---

func (p *Provider) Snapshot(ctx context.Context, vmID string, opts vp.SnapshotOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getVM(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	snap := vp.Snapshot{
		ID:          p.nextID("snapshot"),
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
	moRef := p.nextID("vm")
	clone := *src
	clone.MoRef = moRef
	clone.Name = spec.Name
	clone.Labels = cloneLabels(src.Labels)
	clone.Protected = false
	clone.Power = powerOff
	if spec.PowerOn {
		clone.Power = powerOn
	}
	if spec.HostID != "" {
		clone.HostRef = spec.HostID
	}
	// deep-copy disks/NICs so the clone is independent
	clone.Disks = append([]vsphereDisk(nil), src.Disks...)
	clone.NICs = append([]vsphereNIC(nil), src.NICs...)
	clone.GuestIPs = append([]string(nil), src.GuestIPs...)
	p.backend.createVM(&clone)
	return p.finishTask("clone", moRef), nil
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
	// vMotion (live) / cold relocate: re-place the VM on the target host.
	vm.HostRef = targetHost
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
	// Stand in for an OVF/VMDK export (HttpNfcLease) streaming the VM's disk(s).
	payload := fmt.Sprintf("VSPHEREEXPORT\x00provider=%s\x00vm=%s\x00format=%s\n", p.id, vmID, format)
	info := &vp.ExportInfo{
		Format:     format,
		SizeBytes:  int64(len(payload)),
		DiskCount:  len(vm.Disks),
		SourceVMID: vmID,
		GuestOS:    vm.GuestID,
		Firmware:   vm.Firmware,
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
	for _, hid := range cl.HostIDs {
		if h, ok := p.backend.getHost(hid); ok {
			top.Nodes = append(top.Nodes, p.hostNodeState(h))
		}
	}
	for _, vm := range p.backend.listVMs() {
		if vm.ClusterID == clusterID {
			top.Placement[vm.MoRef] = vm.HostRef
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
		step = 20 // vSphere PerformanceManager default real-time interval
	}
	series := &vp.MetricSeries{EntityID: entityID}
	for i := 0; i < 5; i++ {
		series.Samples = append(series.Samples, vp.MetricSample{
			Timestamp:      base.Add(time.Duration(i*step) * time.Second),
			CPUPercent:     float64(12 + i*6),
			MemUsageBytes:  uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes:  4 << 30,
			NetRxBytes:     uint64(i) * 2000,
			NetTxBytes:     uint64(i) * 1300,
			DiskReadBytes:  uint64(i) * 8192,
			DiskWriteBytes: uint64(i) * 16384,
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
				case ch <- vp.Event{
					Kind:       vp.EventAlert,
					ProviderID: p.id,
					Message:    fmt.Sprintf("vSphere EventManager heartbeat %d", i),
					Timestamp:  time.Now().UTC(),
				}:
				case <-ctx.Done():
					return
				}
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

func dsOrDefault(id string) string {
	if id == "" {
		return "datastore1"
	}
	return id
}

func nicModelOrDefault(model string) string {
	if model == "" {
		return "vmxnet3"
	}
	return model
}

func itoa(i int) string         { return strconv.Itoa(i) }
func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }
func nowUTC() time.Time         { return time.Now().UTC() }

// compile-time assertion: *Provider satisfies the contract.
var _ vp.HypervisorProvider = (*Provider)(nil)
