// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package kvm

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

// FullCaps is the realistic libvirt/KVM capability set. libvirt/QEMU supports the
// entire contract: inventory reads, full lifecycle, snapshots/revert, clone
// (virt-clone), migration (virDomainMigrate), disk export for V2V (qemu-img),
// per-domain metrics and the event stream. KVM has no NATIVE cluster object, but
// this provider models ONE logical cluster grouping its hosts, so it legitimately
// declares CapListClusters + CapClusterTopology + CapNodeState (see D-005 / prompt
// item 4). All bits are therefore set.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// libvirtBackend is the seam between the pure-Go normalization core and a concrete
// libvirt transport. The default build wires it to an in-memory simulator
// (simBackend, sim_backend.go). A live pure-Go socket client (go-libvirt) can be
// wired behind //go:build libvirt_live (live.go) without touching this core.
//
// Methods operate in libvirt-native terms (UUIDs, state ints, KiB, byte sizes);
// the Provider does all contract normalization and capability/error mapping.
type libvirtBackend interface {
	// connection
	version() string
	healthy() bool
	close() error

	// inventory (libvirt-native)
	listNodes() []*libvirtNode
	getNode(id string) (*libvirtNode, bool)
	listDomains() []*libvirtDomain
	getDomain(uuid string) (*libvirtDomain, bool)
	listPools() []*libvirtPool
	listNets() []*libvirtNet

	// lifecycle
	defineDomain(d *libvirtDomain) // create/define a new domain
	undefineDomain(uuid string)    // delete a domain
	setDomainState(uuid string, s libvirtState)
	domainsOnHost(hostID string) int

	// snapshots
	listSnapshots(uuid string) []vp.Snapshot
	createSnapshot(uuid string, snap vp.Snapshot)
	setCurrentSnapshot(uuid, snapID string) bool

	// host/cluster
	hostIDs() []string

	// identity
	clusterName() string
}

// extBackend is the OPTIONAL extension seam a libvirtBackend may also satisfy to
// expose the official-libvirt console / network-write / storage-write surface
// (extended.go: ConsoleProvider, NetworkWriter, StorageProvider). The real
// liveBackend implements it; the in-memory simBackend does not, so the default
// (sim-backed) kvm provider does NOT advertise the extension capability bits and
// the Provider extension methods return ErrUnsupported. These methods DO return
// errors (unlike the core seam) because they map libvirt RPC failures to the
// contract sentinels directly.
type extBackend interface {
	// console: parse <graphics> from the domain XML (DomainGetXMLDesc).
	console(uuid string) (*vp.ConsoleEndpoint, error)
	// network write: NetworkDefineXML + NetworkCreate + NetworkSetAutostart /
	// NetworkLookupBy* + NetworkDestroy + NetworkUndefine.
	createNetwork(spec vp.NetworkSpec) error
	deleteNetwork(id string) error
	// storage write: StoragePool* + StorageVol* (+ StorageVolUpload stream).
	listVolumes(storageID string) ([]vp.Volume, error)
	createVolume(spec vp.VolumeSpec) error
	deleteVolume(storageID, volumeID string) error
	uploadISO(storageID, name string, size int64, r io.Reader) (*vp.Volume, error)
}

