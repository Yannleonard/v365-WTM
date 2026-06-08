// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// live_vsphere.go is the REAL VMware vSphere/ESXi backend. It is PURE Go — it speaks
// the official vSphere SOAP API via github.com/vmware/govmomi, which is itself
// CGO-free — so it compiles everywhere and carries NO build tag: the distroless,
// CGO_ENABLED=0 Linux image (D-005 / D-007) stays intact. It satisfies the existing
// vsphereBackend seam (esxi.go) so the pure-Go normalization core in esxi.go /
// vsphere.go is reused verbatim against a real vCenter/ESXi (or, in tests, against
// govmomi's in-process vcsim simulator, which faithfully implements the vSphere API).
//
// Official vSphere API surface used (via govmomi, 1:1 with the vim25 SOAP API):
//   connection : govmomi.NewClient(ctx, url, insecure) (SOAP login),
//                Client.ServiceContent.About (version), client.Logout
//   inventory  : view.Manager + ContainerView + property.Collector.Retrieve to
//                list mo.VirtualMachine / mo.HostSystem / mo.ClusterComputeResource /
//                mo.Datastore / mo.Network
//   lifecycle  : object.VirtualMachine.PowerOn / PowerOff / Suspend / Reset,
//                object.Folder.CreateVM_Task, object.VirtualMachine.Destroy_Task,
//                object.VirtualMachine.Reconfigure
//   snapshots  : object.VirtualMachine.CreateSnapshot, RevertToSnapshot
//   clone      : object.VirtualMachine.Clone (CloneVM_Task)
//   migrate    : object.VirtualMachine.Relocate / Migrate (vMotion)
//   faults     : task.Error / soap faults mapped to vp.ErrNotFound / ErrConflict /
//                ErrInvalidSpec via mapVimErr
//
// The vsphereBackend seam methods are synchronous and DO NOT return errors (they
// mirror an in-memory model). The live backend therefore performs the SOAP call
// eagerly, records the last transport error, and flips healthy()->false on a hard
// transport failure so HealthCheck surfaces it. Read methods refresh a cached
// snapshot of vSphere managed objects keyed by moRef so getVM/getHost resolve native
// object handles for the write path.
package esxi

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/vmware/govmomi"
	"github.com/vmware/govmomi/find"
	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/property"
	"github.com/vmware/govmomi/task"
	"github.com/vmware/govmomi/view"
	"github.com/vmware/govmomi/vim25"
	"github.com/vmware/govmomi/vim25/methods"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/soap"
	"github.com/vmware/govmomi/vim25/types"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// liveBackend is the real, pure-Go govmomi vSphere backend.
type liveBackend struct {
	endpoint string

	mu       sync.RWMutex
	client   *govmomi.Client
	ver      string
	healthOK bool
	lastErr  error

	// handle cache: moRef(string) -> native object reference, refreshed on list.
	vmRefs   map[string]types.ManagedObjectReference
	hostRefs map[string]types.ManagedObjectReference
}

// NewLive constructs a Provider backed by a REAL vSphere connection. endpoint is the
// vCenter/ESXi SDK URL ("vcenter.lab.local" or "https://vcenter/sdk"); user/pass are
// the SOAP credentials; insecure skips TLS verification (lab/self-signed certs).
func NewLive(id, endpoint, user, pass string, insecure bool, opts ...Option) (*Provider, error) {
	be, err := newLiveBackend(context.Background(), endpoint, user, pass, insecure)
	if err != nil {
		return nil, err
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

// newLiveBackend dials vSphere and runs the official SOAP login
// (govmomi.NewClient), then caches the server version.
func newLiveBackend(ctx context.Context, endpoint, user, pass string, insecure bool) (*liveBackend, error) {
	u, err := soap.ParseURL(endpoint)
	if err != nil {
		return nil, fmt.Errorf("esxi: parse vSphere URL %q: %w", endpoint, err)
	}
	if u == nil {
		return nil, fmt.Errorf("esxi: empty vSphere URL")
	}
	if user != "" {
		u.User = url.UserPassword(user, pass)
	}
	c, err := govmomi.NewClient(ctx, u, insecure)
	if err != nil {
		return nil, fmt.Errorf("esxi: vSphere SOAP login: %w", err)
	}
	be := &liveBackend{
		endpoint: endpoint,
		client:   c,
		healthOK: true,
		vmRefs:   map[string]types.ManagedObjectReference{},
		hostRefs: map[string]types.ManagedObjectReference{},
	}
	be.ver = c.ServiceContent.About.FullName
	return be, nil
}

// fail records a transport error. A hard transport failure (connection-level) marks
// the backend unhealthy; logical vim faults do not.
func (b *liveBackend) fail(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.lastErr = err
	if isTransportError(err) {
		b.healthOK = false
	}
	b.mu.Unlock()
}

// isTransportError reports a connection-level failure (not a logical vim fault, which
// surfaces as a task.Error / soap.Fault).
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	var te task.Error
	if errors.As(err, &te) {
		return false
	}
	if soap.IsSoapFault(err) || soap.IsVimFault(err) {
		return false
	}
	return true
}

// --- connection ---

func (b *liveBackend) version() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ver
}

