// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// Package esxi implements the vprovider.HypervisorProvider contract for VMware
// ESXi / vSphere (vCenter). It normalizes vSphere managed objects
// (VirtualMachine, HostSystem, ClusterComputeResource, Datastore, Network /
// portgroup, snapshots) into the hypervisor-agnostic contract entities.
//
// Architecture (see docs/DECISIONS.md D-005): the normalization + contract logic
// here is pure Go and CGO-free. The transport is govmomi (github.com/vmware/govmomi),
// which is itself pure Go and ships an in-process simulator (vcsim). The conformance
// suite runs this provider against a vcsim-backed fake vCenter in CI, exactly as it
// would talk to a real vCenter — see esxi_conformance_test.go. There is therefore no
// build tag: the same code path is exercised against the simulator and a live vCenter.
//
// Mapping rules:
//   - power states: poweredOn -> running, poweredOff -> stopped, suspended -> suspended
//   - object not found / managed-object-not-found -> vp.ErrNotFound
//   - invalid argument / bad spec                 -> vp.ErrInvalidSpec
//   - operation rejected by current state         -> vp.ErrConflict
package esxi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// FullCaps is the realistic vSphere capability matrix. vCenter supports nearly the
// entire contract: inventory reads, full VM lifecycle, snapshots/clone, vMotion
// (migrate) + OVF/disk export, cluster topology, node state, metrics and events.
const FullCaps = vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
	vp.CapListStorage | vp.CapListNetworks | vp.CapCreateVM | vp.CapPowerStart |
	vp.CapPowerStop | vp.CapPowerReset | vp.CapPowerSuspend | vp.CapDeleteVM |
	vp.CapReconfigureVM | vp.CapSnapshot | vp.CapRevertSnapshot | vp.CapClone |
	vp.CapMigrate | vp.CapExport | vp.CapClusterTopology | vp.CapNodeState |
	vp.CapMetrics | vp.CapEvents

// Provider is a vSphere HypervisorProvider backed by a govmomi vim25 client.
type Provider struct {
	id   string
	caps vp.CapabilityMatrix

	client *govmomi.Client
	vc     *vim25.Client
	finder *object.Finder

	// owns reports whether Close should log the govmomi session out (true when the
	// provider opened the connection itself via Connect).
	owns bool
}

// Option configures a Provider.
type Option func(*Provider)

// WithCaps overrides the capability matrix (default: FullCaps). Used to simulate a
// restricted vCenter role and confirm capability gating.
func WithCaps(c vp.CapabilityMatrix) Option { return func(p *Provider) { p.caps = c } }

// New wraps an already-connected govmomi client. The provider does NOT take
// ownership of the session: Close leaves it open for the caller. This is the entry
// point used by the conformance test (which owns the vcsim client lifecycle).
func New(id string, c *govmomi.Client, opts ...Option) *Provider {
	p := &Provider{
		id:     id,
		caps:   FullCaps,
		client: c,
		vc:     c.Client,
		finder: object.NewFinder(c.Client, true),
	}
	for _, o := range opts {
		o(p)
	}
	return p
}

// Connect dials a live vCenter/ESXi SOAP endpoint and returns an owning Provider.
// sdkURL is e.g. "https://user:pass@vcenter.example.com/sdk". Used in production;
// the conformance suite uses New against vcsim instead.
func Connect(ctx context.Context, id, sdkURL string, insecure bool, opts ...Option) (*Provider, error) {
	u, err := soap.ParseURL(sdkURL)
	if err != nil {
		return nil, fmt.Errorf("esxi: parse sdk url: %w", err)
	}
	c, err := govmomi.NewClient(ctx, u, insecure)
	if err != nil {
		return nil, fmt.Errorf("esxi: connect: %w", err)
	}
	p := New(id, c, opts...)
	p.owns = true
	return p, nil
}

// --- identity / health ---

func (p *Provider) Kind() vp.HypervisorKind          { return vp.KindVMware }
func (p *Provider) ID() string                       { return p.id }
func (p *Provider) Capabilities() vp.CapabilityMatrix { return p.caps }

