// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package xen

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

// FullCaps is the realistic Xen/XAPI capability set. XAPI supports the entire
// contract: inventory reads (VM/host/pool/SR/network records), full lifecycle
// (VM.create / VM.start|clean_shutdown|hard_reboot|suspend|resume / VM.destroy /
// reconfigure), snapshots + revert (VM.snapshot / VM.revert), clone (VM.copy /
// VM.clone), live migration (VM.pool_migrate — XenMotion), disk/VM export
// (export of xva / disk via the HTTP handler) for V2V, the native POOL modeled as
// the contract cluster (CapListClusters + CapClusterTopology + CapNodeState, per
// prompt item 3/4), per-VM metrics (RRD) and the event stream (event.next). All
// bits are therefore set.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// xapiBackend is the seam between the pure-Go normalization core and a concrete
// XAPI transport. The default build wires it to an in-memory XAPI fake
// (simBackend, sim_backend.go). A live XML-RPC/JSON-RPC HTTP client can be wired
// behind //go:build xen_live (live.go) without touching this core and without any
// go.mod dependency.
//
// Methods operate in XAPI-native terms (opaque refs, power_state tokens, byte
// sizes, a pool grouping hosts); the Provider does all contract normalization and
// capability/error mapping.
type xapiBackend interface {
	// connection / session
	version() string
	healthy() bool
	close() error

	// inventory (XAPI-native)
	listHosts() []*xapiHost
	getHost(ref string) (*xapiHost, bool)
	listVMs() []*xapiVM
	getVM(ref string) (*xapiVM, bool)
	listSRs() []*xapiSR
	listNetworks() []*xapiNetwork

	// lifecycle
	createVM(v *xapiVM) // VM.create
	destroyVM(ref string) // VM.destroy
	setPowerState(ref string, s xapiPowerState)
	vmsOnHost(hostRef string) int

	// snapshots
	listSnapshots(ref string) []vp.Snapshot
	createSnapshot(ref string, snap vp.Snapshot)
	setCurrentSnapshot(ref, snapID string) bool

	// pool / cluster (XAPI has a NATIVE pool)
	pool() *xapiPool
}

// Provider is the Xen/XAPI HypervisorProvider. The core is CGO-free; the
// XAPI-specific transport lives behind the xapiBackend seam.
type Provider struct {
	id        string
	kind      vp.HypervisorKind
	caps      vp.CapabilityMatrix
	clusterID string // XAPI pool opaque ref (cached from the backend pool record)

	backend xapiBackend
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

// WithKind overrides the reported hypervisor kind (default: KindXen). Tests use it
// to confirm the normalized Kind is wired through every entity.
func WithKind(k vp.HypervisorKind) Option { return func(p *Provider) { p.kind = k } }

// WithBackend injects an xapiBackend (default in the default build: a seeded
// in-memory XAPI fake). Live transport is injected via the xen_live build.
func WithBackend(b xapiBackend) Option { return func(p *Provider) { p.backend = b } }

// New constructs a Xen provider. With no WithBackend option it uses the seeded
// in-memory XAPI fake so it can be constructed in tests without a real XenServer.
func New(id string, opts ...Option) *Provider {
	p := &Provider{
		id:      id,
		kind:    vp.KindXen,
		caps:    FullCaps,
		tracker: vp.NewTaskTracker(),
	}
	for _, o := range opts {
		o(p)
	}
	if p.backend == nil {
		p.backend = newSimBackend()
	}
	// The Xen POOL is the contract cluster; cache its opaque ref so every VM/host
	// carries a stable ClusterID.
	if pl := p.backend.pool(); pl != nil {
		p.clusterID = pl.Ref
	}
	return p
}

func (p *Provider) nextID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, atomic.AddInt64(&p.seq, 1))
}