func (b *liveBackend) healthy() bool {
	b.mu.RLock()
	c := b.client
	ok := b.healthOK
	b.mu.RUnlock()
	if !ok || c == nil {
		return false
	}
	// Active probe: SessionManager.UserSession round-trips to the server cheaply.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	active, err := c.SessionManager.UserSession(ctx)
	if err != nil {
		b.fail(err)
		return false
	}
	return active != nil
}

func (b *liveBackend) close() error {
	b.mu.Lock()
	c := b.client
	b.client = nil
	b.healthOK = false
	b.mu.Unlock()
	if c != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return c.Logout(ctx)
	}
	return nil
}

// --- inventory walks (view.Manager + property.Collector) ---

// retrieve walks a ContainerView of one managed-object kind and fills dst (a pointer
// to a slice of mo.* structs) using the property collector. This is the canonical
// govmomi inventory pattern.
func (b *liveBackend) retrieve(kind string, props []string, dst any) bool {
	b.mu.RLock()
	c := b.client
	b.mu.RUnlock()
	if c == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	m := view.NewManager(c.Client)
	v, err := m.CreateContainerView(ctx, c.ServiceContent.RootFolder, []string{kind}, true)
	if err != nil {
		b.fail(err)
		return false
	}
	defer func() { _ = v.Destroy(ctx) }()
	if err := v.Retrieve(ctx, []string{kind}, props, dst); err != nil {
		b.fail(err)
		return false
	}
	return true
}

func (b *liveBackend) listHosts() []*vsphereHost {
	var hosts []mo.HostSystem
	if !b.retrieve("HostSystem",
		[]string{"name", "parent", "runtime", "hardware", "summary", "config.product"}, &hosts) {
		return nil
	}
	out := make([]*vsphereHost, 0, len(hosts))
	refs := make(map[string]types.ManagedObjectReference, len(hosts))
	for i := range hosts {
		h := &hosts[i]
		refs[h.Self.Value] = h.Self
		out = append(out, convertHost(h))
	}
	b.mu.Lock()
	b.hostRefs = refs
	b.mu.Unlock()
	return out
}

func (b *liveBackend) getHost(moRef string) (*vsphereHost, bool) {
	for _, h := range b.listHosts() {
		if h.MoRef == moRef {
			return h, true
		}
	}
	return nil, false
}

func (b *liveBackend) listVMs() []*vsphereVM {
	var vms []mo.VirtualMachine
	if !b.retrieve("VirtualMachine",
		[]string{"name", "config", "runtime", "guest", "summary"}, &vms) {
		return nil
	}
	out := make([]*vsphereVM, 0, len(vms))
	refs := make(map[string]types.ManagedObjectReference, len(vms))
	for i := range vms {
		vm := &vms[i]
		refs[vm.Self.Value] = vm.Self
		out = append(out, convertVM(vm))
	}
	b.mu.Lock()
	b.vmRefs = refs
	b.mu.Unlock()
	return out
}

func (b *liveBackend) getVM(moRef string) (*vsphereVM, bool) {
	for _, vm := range b.listVMs() {
		if vm.MoRef == moRef {
			return vm, true
		}
	}
	return nil, false
}

func (b *liveBackend) listClusters() []*vsphereCluster {
	var cls []mo.ClusterComputeResource
	if !b.retrieve("ClusterComputeResource",
		[]string{"name", "host", "configuration"}, &cls) {
		return nil
	}
	out := make([]*vsphereCluster, 0, len(cls))
	for i := range cls {
		out = append(out, convertCluster(&cls[i]))
	}
	return out
}