// Provider is the KVM/libvirt HypervisorProvider. The core is CGO-free; the
// libvirt-specific bits live behind the libvirtBackend seam.
type Provider struct {
	id        string
	kind      vp.HypervisorKind
	caps      vp.CapabilityMatrix
	clusterID string // logical cluster id (KVM has no native cluster)

	backend libvirtBackend
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

// WithBackend injects a libvirtBackend (default in the default build: a seeded
// in-memory simulator). Live transport is injected via the libvirt_live build.
func WithBackend(b libvirtBackend) Option { return func(p *Provider) { p.backend = b } }

// New constructs a KVM provider. With no WithBackend option it uses the seeded
// in-memory simulator so it can be constructed in tests without libvirtd.
func New(id string, opts ...Option) *Provider {
	p := &Provider{
		id:        id,
		kind:      vp.KindKVM,
		caps:      FullCaps,
		clusterID: "kvm-logical-cluster",
		tracker:   vp.NewTaskTracker(),
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

func (p *Provider) Kind() vp.HypervisorKind            { return p.kind }
func (p *Provider) ID() string                         { return p.id }
func (p *Provider) Capabilities() vp.CapabilityMatrix  { return p.caps }

func (p *Provider) HealthCheck(ctx context.Context) (vp.HealthStatus, error) {
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed || !p.backend.healthy() {
		return vp.HealthStatus{Healthy: false, Message: "libvirt connection unavailable",
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
	nodes := p.backend.listNodes()
	out := make([]vp.Host, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, p.normalizeNode(n))
	}
	return out, nil
}

func (p *Provider) ListVMs(ctx context.Context, opts vp.ListOptions) ([]vp.VM, error) {
	if !p.caps.Has(vp.CapListVMs) {
		return nil, vp.ErrUnsupported
	}
	doms := p.backend.listDomains()
	out := make([]vp.VM, 0, len(doms))
	for _, d := range doms {
		v := p.normalizeDomain(d)
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
	d, ok := p.backend.getDomain(id)
	if !ok {
		return nil, vp.ErrNotFound
	}
	v := p.normalizeDomain(d)
	// Raw mirrors what a libvirt inspect would surface (domain XML-ish view).
	raw, _ := json.Marshal(map[string]any{
		"uuid":     d.UUID,
		"name":     d.Name,
		"stateInt": int(d.State),
		"state":    d.State.raw(),
		"vcpu":     d.VCPUs,
		"memoryKiB": d.MemoryKB,
		"osType":   d.OSType,
		"firmware": d.Firmware,
	})
	return &vp.VMDetail{VM: v, Raw: raw}, nil
}

func (p *Provider) ListClusters(ctx context.Context) ([]vp.Cluster, error) {
	if !p.caps.Has(vp.CapListClusters) {
		return nil, vp.ErrUnsupported
	}
	// KVM has no native cluster object; model exactly one logical cluster that
	// groups every host this provider knows about.
	return []vp.Cluster{{
		ID:         p.clusterID,
		Name:       p.backend.clusterName(),
		Kind:       p.kind,
		ProviderID: p.id,
		HostIDs:    p.backend.hostIDs(),
		HAEnabled:  false, // plain libvirt has no built-in HA
	}}, nil
}

func (p *Provider) ListStorage(ctx context.Context) ([]vp.StoragePool, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	pools := p.backend.listPools()
	out := make([]vp.StoragePool, 0, len(pools))
	for _, pl := range pools {
		out = append(out, p.normalizePool(pl))
	}
	return out, nil
}

func (p *Provider) ListNetworks(ctx context.Context) ([]vp.Network, error) {
	if !p.caps.Has(vp.CapListNetworks) {
		return nil, vp.ErrUnsupported
	}
	nets := p.backend.listNets()
	out := make([]vp.Network, 0, len(nets))
	for _, n := range nets {
		out = append(out, p.normalizeNet(n))
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
	uuid := p.nextID("dom")
	d := &libvirtDomain{
		UUID:     uuid,
		Name:     spec.Name,
		State:    domShutoff, // freshly defined domains are shutoff
		HostID:   spec.HostID,
		VCPUs:    spec.VCPUs,
		MemoryKB: spec.MemoryMB * 1024,
		OSType:   spec.GuestOS,
		Firmware: spec.Firmware,
		Labels:   spec.Labels,
		Created:  time.Now().UTC().Unix(),
		BootISO:  spec.BootISO,
	}
	for i, dk := range spec.Disks {
		d.Disks = append(d.Disks, libvirtDisk{
			Target:   "vd" + string(rune('a'+i)),
			Driver:   string(normalizeDiskFormat(string(dk.Format))),
			Source:   dk.SourcePath,
			Pool:     dk.StorageID,
			CapBytes: int64(dk.CapacityGB * bytesPerGB),
		})
	}
	for _, n := range spec.NICs {
		d.NICs = append(d.NICs, libvirtNIC{
			MAC:     n.MAC,
			Network: n.NetworkID,
			Model:   n.Model,
			Link:    true,
		})
	}
	p.backend.defineDomain(d)
	return p.finishTask("createVM", uuid), nil
}

func (p *Provider) PowerOp(ctx context.Context, vmID string, op vp.PowerOp) (*vp.Task, error) {
	if !op.Valid() {
		return nil, vp.ErrInvalidSpec
	}
	if !p.caps.Has(vp.PowerOpCapability(op)) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getDomain(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	switch op {
	case vp.PowerStart, vp.PowerResume, vp.PowerReset:
		// start: shutoff->running; resume: paused->running; reset: hard reboot.
		p.backend.setDomainState(vmID, domRunning)
	case vp.PowerStop:
		p.backend.setDomainState(vmID, domShutoff)
	case vp.PowerSuspend:
		// suspend == save state / guest-PM suspend -> pmsuspended.
		p.backend.setDomainState(vmID, domPMSuspended)
	}
	return p.finishTask("powerOp", vmID), nil
}

func (p *Provider) DeleteVM(ctx context.Context, vmID string, opts vp.DeleteOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapDeleteVM) {
		return nil, vp.ErrUnsupported
	}
	d, ok := p.backend.getDomain(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if normalizeState(d.State) == vp.StateRunning && !opts.Force {
		return nil, vp.ErrConflict
	}
	// Force on a running domain destroys it first (virDomainDestroy).
	if normalizeState(d.State) == vp.StateRunning && opts.Force {
		p.backend.setDomainState(vmID, domShutoff)
	}
	p.backend.undefineDomain(vmID)
	return p.finishTask("deleteVM", vmID), nil
}

func (p *Provider) ReconfigureVM(ctx context.Context, vmID string, spec vp.VMReconfigureSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapReconfigureVM) {
		return nil, vp.ErrUnsupported
	}
	d, ok := p.backend.getDomain(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if spec.VCPUs != nil {
		if *spec.VCPUs <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		d.VCPUs = *spec.VCPUs
	}
	if spec.MemoryMB != nil {
		if *spec.MemoryMB <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		d.MemoryKB = *spec.MemoryMB * 1024
	}
	for i, dk := range spec.AddDisks {
		d.Disks = append(d.Disks, libvirtDisk{
			Target:   "vd" + string(rune('a'+len(d.Disks)+i)),
			Driver:   string(normalizeDiskFormat(string(dk.Format))),
			Source:   dk.SourcePath,
			Pool:     dk.StorageID,
			CapBytes: int64(dk.CapacityGB * bytesPerGB),
		})
	}
	for _, n := range spec.AddNICs {
		d.NICs = append(d.NICs, libvirtNIC{MAC: n.MAC, Network: n.NetworkID, Model: n.Model, Link: true})
	}
	if spec.Labels != nil {
		if d.Labels == nil {
			d.Labels = map[string]string{}
		}
		for k, val := range spec.Labels {
			d.Labels[k] = val
		}
	}
	return p.finishTask("reconfigureVM", vmID), nil
}

// --- snapshots & clones ---

func (p *Provider) Snapshot(ctx context.Context, vmID string, opts vp.SnapshotOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getDomain(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	snap := vp.Snapshot{
		ID:          p.nextID("snap"),
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
	if _, ok := p.backend.getDomain(vmID); !ok {
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
	if _, ok := p.backend.getDomain(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	return p.backend.listSnapshots(vmID), nil
}

func (p *Provider) Clone(ctx context.Context, vmID string, spec vp.CloneSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapClone) {
		return nil, vp.ErrUnsupported
	}
	src, ok := p.backend.getDomain(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, vp.ErrInvalidSpec
	}
	uuid := p.nextID("dom")
	clone := *src
	clone.UUID = uuid
	clone.Name = spec.Name
	clone.Labels = cloneLabels(src.Labels)
	clone.State = domShutoff
	if spec.PowerOn {
		clone.State = domRunning
	}
	if spec.HostID != "" {
		clone.HostID = spec.HostID
	}
	// deep-copy disks/NICs so the clone is independent
	clone.Disks = append([]libvirtDisk(nil), src.Disks...)
	clone.NICs = append([]libvirtNIC(nil), src.NICs...)
	p.backend.defineDomain(&clone)
	return p.finishTask("clone", uuid), nil
}

// --- migration ---

func (p *Provider) MigrateVM(ctx context.Context, vmID, targetHost string, opts vp.MigrateOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMigrate) {
		return nil, vp.ErrUnsupported
	}
	d, ok := p.backend.getDomain(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if _, ok := p.backend.getNode(targetHost); !ok {
		return nil, vp.ErrInvalidSpec
	}
	d.HostID = targetHost
	return p.finishTask("migrate", vmID), nil
}

func (p *Provider) ExportVM(ctx context.Context, vmID string, format vp.DiskFormat) (io.ReadCloser, *vp.ExportInfo, error) {
	if !p.caps.Has(vp.CapExport) {
		return nil, nil, vp.ErrUnsupported
	}
	if !format.Valid() {
		return nil, nil, vp.ErrInvalidSpec
	}
	d, ok := p.backend.getDomain(vmID)
	if !ok {
		return nil, nil, vp.ErrNotFound
	}
	// Stand in for qemu-img convert streaming the domain's disk(s).
	payload := fmt.Sprintf("KVMEXPORT\x00provider=%s\x00domain=%s\x00format=%s\n", p.id, vmID, format)
	info := &vp.ExportInfo{
		Format:     format,
		SizeBytes:  int64(len(payload)),
		DiskCount:  len(d.Disks),
		SourceVMID: vmID,
		GuestOS:    d.OSType,
		Firmware:   d.Firmware,
	}
	return io.NopCloser(strings.NewReader(payload)), info, nil
}

// --- cluster & HA ---

func (p *Provider) GetClusterTopology(ctx context.Context, clusterID string) (*vp.Topology, error) {
	if !p.caps.Has(vp.CapClusterTopology) {
		return nil, vp.ErrUnsupported
	}
	if clusterID != p.clusterID {
		return nil, vp.ErrNotFound
	}
	top := &vp.Topology{ClusterID: clusterID, Placement: map[string]string{}}
	for _, n := range p.backend.listNodes() {
		h := p.normalizeNode(n)
		top.Nodes = append(top.Nodes, vp.NodeState{
			NodeID: h.ID, State: h.State, VMCount: h.VMCount, UpdatedAt: time.Now().UTC(),
		})
	}
	for _, d := range p.backend.listDomains() {
		top.Placement[d.UUID] = d.HostID
	}
	return top, nil
}

func (p *Provider) NodeState(ctx context.Context, nodeID string) (*vp.NodeState, error) {
	if !p.caps.Has(vp.CapNodeState) {
		return nil, vp.ErrUnsupported
	}
	n, ok := p.backend.getNode(nodeID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	h := p.normalizeNode(n)
	return &vp.NodeState{
		NodeID: nodeID, State: h.State, VMCount: h.VMCount, UpdatedAt: time.Now().UTC(),
	}, nil
}

// --- observability ---

func (p *Provider) GetMetrics(ctx context.Context, entityID string, window vp.MetricWindow) (*vp.MetricSeries, error) {
	if !p.caps.Has(vp.CapMetrics) {
		return nil, vp.ErrUnsupported
	}
	_, domOK := p.backend.getDomain(entityID)
	_, nodeOK := p.backend.getNode(entityID)
	if !domOK && !nodeOK {
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
			Timestamp:      base.Add(time.Duration(i*step) * time.Second),
			CPUPercent:     float64(8 + i*7),
			MemUsageBytes:  uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes:  4 << 30,
			NetRxBytes:     uint64(i) * 1500,
			NetTxBytes:     uint64(i) * 1100,
			DiskReadBytes:  uint64(i) * 4096,
			DiskWriteBytes: uint64(i) * 8192,
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
					Message:    fmt.Sprintf("libvirt lifecycle heartbeat %d", i),
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

func itoa(i int) string      { return strconv.Itoa(i) }
func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }

// ext returns the backend's extension surface if it implements one (i.e. the live
// libvirt backend); the sim backend does not, so callers fall back to ErrUnsupported.
func (p *Provider) ext() (extBackend, bool) {
	e, ok := p.backend.(extBackend)
	return e, ok
}

// --- extension: graphical console (ConsoleProvider) ---

// Console returns the VM's graphical console endpoint, read from the live domain
// XML's <graphics type='vnc'|'spice'> element (the official libvirt way to expose
// a console). Requires CapConsole and a live backend.
func (p *Provider) Console(ctx context.Context, vmID string) (*vp.ConsoleEndpoint, error) {
	if !p.caps.Has(vp.CapConsole) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getDomain(vmID); !ok {
		return nil, vp.ErrNotFound
	}
	return e.console(vmID)
}

// --- extension: network write (NetworkWriter) ---

func (p *Provider) CreateNetwork(ctx context.Context, spec vp.NetworkSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapNetworkWrite) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, vp.ErrInvalidSpec
	}
	if err := e.createNetwork(spec); err != nil {
		return nil, err
	}
	return p.finishTask("createNetwork", spec.Name), nil
}

func (p *Provider) DeleteNetwork(ctx context.Context, networkID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapNetworkWrite) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if err := e.deleteNetwork(networkID); err != nil {
		return nil, err
	}
	return p.finishTask("deleteNetwork", networkID), nil
}

// --- extension: storage / ISO write (StorageProvider) ---

func (p *Provider) ListVolumes(ctx context.Context, storageID string) ([]vp.Volume, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	return e.listVolumes(storageID)
}

func (p *Provider) CreateVolume(ctx context.Context, spec vp.VolumeSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapStorageWrite) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if strings.TrimSpace(spec.Name) == "" || spec.CapacityGB <= 0 {
		return nil, vp.ErrInvalidSpec
	}
	if err := e.createVolume(spec); err != nil {
		return nil, err
	}
	return p.finishTask("createVolume", spec.Name), nil
}

func (p *Provider) DeleteVolume(ctx context.Context, storageID, volumeID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapStorageWrite) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if err := e.deleteVolume(storageID, volumeID); err != nil {
		return nil, err
	}
	return p.finishTask("deleteVolume", volumeID), nil
}

func (p *Provider) UploadISO(ctx context.Context, storageID, name string, size int64, r io.Reader) (*vp.Volume, error) {
	if !p.caps.Has(vp.CapStorageWrite) {
		return nil, vp.ErrUnsupported
	}
	e, ok := p.ext()
	if !ok {
		return nil, vp.ErrUnsupported
	}
	if strings.TrimSpace(name) == "" {
		return nil, vp.ErrInvalidSpec
	}
	return e.uploadISO(storageID, name, size, r)
}

// compile-time assertion: *Provider satisfies the contract.
var _ vp.HypervisorProvider = (*Provider)(nil)

// *Provider also satisfies the extension contracts; whether a given instance
// actually services them depends on the backend (live vs sim) + capability bits.
var (
	_ vp.ConsoleProvider = (*Provider)(nil)
	_ vp.NetworkWriter   = (*Provider)(nil)
	_ vp.StorageProvider = (*Provider)(nil)
)