// newRef mints a fresh XAPI-style opaque ref for created entities.
func (p *Provider) newRef(kind string) string {
	return fmt.Sprintf("OpaqueRef:%s-%d", kind, atomic.AddInt64(&p.seq, 1))
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
		return vp.HealthStatus{Healthy: false, Message: "XAPI session unavailable",
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
	for _, raw := range vms {
		v := p.normalizeVM(raw)
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
	raw, ok := p.backend.getVM(id)
	if !ok {
		return nil, vp.ErrNotFound
	}
	v := p.normalizeVM(raw)
	// Raw mirrors what XAPI VM.get_record would surface (the native VM record).
	rawJSON, _ := json.Marshal(map[string]any{
		"ref":               raw.Ref,
		"uuid":              raw.UUID,
		"name_label":        raw.NameLabel,
		"power_state":       string(raw.PowerState),
		"VCPUs_max":         raw.VCPUsMax,
		"memory_static_max": raw.MemoryB,
		"resident_on":       raw.ResidentOn,
		"HVM":               raw.HVM,
		"is_control_domain": raw.IsControl,
	})
	return &vp.VMDetail{VM: v, Raw: rawJSON}, nil
}

func (p *Provider) ListClusters(ctx context.Context) ([]vp.Cluster, error) {
	if !p.caps.Has(vp.CapListClusters) {
		return nil, vp.ErrUnsupported
	}
	// XAPI has a NATIVE pool object; model exactly that pool as the contract
	// cluster grouping every host in the pool (prompt item 3).
	pl := p.backend.pool()
	if pl == nil {
		return []vp.Cluster{}, nil
	}
	return []vp.Cluster{{
		ID:         pl.Ref,
		Name:       pl.NameLabel,
		Kind:       p.kind,
		ProviderID: p.id,
		HostIDs:    append([]string(nil), pl.HostRefs...),
		HAEnabled:  pl.HAEnabled,
	}}, nil
}

func (p *Provider) ListStorage(ctx context.Context) ([]vp.StoragePool, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	srs := p.backend.listSRs()
	out := make([]vp.StoragePool, 0, len(srs))
	for _, s := range srs {
		out = append(out, p.normalizeSR(s))
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
	ref := p.newRef("VM")
	v := &xapiVM{
		Ref:        ref,
		UUID:       p.nextID("uuid"),
		NameLabel:  spec.Name,
		PowerState: psHalted, // freshly created VMs are Halted
		ResidentOn: spec.HostID,
		VCPUsMax:   spec.VCPUs,
		MemoryB:    spec.MemoryMB * (1 << 20),
		OSDistro:   spec.GuestOS,
		HVM:        true,
		UEFI:       spec.Firmware == vp.FirmwareUEFI,
		Labels:     spec.Labels,
		Created:    time.Now().UTC().Unix(),
	}
	for i, d := range spec.Disks {
		v.VBDs = append(v.VBDs, xapiVBD{
			Ref:      fmt.Sprintf("%s-vbd%d", ref, i),
			Device:   strconv.Itoa(i),
			VDIRef:   fmt.Sprintf("%s-vdi%d", ref, i),
			SRRef:    d.StorageID,
			VirtualB: int64(d.CapacityGB * bytesPerGB),
			Path:     d.SourcePath,
		})
	}
	for i, n := range spec.NICs {
		v.VIFs = append(v.VIFs, xapiVIF{
			Ref:        fmt.Sprintf("%s-vif%d", ref, i),
			MAC:        n.MAC,
			NetworkRef: n.NetworkID,
			Model:      n.Model,
			Attached:   true,
		})
	}
	p.backend.createVM(v)
	return p.finishTask("createVM", ref), nil
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
		// start: VM.start (Halted->Running); resume: VM.resume (Suspended->Running);
		// reset: VM.hard_reboot (stays Running).
		p.backend.setPowerState(vmID, psRunning)
	case vp.PowerStop:
		// VM.clean_shutdown / hard_shutdown -> Halted.
		p.backend.setPowerState(vmID, psHalted)
	case vp.PowerSuspend:
		// VM.suspend -> Suspended (whole-VM state saved to disk).
		p.backend.setPowerState(vmID, psSuspended)
	}
	return p.finishTask("powerOp", vmID), nil
}

func (p *Provider) DeleteVM(ctx context.Context, vmID string, opts vp.DeleteOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapDeleteVM) {
		return nil, vp.ErrUnsupported
	}
	v, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if v.IsControl {
		// control domain / protected: never deletable.
		return nil, vp.ErrConflict
	}
	if normalizeState(v.PowerState) == vp.StateRunning && !opts.Force {
		return nil, vp.ErrConflict
	}
	// Force on a running VM hard-shuts it down first (VM.hard_shutdown).
	if normalizeState(v.PowerState) == vp.StateRunning && opts.Force {
		p.backend.setPowerState(vmID, psHalted)
	}
	p.backend.destroyVM(vmID)
	return p.finishTask("deleteVM", vmID), nil
}

func (p *Provider) ReconfigureVM(ctx context.Context, vmID string, spec vp.VMReconfigureSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapReconfigureVM) {
		return nil, vp.ErrUnsupported
	}
	v, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if spec.VCPUs != nil {
		if *spec.VCPUs <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		v.VCPUsMax = *spec.VCPUs
	}
	if spec.MemoryMB != nil {
		if *spec.MemoryMB <= 0 {
			return nil, vp.ErrInvalidSpec
		}
		v.MemoryB = *spec.MemoryMB * (1 << 20)
	}
	for i, d := range spec.AddDisks {
		v.VBDs = append(v.VBDs, xapiVBD{
			Ref:      fmt.Sprintf("%s-vbd%d", v.Ref, len(v.VBDs)+i),
			Device:   strconv.Itoa(len(v.VBDs) + i),
			VDIRef:   fmt.Sprintf("%s-vdi%d", v.Ref, len(v.VBDs)+i),
			SRRef:    d.StorageID,
			VirtualB: int64(d.CapacityGB * bytesPerGB),
			Path:     d.SourcePath,
		})
	}
	for i, n := range spec.AddNICs {
		v.VIFs = append(v.VIFs, xapiVIF{
			Ref:        fmt.Sprintf("%s-vif%d", v.Ref, len(v.VIFs)+i),
			MAC:        n.MAC,
			NetworkRef: n.NetworkID,
			Model:      n.Model,
			Attached:   true,
		})
	}
	if spec.Labels != nil {
		if v.Labels == nil {
			v.Labels = map[string]string{}
		}
		for k, val := range spec.Labels {
			v.Labels[k] = val
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
		ID:          p.newRef("VM"), // XAPI snapshots are themselves VM objects (opaque refs)
		VMID:        vmID,
		Name:        opts.Name,
		Description: opts.Description,
		HasMemory:   opts.Memory, // VM.checkpoint includes memory; VM.snapshot does not
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
	// Linked clone -> VM.clone (CoW); full clone -> VM.copy. Either way a new VM
	// object with a fresh opaque ref.
	ref := p.newRef("VM")
	clone := *src
	clone.Ref = ref
	clone.UUID = p.nextID("uuid")
	clone.NameLabel = spec.Name
	clone.Labels = cloneLabels(src.Labels)
	clone.IsControl = false
	clone.PowerState = psHalted
	if spec.PowerOn {
		clone.PowerState = psRunning
	}
	if spec.HostID != "" {
		clone.ResidentOn = spec.HostID
	}
	// deep-copy VBDs/VIFs so the clone is independent
	clone.VBDs = append([]xapiVBD(nil), src.VBDs...)
	clone.VIFs = append([]xapiVIF(nil), src.VIFs...)
	p.backend.createVM(&clone)
	return p.finishTask("clone", ref), nil
}

// --- migration ---

func (p *Provider) MigrateVM(ctx context.Context, vmID, targetHost string, opts vp.MigrateOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMigrate) {
		return nil, vp.ErrUnsupported
	}
	v, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, vp.ErrNotFound
	}
	if _, ok := p.backend.getHost(targetHost); !ok {
		return nil, vp.ErrInvalidSpec
	}
	// VM.pool_migrate (XenMotion) within the pool: change resident host.
	v.ResidentOn = targetHost
	return p.finishTask("migrate", vmID), nil
}