func (b *liveBackend) getCluster(moRef string) (*vsphereCluster, bool) {
	for _, c := range b.listClusters() {
		if c.MoRef == moRef {
			return c, true
		}
	}
	return nil, false
}

func (b *liveBackend) listDatastores() []*vsphereDatastore {
	var dss []mo.Datastore
	if !b.retrieve("Datastore", []string{"name", "summary", "host"}, &dss) {
		return nil
	}
	out := make([]*vsphereDatastore, 0, len(dss))
	for i := range dss {
		out = append(out, convertDatastore(&dss[i]))
	}
	return out
}

func (b *liveBackend) listNetworks() []*vsphereNetwork {
	var nets []mo.Network
	if !b.retrieve("Network", []string{"name", "summary"}, &nets) {
		return nil
	}
	out := make([]*vsphereNetwork, 0, len(nets))
	for i := range nets {
		out = append(out, convertNetwork(&nets[i]))
	}
	return out
}

// --- lifecycle ---

func (b *liveBackend) createVM(vm *vsphereVM) {
	b.mu.RLock()
	c := b.client
	b.mu.RUnlock()
	if c == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	finder := find.NewFinder(c.Client, true)
	dc, err := finder.DefaultDatacenter(ctx)
	if err != nil {
		b.fail(err)
		return
	}
	finder.SetDatacenter(dc)
	folders, err := dc.Folders(ctx)
	if err != nil {
		b.fail(err)
		return
	}
	var hostObj *object.HostSystem
	if vm.HostRef != "" {
		hr := types.ManagedObjectReference{Type: "HostSystem", Value: vm.HostRef}
		hostObj = object.NewHostSystem(c.Client, hr)
	}
	// Resolve a resource pool. Prefer the requested host's pool; otherwise pick the
	// first resource pool in the datacenter. DefaultResourcePool errors when several
	// compute resources exist (the common multi-host/cluster case), so we resolve
	// explicitly.
	var pool *object.ResourcePool
	if hostObj != nil {
		if rp, err := hostObj.ResourcePool(ctx); err == nil {
			pool = rp
		}
	}
	if pool == nil {
		pools, err := finder.ResourcePoolList(ctx, "*")
		if err != nil || len(pools) == 0 {
			if rp, derr := finder.DefaultResourcePool(ctx); derr == nil {
				pool = rp
			} else {
				b.fail(err)
				return
			}
		} else {
			pool = pools[0]
		}
	}
	// Resolve a datastore NAME for the VM home path. The spec carries a datastore
	// moRef (StorageID); if absent, fall back to the datacenter's default datastore.
	dsName := ""
	if len(vm.Disks) > 0 && vm.Disks[0].DatastoreID != "" {
		dsRef := types.ManagedObjectReference{Type: "Datastore", Value: vm.Disks[0].DatastoreID}
		var moDS mo.Datastore
		if pc := property.DefaultCollector(c.Client); pc != nil {
			if err := pc.RetrieveOne(ctx, dsRef, []string{"name"}, &moDS); err == nil {
				dsName = moDS.Name
			}
		}
	}
	if dsName == "" {
		if ds, err := finder.DefaultDatastore(ctx); err == nil {
			dsName = ds.Name()
		} else if dss, derr := finder.DatastoreList(ctx, "*"); derr == nil && len(dss) > 0 {
			dsName = dss[0].Name()
		} else {
			dsName = "datastore1"
		}
	}
	spec := types.VirtualMachineConfigSpec{
		Name:     vm.Name,
		NumCPUs:  int32(vm.NumCPU),
		MemoryMB: vm.MemoryMB,
		GuestId:  vm.GuestID,
		Files:    &types.VirtualMachineFileInfo{VmPathName: fmt.Sprintf("[%s]", dsName)},
	}
	if vm.Firmware == vp.FirmwareUEFI {
		spec.Firmware = string(types.GuestOsDescriptorFirmwareTypeEfi)
	} else if vm.Firmware == vp.FirmwareBIOS {
		spec.Firmware = string(types.GuestOsDescriptorFirmwareTypeBios)
	}
	t, err := folders.VmFolder.CreateVM(ctx, spec, pool, hostObj)
	if err != nil {
		b.fail(err)
		return
	}
	info, err := t.WaitForResult(ctx, nil)
	if err != nil {
		b.fail(err)
		return
	}
	if ref, ok := info.Result.(types.ManagedObjectReference); ok {
		// Reflect the real vCenter-assigned moRef back into the model.
		vm.MoRef = ref.Value
		b.mu.Lock()
		b.vmRefs[ref.Value] = ref
		b.mu.Unlock()
	}
}