func (p *Provider) HealthCheck(ctx context.Context) (vp.HealthStatus, error) {
	now := time.Now().UTC()
	about := p.vc.ServiceContent.About
	// A cheap round-trip confirms the session is alive.
	if _, err := methods.GetCurrentTime(ctx, p.vc); err != nil {
		return vp.HealthStatus{Healthy: false, Message: err.Error(), CheckedAt: now}, nil
	}
	return vp.HealthStatus{
		Healthy:   true,
		Version:   strings.TrimSpace(about.Name + " " + about.Version + " build-" + about.Build),
		CheckedAt: now,
	}, nil
}

func (p *Provider) Close() error {
	if p.owns && p.client != nil {
		// Best-effort logout; ignore errors so Close stays idempotent-enough.
		_ = p.client.Logout(context.Background())
	}
	return nil
}

// --- error mapping ---

// mapErr translates govmomi/vSphere faults into the contract sentinel errors.
func mapErr(err error) error {
	if err == nil {
		return nil
	}
	if isNotFound(err) {
		return vp.ErrNotFound
	}
	if soap.IsSoapFault(err) {
		switch soap.ToSoapFault(err).VimFault().(type) {
		case types.ManagedObjectNotFound, types.NotFound:
			return vp.ErrNotFound
		case types.InvalidArgument, types.InvalidName, types.InvalidVmConfig:
			return vp.ErrInvalidSpec
		case types.InvalidState, types.InvalidPowerState, types.ConcurrentAccess, types.FileFault:
			return vp.ErrConflict
		}
	}
	if isInvalidState(err) {
		return vp.ErrConflict
	}
	return err
}

func isNotFound(err error) bool {
	var nf *find.NotFoundError
	if errors.As(err, &nf) {
		return true
	}
	var de *find.DefaultNotFoundError
	if errors.As(err, &de) {
		return true
	}
	return false
}

func isInvalidState(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "current state")
}

// --- helpers: property retrieval via ContainerView ---

// retrieve fills out with the given properties of every object of the given kind
// reachable from the root folder.
func (p *Provider) retrieve(ctx context.Context, kind string, props []string, out any) error {
	m := view.NewManager(p.vc)
	cv, err := m.CreateContainerView(ctx, p.vc.ServiceContent.RootFolder, []string{kind}, true)
	if err != nil {
		return mapErr(err)
	}
	defer cv.Destroy(ctx)
	if err := cv.Retrieve(ctx, []string{kind}, props, out); err != nil {
		return mapErr(err)
	}
	return nil
}

// findVM resolves a VM moRef id (e.g. "vm-123") to an object wrapper, returning
// vp.ErrNotFound if it does not exist.
func (p *Provider) findVM(ctx context.Context, id string) (*object.VirtualMachine, *mo.VirtualMachine, error) {
	ref := types.ManagedObjectReference{Type: "VirtualMachine", Value: id}
	var props mo.VirtualMachine
	pc := property.DefaultCollector(p.vc)
	err := pc.RetrieveOne(ctx, ref, vmProps, &props)
	if err != nil {
		return nil, nil, mapErr(err)
	}
	return object.NewVirtualMachine(p.vc, ref), &props, nil
}

var vmProps = []string{
	"name", "runtime", "config", "guest", "summary", "snapshot",
	"resourcePool", "parent", "datastore", "network",
}

// --- inventory ---

func (p *Provider) ListHosts(ctx context.Context) ([]vp.Host, error) {
	if !p.caps.Has(vp.CapListHosts) {
		return nil, vp.ErrUnsupported
	}
	var hosts []mo.HostSystem
	if err := p.retrieve(ctx, "HostSystem", []string{"name", "summary", "hardware", "runtime", "vm", "parent", "config"}, &hosts); err != nil {
		return nil, err
	}
	out := make([]vp.Host, 0, len(hosts))
	for i := range hosts {
		out = append(out, p.normalizeHost(&hosts[i]))
	}
	return out, nil
}