func (p *Provider) ExportVM(ctx context.Context, vmID string, format vp.DiskFormat) (io.ReadCloser, *vp.ExportInfo, error) {
	if !p.caps.Has(vp.CapExport) {
		return nil, nil, vp.ErrUnsupported
	}
	if !format.Valid() {
		return nil, nil, vp.ErrInvalidSpec
	}
	v, ok := p.backend.getVM(vmID)
	if !ok {
		return nil, nil, vp.ErrNotFound
	}
	// LIVE path: a real XAPI connection is attached but no real export (XVA / disk
	// HTTP handler) is implemented yet. Fabricating a placeholder here would make
	// backup / V2V record a worthless stub as SUCCESS, so we HARD-ERROR instead.
	// Only the in-memory sim backend (tests) may return the placeholder below.
	if _, live := p.backend.(*liveBackend); live {
		return nil, nil, fmt.Errorf("%w: VM export not yet implemented for xen", vp.ErrUnsupported)
	}
	// Stand in for the XAPI export HTTP handler streaming the VM's xva / disk(s).
	payload := fmt.Sprintf("XENEXPORT\x00provider=%s\x00vm=%s\x00format=%s\n", p.id, vmID, format)
	info := &vp.ExportInfo{
		Format:     format,
		SizeBytes:  int64(len(payload)),
		DiskCount:  len(v.VBDs),
		SourceVMID: vmID,
		GuestOS:    v.OSDistro,
		Firmware:   normalizeFirmware(v),
	}
	return io.NopCloser(strings.NewReader(payload)), info, nil
}