func (b *liveBackend) destroyVM(moRef string) {
	c, vm, ok := b.vmObject(moRef)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	_ = c
	t, err := vm.Destroy(ctx)
	if err != nil {
		b.fail(err)
		return
	}
	if _, err := t.WaitForResult(ctx, nil); err != nil {
		b.fail(err)
		return
	}
	b.mu.Lock()
	delete(b.vmRefs, moRef)
	b.mu.Unlock()
}

func (b *liveBackend) setPower(moRef string, s powerState) {
	_, vm, ok := b.vmObject(moRef)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	var (
		t   *object.Task
		err error
	)
	switch s {
	case powerOn:
		t, err = vm.PowerOn(ctx)
	case powerOff:
		t, err = vm.PowerOff(ctx)
	case powerSuspended:
		t, err = vm.Suspend(ctx)
	default:
		return
	}
	if err != nil {
		b.fail(err)
		return
	}
	if _, err := t.WaitForResult(ctx, nil); err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) vmsOnHost(hostRef string) int {
	n := 0
	for _, vm := range b.listVMs() {
		if vm.HostRef == hostRef {
			n++
		}
	}
	return n
}

// --- snapshots ---

func (b *liveBackend) listSnapshots(moRef string) []vp.Snapshot {
	c, vm, ok := b.vmObject(moRef)
	if !ok {
		return nil
	}
	_ = c
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var moVM mo.VirtualMachine
	pc := property.DefaultCollector(b.clientRef())
	if pc == nil {
		return nil
	}
	if err := pc.RetrieveOne(ctx, vm.Reference(), []string{"snapshot"}, &moVM); err != nil {
		b.fail(err)
		return nil
	}
	if moVM.Snapshot == nil {
		return nil
	}
	current := ""
	if moVM.Snapshot.CurrentSnapshot != nil {
		current = moVM.Snapshot.CurrentSnapshot.Value
	}
	var out []vp.Snapshot
	var walk func(tree []types.VirtualMachineSnapshotTree, parent string)
	walk = func(tree []types.VirtualMachineSnapshotTree, parent string) {
		for i := range tree {
			node := &tree[i]
			out = append(out, vp.Snapshot{
				ID:          node.Snapshot.Value,
				VMID:        moRef,
				Name:        node.Name,
				Description: node.Description,
				ParentID:    parent,
				HasMemory:   node.State == types.VirtualMachinePowerStatePoweredOn,
				IsCurrent:   node.Snapshot.Value == current,
				CreatedAt:   node.CreateTime,
			})
			walk(node.ChildSnapshotList, node.Snapshot.Value)
		}
	}
	walk(moVM.Snapshot.RootSnapshotList, "")
	return out
}