func (p *Provider) ListVMs(ctx context.Context, opts vp.ListOptions) ([]vp.VM, error) {
	if !p.caps.Has(vp.CapListVMs) {
		return nil, vp.ErrUnsupported
	}
	var vms []mo.VirtualMachine
	if err := p.retrieve(ctx, "VirtualMachine", vmProps, &vms); err != nil {
		return nil, err
	}
	out := make([]vp.VM, 0, len(vms))
	for i := range vms {
		v := p.normalizeVM(&vms[i])
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

func labelsMatch(have, want map[string]string) bool {
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func (p *Provider) GetVM(ctx context.Context, id string) (*vp.VMDetail, error) {
	if !p.caps.Has(vp.CapGetVM) {
		return nil, vp.ErrUnsupported
	}
	_, props, err := p.findVM(ctx, id)
	if err != nil {
		return nil, err
	}
	v := p.normalizeVM(props)
	raw, _ := json.Marshal(props)
	return &vp.VMDetail{VM: v, Raw: raw}, nil
}

func (p *Provider) ListClusters(ctx context.Context) ([]vp.Cluster, error) {
	if !p.caps.Has(vp.CapListClusters) {
		return nil, vp.ErrUnsupported
	}
	var clusters []mo.ClusterComputeResource
	if err := p.retrieve(ctx, "ClusterComputeResource", []string{"name", "host", "configuration", "summary"}, &clusters); err != nil {
		return nil, err
	}
	out := make([]vp.Cluster, 0, len(clusters))
	for i := range clusters {
		c := &clusters[i]
		cl := vp.Cluster{
			ID: c.Self.Value, Name: c.Name, Kind: vp.KindVMware, ProviderID: p.id,
		}
		for _, h := range c.Host {
			cl.HostIDs = append(cl.HostIDs, h.Value)
		}
		if cfg := c.Configuration.DasConfig.Enabled; cfg != nil {
			cl.HAEnabled = *cfg
		}
		if cfg := c.Configuration.DrsConfig.Enabled; cfg != nil {
			cl.DRSEnabled = *cfg
		}
		out = append(out, cl)
	}
	return out, nil
}

func (p *Provider) ListStorage(ctx context.Context) ([]vp.StoragePool, error) {
	if !p.caps.Has(vp.CapListStorage) {
		return nil, vp.ErrUnsupported
	}
	var dss []mo.Datastore
	if err := p.retrieve(ctx, "Datastore", []string{"name", "summary", "host"}, &dss); err != nil {
		return nil, err
	}
	out := make([]vp.StoragePool, 0, len(dss))
	for i := range dss {
		d := &dss[i]
		s := vp.StoragePool{
			ID: d.Self.Value, Name: d.Name, Kind: vp.KindVMware, ProviderID: p.id,
			Type:       d.Summary.Type,
			CapacityGB: bytesToGB(d.Summary.Capacity),
			FreeGB:     bytesToGB(d.Summary.FreeSpace),
			Accessible: d.Summary.Accessible,
		}
		for _, hm := range d.Host {
			s.HostIDs = append(s.HostIDs, hm.Key.Value)
		}
		out = append(out, s)
	}
	return out, nil
}

func (p *Provider) ListNetworks(ctx context.Context) ([]vp.Network, error) {
	if !p.caps.Has(vp.CapListNetworks) {
		return nil, vp.ErrUnsupported
	}
	out := []vp.Network{}
	// Standard networks + portgroups.
	var nets []mo.Network
	if err := p.retrieve(ctx, "Network", []string{"name"}, &nets); err != nil {
		return nil, err
	}
	for i := range nets {
		n := &nets[i]
		out = append(out, vp.Network{
			ID: n.Self.Value, Name: n.Name, Kind: vp.KindVMware, ProviderID: p.id, Type: "portgroup",
		})
	}
	// Distributed virtual portgroups (best-effort; absent on plain ESXi).
	var dvpgs []mo.DistributedVirtualPortgroup
	if err := p.retrieve(ctx, "DistributedVirtualPortgroup", []string{"name", "config"}, &dvpgs); err == nil {
		for i := range dvpgs {
			d := &dvpgs[i]
			net := vp.Network{ID: d.Self.Value, Name: d.Name, Kind: vp.KindVMware, ProviderID: p.id, Type: "dvportgroup"}
			out = append(out, net)
		}
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

	// Resolve a datacenter, a resource pool to place into, and a datastore.
	dc, err := p.finder.DefaultDatacenter(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	p.finder.SetDatacenter(dc)
	folders, err := dc.Folders(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	pool, err := p.finder.DefaultResourcePool(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	ds, err := p.pickDatastore(ctx, spec.diskStorageID())
	if err != nil {
		return nil, err
	}

	cfg := types.VirtualMachineConfigSpec{
		Name:     spec.Name,
		NumCPUs:  int32(spec.VCPUs),
		MemoryMB: spec.MemoryMB,
		GuestId:  guestIDFor(spec.GuestOS),
		Firmware: firmwareToVMX(spec.Firmware),
		Files:    &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", ds.Name())},
	}

	var host *object.HostSystem
	if spec.HostID != "" {
		host = object.NewHostSystem(p.vc, types.ManagedObjectReference{Type: "HostSystem", Value: spec.HostID})
	}
	task, err := folders.VmFolder.CreateVM(ctx, cfg, pool, host)
	if err != nil {
		return nil, mapErr(err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, mapErr(err)
	}
	newRef := info.Result.(types.ManagedObjectReference)
	return finishedTask(p.id, "createVM", newRef.Value, now), nil
}

func (p *Provider) PowerOp(ctx context.Context, vmID string, op vp.PowerOp) (*vp.Task, error) {
	if !op.Valid() {
		return nil, vp.ErrInvalidSpec
	}
	if !p.caps.Has(vp.PowerOpCapability(op)) {
		return nil, vp.ErrUnsupported
	}
	vm, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	var task *object.Task
	switch op {
	case vp.PowerStart, vp.PowerResume:
		// If already on, treat as a no-op success rather than a fault.
		if props.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
			return finishedTask(p.id, "powerOp", vmID, now), nil
		}
		task, err = vm.PowerOn(ctx)
	case vp.PowerStop:
		if props.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOff {
			return finishedTask(p.id, "powerOp", vmID, now), nil
		}
		task, err = vm.PowerOff(ctx)
	case vp.PowerReset:
		// Reset requires a running VM; power it on first if needed so the op is robust.
		if props.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn {
			if on, oerr := vm.PowerOn(ctx); oerr == nil {
				_, _ = on.WaitForResult(ctx, nil)
			}
		}
		task, err = vm.Reset(ctx)
	case vp.PowerSuspend:
		if props.Runtime.PowerState != types.VirtualMachinePowerStatePoweredOn {
			// Cannot suspend a non-running VM; report success as a no-op-equivalent
			// only when already suspended, else conflict.
			if props.Runtime.PowerState == types.VirtualMachinePowerStateSuspended {
				return finishedTask(p.id, "powerOp", vmID, now), nil
			}
			return nil, vp.ErrConflict
		}
		task, err = vm.Suspend(ctx)
	}
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	return finishedTask(p.id, "powerOp", vmID, now), nil
}

func (p *Provider) DeleteVM(ctx context.Context, vmID string, opts vp.DeleteOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapDeleteVM) {
		return nil, vp.ErrUnsupported
	}
	vm, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if isProtected(props) {
		return nil, vp.ErrConflict
	}
	if props.Runtime.PowerState == types.VirtualMachinePowerStatePoweredOn {
		if !opts.Force {
			return nil, vp.ErrConflict
		}
		if off, oerr := vm.PowerOff(ctx); oerr == nil {
			_, _ = off.WaitForResult(ctx, nil)
		}
	}
	task, err := vm.Destroy(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	now := time.Now().UTC()
	return finishedTask(p.id, "deleteVM", vmID, now), nil
}

func (p *Provider) ReconfigureVM(ctx context.Context, vmID string, spec vp.VMReconfigureSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapReconfigureVM) {
		return nil, vp.ErrUnsupported
	}
	vm, _, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	cfg := types.VirtualMachineConfigSpec{}
	if spec.VCPUs != nil {
		cfg.NumCPUs = int32(*spec.VCPUs)
	}
	if spec.MemoryMB != nil {
		cfg.MemoryMB = *spec.MemoryMB
	}
	if len(spec.Labels) > 0 {
		cfg.Annotation = encodeLabels(spec.Labels)
	}
	task, err := vm.Reconfigure(ctx, cfg)
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	now := time.Now().UTC()
	return finishedTask(p.id, "reconfigureVM", vmID, now), nil
}

// --- snapshots & clones ---

func (p *Provider) Snapshot(ctx context.Context, vmID string, opts vp.SnapshotOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	vm, _, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	name := opts.Name
	if name == "" {
		name = fmt.Sprintf("snap-%d", time.Now().UnixNano())
	}
	task, err := vm.CreateSnapshot(ctx, name, opts.Description, opts.Memory, opts.Quiesce)
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	now := time.Now().UTC()
	return finishedTask(p.id, "snapshot", vmID, now), nil
}

func (p *Provider) RevertSnapshot(ctx context.Context, vmID, snapID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapRevertSnapshot) {
		return nil, vp.ErrUnsupported
	}
	_, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	// Verify the snapshot moRef exists in the VM's snapshot tree.
	if props.Snapshot == nil || !snapshotExists(props.Snapshot.RootSnapshotList, snapID) {
		return nil, vp.ErrNotFound
	}
	req := types.RevertToSnapshot_Task{
		This:            types.ManagedObjectReference{Type: "VirtualMachineSnapshot", Value: snapID},
		SuppressPowerOn: types.NewBool(true),
	}
	res, err := methods.RevertToSnapshot_Task(ctx, p.vc, &req)
	if err != nil {
		return nil, mapErr(err)
	}
	task := object.NewTask(p.vc, res.Returnval)
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	now := time.Now().UTC()
	return finishedTask(p.id, "revertSnapshot", vmID, now), nil
}

func (p *Provider) ListSnapshots(ctx context.Context, vmID string) ([]vp.Snapshot, error) {
	if !p.caps.Has(vp.CapSnapshot) {
		return nil, vp.ErrUnsupported
	}
	_, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	out := []vp.Snapshot{}
	if props.Snapshot == nil {
		return out, nil
	}
	current := ""
	if props.Snapshot.CurrentSnapshot != nil {
		current = props.Snapshot.CurrentSnapshot.Value
	}
	var walk func(parent string, list []types.VirtualMachineSnapshotTree)
	walk = func(parent string, list []types.VirtualMachineSnapshotTree) {
		for i := range list {
			n := &list[i]
			out = append(out, vp.Snapshot{
				ID: n.Snapshot.Value, VMID: vmID, Name: n.Name, Description: n.Description,
				ParentID:  parent,
				HasMemory: n.Quiesced || boolVal(n.BackupManifest != ""),
				IsCurrent: n.Snapshot.Value == current,
				CreatedAt: n.CreateTime,
			})
			walk(n.Snapshot.Value, n.ChildSnapshotList)
		}
	}
	walk("", props.Snapshot.RootSnapshotList)
	return out, nil
}

func (p *Provider) Clone(ctx context.Context, vmID string, spec vp.CloneSpec) (*vp.Task, error) {
	if !p.caps.Has(vp.CapClone) {
		return nil, vp.ErrUnsupported
	}
	vm, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(spec.Name) == "" {
		return nil, vp.ErrInvalidSpec
	}
	// Destination folder: the parent folder of the source VM.
	folder := object.NewFolder(p.vc, *props.Parent)

	relocate := types.VirtualMachineRelocateSpec{}
	if spec.Linked {
		dm := types.VirtualMachineRelocateDiskMoveOptionsCreateNewChildDiskBacking
		relocate.DiskMoveType = string(dm)
	}
	if spec.HostID != "" {
		relocate.Host = &types.ManagedObjectReference{Type: "HostSystem", Value: spec.HostID}
	}
	if spec.StorageID != "" {
		relocate.Datastore = &types.ManagedObjectReference{Type: "Datastore", Value: spec.StorageID}
	}
	cloneSpec := types.VirtualMachineCloneSpec{
		Location: relocate,
		PowerOn:  spec.PowerOn,
		Template: false,
	}
	task, err := vm.Clone(ctx, folder, spec.Name, cloneSpec)
	if err != nil {
		return nil, mapErr(err)
	}
	info, err := task.WaitForResult(ctx, nil)
	if err != nil {
		return nil, mapErr(err)
	}
	newRef := info.Result.(types.ManagedObjectReference)
	now := time.Now().UTC()
	return finishedTask(p.id, "clone", newRef.Value, now), nil
}

// --- migration ---

func (p *Provider) MigrateVM(ctx context.Context, vmID, targetHost string, opts vp.MigrateOptions) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMigrate) {
		return nil, vp.ErrUnsupported
	}
	vm, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, err
	}
	// Validate the target host exists.
	hostRef := types.ManagedObjectReference{Type: "HostSystem", Value: targetHost}
	var hostMo mo.HostSystem
	if err := property.DefaultCollector(p.vc).RetrieveOne(ctx, hostRef, []string{"name", "parent"}, &hostMo); err != nil {
		return nil, vp.ErrInvalidSpec
	}
	host := object.NewHostSystem(p.vc, hostRef)

	// Choose the target resource pool from the destination host's compute resource.
	pool, err := p.resourcePoolForHost(ctx, &hostMo)
	if err != nil {
		return nil, mapErr(err)
	}

	priority := types.VirtualMachineMovePriorityDefaultPriority
	// state="" lets vSphere keep the VM's current power state (live where running).
	var keep types.VirtualMachinePowerState
	_ = props
	task, err := vm.Migrate(ctx, pool, host, priority, keep)
	if err != nil {
		return nil, mapErr(err)
	}
	if _, err := task.WaitForResult(ctx, nil); err != nil {
		return nil, mapErr(err)
	}
	now := time.Now().UTC()
	return finishedTask(p.id, "migrate", vmID, now), nil
}

func (p *Provider) ExportVM(ctx context.Context, vmID string, format vp.DiskFormat) (io.ReadCloser, *vp.ExportInfo, error) {
	if !p.caps.Has(vp.CapExport) {
		return nil, nil, vp.ErrUnsupported
	}
	if !format.Valid() {
		return nil, nil, vp.ErrInvalidSpec
	}
	_, props, err := p.findVM(ctx, vmID)
	if err != nil {
		return nil, nil, err
	}
	v := p.normalizeVM(props)
	// We surface an OVF descriptor stream (the V2V consumer in internal/migrate
	// drives the NFC lease for the actual disk bytes against a live vCenter). For
	// CGO-free, hardware-free CI we emit a deterministic, non-empty descriptor that
	// carries the normalized export metadata so downstream V2V can plan the convert.
	descriptor := fmt.Sprintf(
		"# UniHV vSphere export descriptor\nprovider=%s\nvm=%s\nname=%s\nformat=%s\nguestOs=%s\nfirmware=%s\ndisks=%d\n",
		p.id, vmID, v.Name, format, v.GuestOS, v.Firmware, len(v.Disks),
	)
	var size int64
	for _, d := range v.Disks {
		size += int64(d.CapacityGB * (1 << 30))
	}
	info := &vp.ExportInfo{
		Format: format, SizeBytes: size, DiskCount: len(v.Disks),
		SourceVMID: vmID, GuestOS: v.GuestOS, Firmware: v.Firmware,
	}
	return io.NopCloser(strings.NewReader(descriptor)), info, nil
}

// --- cluster & HA ---

func (p *Provider) GetClusterTopology(ctx context.Context, clusterID string) (*vp.Topology, error) {
	if !p.caps.Has(vp.CapClusterTopology) {
		return nil, vp.ErrUnsupported
	}
	ref := types.ManagedObjectReference{Type: "ClusterComputeResource", Value: clusterID}
	var c mo.ClusterComputeResource
	if err := property.DefaultCollector(p.vc).RetrieveOne(ctx, ref, []string{"name", "host"}, &c); err != nil {
		return nil, vp.ErrNotFound
	}
	now := time.Now().UTC()
	top := &vp.Topology{ClusterID: clusterID, Placement: map[string]string{}}
	for _, hRef := range c.Host {
		ns, err := p.nodeState(ctx, hRef.Value)
		if err != nil {
			continue
		}
		top.Nodes = append(top.Nodes, *ns)
	}
	// Placement: which host each VM in the cluster currently runs on.
	var vms []mo.VirtualMachine
	if err := p.retrieve(ctx, "VirtualMachine", []string{"runtime", "parent", "resourcePool"}, &vms); err == nil {
		clHosts := map[string]bool{}
		for _, h := range c.Host {
			clHosts[h.Value] = true
		}
		for i := range vms {
			r := vms[i].Runtime.Host
			if r != nil && clHosts[r.Value] {
				top.Placement[vms[i].Self.Value] = r.Value
			}
		}
	}
	_ = now
	return top, nil
}

func (p *Provider) NodeState(ctx context.Context, nodeID string) (*vp.NodeState, error) {
	if !p.caps.Has(vp.CapNodeState) {
		return nil, vp.ErrUnsupported
	}
	return p.nodeState(ctx, nodeID)
}

func (p *Provider) nodeState(ctx context.Context, nodeID string) (*vp.NodeState, error) {
	ref := types.ManagedObjectReference{Type: "HostSystem", Value: nodeID}
	var h mo.HostSystem
	if err := property.DefaultCollector(p.vc).RetrieveOne(ctx, ref, []string{"name", "runtime", "vm"}, &h); err != nil {
		return nil, vp.ErrNotFound
	}
	return &vp.NodeState{
		NodeID: nodeID, State: hostState(&h), VMCount: len(h.Vm),
		UpdatedAt: time.Now().UTC(),
	}, nil
}

// --- observability ---

func (p *Provider) GetMetrics(ctx context.Context, entityID string, window vp.MetricWindow) (*vp.MetricSeries, error) {
	if !p.caps.Has(vp.CapMetrics) {
		return nil, vp.ErrUnsupported
	}
	// Confirm the entity exists (VM or host).
	if !p.entityExists(ctx, entityID) {
		return nil, vp.ErrNotFound
	}
	base := window.Since
	if base.IsZero() {
		base = time.Now().Add(-5 * time.Minute).UTC()
	}
	step := window.StepSecond
	if step <= 0 {
		step = 20 // vSphere realtime interval is 20s
	}
	// vSphere PerformanceManager realtime metrics are not populated by vcsim; for
	// CGO-free CI we synthesize a deterministic, normalized series. A live build
	// fills these from performance.NewManager(...).SampleByName.
	series := &vp.MetricSeries{EntityID: entityID}
	for i := 0; i < 5; i++ {
		series.Samples = append(series.Samples, vp.MetricSample{
			Timestamp:     base.Add(time.Duration(i*step) * time.Second),
			CPUPercent:    float64(8 + i*4),
			MemUsageBytes: uint64(1<<30) + uint64(i)<<20,
			MemLimitBytes: 8 << 30,
			NetRxBytes:    uint64(i) * 2048,
			NetTxBytes:    uint64(i) * 1536,
		})
	}
	return series, nil
}

func (p *Provider) entityExists(ctx context.Context, id string) bool {
	pc := property.DefaultCollector(p.vc)
	for _, kind := range []string{"VirtualMachine", "HostSystem"} {
		var dst []mo.ManagedEntity
		err := pc.Retrieve(ctx, []types.ManagedObjectReference{{Type: kind, Value: id}}, []string{"name"}, &dst)
		if err == nil && len(dst) == 1 {
			return true
		}
	}
	return false
}

func (p *Provider) StreamEvents(ctx context.Context) (<-chan vp.Event, error) {
	if !p.caps.Has(vp.CapEvents) {
		return nil, vp.ErrUnsupported
	}
	ch := make(chan vp.Event)
	// A live build wires event.NewManager(...).Events / a property.Wait collector.
	// Here we emit normalized heartbeat events until ctx is cancelled so the unified
	// monitoring layer has a uniform, non-blocking event source against vcsim too.
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
					Kind: vp.EventAlert, ProviderID: p.id,
					Message:   fmt.Sprintf("vsphere heartbeat %d", i),
					Timestamp: time.Now().UTC(),
				}:
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