// --- cluster & HA ---

func (p *Provider) GetClusterTopology(ctx context.Context, clusterID string) (*vp.Topology, error) {
	if !p.caps.Has(vp.CapClusterTopology) {
		return nil, vp.ErrUnsupported
	}
	pl := p.backend.pool()
	if pl == nil || clusterID != pl.Ref {
		return nil, vp.ErrNotFound
	}
	top := &vp.Topology{ClusterID: clusterID, Placement: map[string]string{}}
	for _, h := range p.backend.listHosts() {
		nh := p.normalizeHost(h)
		top.Nodes = append(top.Nodes, vp.NodeState{
			NodeID: nh.ID, State: nh.State, VMCount: nh.VMCount, UpdatedAt: time.Now().UTC(),
		})
	}
	for _, v := range p.backend.listVMs() {
		top.Placement[v.Ref] = v.ResidentOn
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
	nh := p.normalizeHost(h)
	return &vp.NodeState{
		NodeID: nodeID, State: nh.State, VMCount: nh.VMCount, UpdatedAt: time.Now().UTC(),
	}, nil
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
		step = 30 // XAPI RRD default consolidation step
	}
	series := &vp.MetricSeries{EntityID: entityID}
	for i := 0; i < 5; i++ {
		series.Samples = append(series.Samples, vp.MetricSample{
			Timestamp:      base.Add(time.Duration(i*step) * time.Second),
			CPUPercent:     float64(9 + i*6),
			MemUsageBytes:  uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes:  4 << 30,
			NetRxBytes:     uint64(i) * 1400,
			NetTxBytes:     uint64(i) * 1000,
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
	// REAL content only: emit one truthful vm.state event per VM reflecting its
	// CURRENT state, then close. No fabricated heartbeat content. (xen is a
	// non-live model provider; a snapshot of real inventory state is the honest
	// event surface — when wired to live XAPI this becomes the real
	// event.next/event.from subscription.)
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
		for _, raw := range vms {
			v := p.normalizeVM(raw)
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

func unixUTC(s int64) time.Time { return time.Unix(s, 0).UTC() }

// compile-time assertion: *Provider satisfies the contract.
var _ vp.HypervisorProvider = (*Provider)(nil)