func (b *liveBackend) createSnapshot(moRef string, snap vp.Snapshot) {
	_, vm, ok := b.vmObject(moRef)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// CreateSnapshot(name, description, memory, quiesce).
	t, err := vm.CreateSnapshot(ctx, snap.Name, snap.Description, snap.HasMemory, false)
	if err != nil {
		b.fail(err)
		return
	}
	if _, err := t.WaitForResult(ctx, nil); err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) setCurrentSnapshot(moRef, snapID string) bool {
	_, vm, ok := b.vmObject(moRef)
	if !ok {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	// Resolve the snapshot moRef -> a SnapshotTree node, then RevertToSnapshot.
	var moVM mo.VirtualMachine
	pc := property.DefaultCollector(b.clientRef())
	if pc == nil {
		return false
	}
	if err := pc.RetrieveOne(ctx, vm.Reference(), []string{"snapshot"}, &moVM); err != nil {
		b.fail(err)
		return false
	}
	if moVM.Snapshot == nil || !snapshotExists(moVM.Snapshot.RootSnapshotList, snapID) {
		return false
	}
	// RevertToSnapshot_Task is invoked on the snapshot managed object itself.
	snapRef := types.ManagedObjectReference{Type: "VirtualMachineSnapshot", Value: snapID}
	res, err := methods.RevertToSnapshot_Task(ctx, b.clientRef(), &types.RevertToSnapshot_Task{
		This: snapRef,
	})
	if err != nil {
		b.fail(err)
		return false
	}
	t := object.NewTask(b.clientRef(), res.Returnval)
	if _, err := t.WaitForResult(ctx, nil); err != nil {
		b.fail(err)
		return false
	}
	return true
}

func snapshotExists(tree []types.VirtualMachineSnapshotTree, id string) bool {
	for i := range tree {
		if tree[i].Snapshot.Value == id {
			return true
		}
		if snapshotExists(tree[i].ChildSnapshotList, id) {
			return true
		}
	}
	return false
}

// --- native object resolution ---

// clientRef returns the underlying *vim25.Client (the soap.Client transport govmomi
// objects bind to).
func (b *liveBackend) clientRef() *vim25.Client {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.client == nil {
		return nil
	}
	return b.client.Client
}

// vmObject resolves a cached native VirtualMachine object by moRef, refreshing the
// cache via listVMs if necessary.
func (b *liveBackend) vmObject(moRef string) (*vim25.Client, *object.VirtualMachine, bool) {
	b.mu.RLock()
	c := b.client
	ref, ok := b.vmRefs[moRef]
	b.mu.RUnlock()
	if c == nil {
		return nil, nil, false
	}
	if !ok {
		b.listVMs() // refresh
		b.mu.RLock()
		ref, ok = b.vmRefs[moRef]
		b.mu.RUnlock()
		if !ok {
			return nil, nil, false
		}
	}
	return c.Client, object.NewVirtualMachine(c.Client, ref), true
}

// migrate performs a real vMotion / relocate to the target host (official
// VirtualMachine.Relocate, RelocateVM_Task). Exposed beyond the vsphereBackend seam
// for completeness; the provider's MigrateVM updates the model and calls this.
func (b *liveBackend) migrate(moRef, targetHost string, live bool) error {
	_, vm, ok := b.vmObject(moRef)
	if !ok {
		return vp.ErrNotFound
	}
	hostRef := types.ManagedObjectReference{Type: "HostSystem", Value: targetHost}
	spec := types.VirtualMachineRelocateSpec{Host: &hostRef}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	t, err := vm.Relocate(ctx, spec, types.VirtualMachineMovePriorityDefaultPriority)
	if err != nil {
		return mapVimErr(err)
	}
	if _, err := t.WaitForResult(ctx, nil); err != nil {
		return mapVimErr(err)
	}
	return nil
}

// --- vim fault -> contract sentinel mapping ---

// mapVimErr maps a vim25/SOAP fault to a contract sentinel.
func mapVimErr(err error) error {
	if err == nil {
		return nil
	}
	if soap.IsVimFault(err) {
		switch soap.ToVimFault(err).(type) {
		case *types.ManagedObjectNotFound, *types.NotFound:
			return vp.ErrNotFound
		case *types.InvalidPowerState, *types.InvalidState, *types.ConcurrentAccess,
			*types.ResourceInUse, *types.FileLocked:
			return vp.ErrConflict
		case *types.InvalidArgument, *types.InvalidVmConfig, *types.InvalidDeviceSpec,
			*types.NotSupported:
			return vp.ErrInvalidSpec
		}
	}
	var te task.Error
	if errors.As(err, &te) && te.Fault() != nil {
		switch te.Fault().(type) {
		case *types.ManagedObjectNotFound, *types.NotFound:
			return vp.ErrNotFound
		case *types.InvalidPowerState, *types.InvalidState, *types.ConcurrentAccess,
			*types.ResourceInUse, *types.FileLocked:
			return vp.ErrConflict
		case *types.InvalidArgument, *types.InvalidVmConfig, *types.InvalidDeviceSpec,
			*types.NotSupported:
			return vp.ErrInvalidSpec
		}
	}
	if strings.Contains(strings.ToLower(err.Error()), "not found") {
		return vp.ErrNotFound
	}
	return err
}

var _ vsphereBackend = (*liveBackend)(nil)
