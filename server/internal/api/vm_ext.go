package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/store"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// labelPool is the VM label that assigns a VM to a resource pool (Lot 5A).
const labelPool = "unihv.pool"

// --- console ---

// VMConsole returns the graphical-console endpoint for a VM (VNC/SPICE/RDP). The
// UI uses it to open a noVNC viewer (VNC/SPICE) or hand off RDP. Requires the
// provider to implement vprovider.ConsoleProvider + advertise CapConsole.
func (s *Server) VMConsole(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	cp, impl := p.(vprovider.ConsoleProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapConsole) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	ep, err := cp.Console(ctx, chi.URLParam(r, "vmID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	// The console password is a one-shot ticket; it is returned to the authenticated,
	// console-permitted user only (already gated by RBAC vm.console on the route).
	ok(w, ep)
}

// --- network write ---

// VMNetworkCreate creates a virtual network/switch on a provider.
func (s *Server) VMNetworkCreate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	nw, impl := p.(vprovider.NetworkWriter)
	if !impl || !p.Capabilities().Has(vprovider.CapNetworkWrite) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var spec vprovider.NetworkSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := nw.CreateNetwork(ctx, spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMNetworkDelete deletes a virtual network/switch.
func (s *Server) VMNetworkDelete(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	nw, impl := p.(vprovider.NetworkWriter)
	if !impl || !p.Capabilities().Has(vprovider.CapNetworkWrite) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := nw.DeleteNetwork(ctx, chi.URLParam(r, "networkID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// --- storage / volumes / ISO ---

// VMVolumes lists volumes (disks + ISOs) in a storage pool.
func (s *Server) VMVolumes(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sp, impl := p.(vprovider.StorageProvider)
	if !impl {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	vols, err := sp.ListVolumes(ctx, chi.URLParam(r, "storageID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, vols)
}

// VMVolumeCreate provisions a disk volume in a pool.
func (s *Server) VMVolumeCreate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sp, impl := p.(vprovider.StorageProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapStorageWrite) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var spec vprovider.VolumeSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	spec.StorageID = chi.URLParam(r, "storageID")
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := sp.CreateVolume(ctx, spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMVolumeDelete removes a volume.
func (s *Server) VMVolumeDelete(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sp, impl := p.(vprovider.StorageProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapStorageWrite) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := sp.DeleteVolume(ctx, chi.URLParam(r, "storageID"), chi.URLParam(r, "volumeID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMISOUpload streams an ISO image into a storage pool (multipart/octet-stream body).
func (s *Server) VMISOUpload(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sp, impl := p.(vprovider.StorageProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapStorageWrite) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	name := r.URL.Query().Get("name")
	if name == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "name query param is required"))
		return
	}
	// Cap ISO upload to a generous bound (8 GiB) to avoid unbounded writes.
	const maxISO = 8 << 30
	body := http.MaxBytesReader(w, r.Body, maxISO)
	defer body.Close()
	ctx, cancel := contextWithTimeout(r, 30*time.Minute)
	defer cancel()
	vol, err := sp.UploadISO(ctx, chi.URLParam(r, "storageID"), name, r.ContentLength, body)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, vol)
}

// --- hot-plug device management (DeviceManager) ---

// vmDeviceManager resolves the request's provider and, if it implements
// DeviceManager + advertises CapHotPlug, returns it. Otherwise it writes a 405 and
// returns false (so the caller returns immediately) — never a silent failure.
func (s *Server) vmDeviceManager(w http.ResponseWriter, r *http.Request) (vprovider.DeviceManager, string, bool) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return nil, "", false
	}
	dm, impl := p.(vprovider.DeviceManager)
	if !impl || !p.Capabilities().Has(vprovider.CapHotPlug) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return nil, "", false
	}
	return dm, chi.URLParam(r, "vmID"), true
}

// VMDiskAttach hot-attaches a disk to a RUNNING VM (no reboot).
func (s *Server) VMDiskAttach(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	var spec vprovider.DiskSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := dm.AttachDisk(ctx, vmID, spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMDiskDetach hot-removes a disk from a RUNNING VM.
func (s *Server) VMDiskDetach(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := dm.DetachDisk(ctx, vmID, chi.URLParam(r, "diskID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMNICAttach hot-attaches a virtual NIC to a RUNNING VM.
func (s *Server) VMNICAttach(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	var spec vprovider.NICSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := dm.AttachNIC(ctx, vmID, spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMNICDetach hot-removes a NIC from a RUNNING VM.
func (s *Server) VMNICDetach(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := dm.DetachNIC(ctx, vmID, chi.URLParam(r, "nicID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// vmISOBody is the mount-ISO request body.
type vmISOBody struct {
	ISOPath string `json:"isoPath"`
}

// VMISOMount inserts an ISO into a RUNNING VM's CD-ROM (no reboot).
func (s *Server) VMISOMount(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	var body vmISOBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := dm.MountISO(ctx, vmID, body.ISOPath)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMISOUnmount ejects the media from a RUNNING VM's CD-ROM.
func (s *Server) VMISOUnmount(w http.ResponseWriter, r *http.Request) {
	dm, vmID, okp := s.vmDeviceManager(w, r)
	if !okp {
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := dm.UnmountISO(ctx, vmID)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// --- guest agent (qemu-ga) ---

// VMGuestInfo returns the in-guest agent view (hostname, OS, IPs, agent-connected
// flag). Requires the provider to implement GuestAgentProvider + advertise
// CapGuestAgent. When the agent is simply NOT running in the guest the handler
// still returns 200 with agentConnected=false (a soft state, not a 405/500).
func (s *Server) VMGuestInfo(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ga, impl := p.(vprovider.GuestAgentProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapGuestAgent) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()
	gi, err := ga.GuestInfo(ctx, chi.URLParam(r, "vmID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, gi)
}

// --- snapshot management (delete-single) ---

// VMSnapshotDelete deletes a SINGLE snapshot (DomainSnapshotDelete). Requires
// SnapshotManager + CapSnapshot.
func (s *Server) VMSnapshotDelete(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sm, impl := p.(vprovider.SnapshotManager)
	if !impl || !p.Capabilities().Has(vprovider.CapSnapshot) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := sm.DeleteSnapshot(ctx, chi.URLParam(r, "vmID"), chi.URLParam(r, "snapID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// --- online disk resize ---

// vmDiskResizeBody is the resize-disk request body.
type vmDiskResizeBody struct {
	CapacityGB float64 `json:"capacityGb"`
}

// VMDiskResize grows a VM's disk online (DomainBlockResize). Requires DiskResizer +
// CapDiskResize.
func (s *Server) VMDiskResize(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	dr, impl := p.(vprovider.DiskResizer)
	if !impl || !p.Capabilities().Has(vprovider.CapDiskResize) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var body vmDiskResizeBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := dr.ResizeDisk(ctx, chi.URLParam(r, "vmID"), chi.URLParam(r, "diskID"), body.CapacityGB)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// --- VM templates (Lot 4A): mark/unmark + list templates ---

// vmTemplateBody is the mark-as-template request body.
type vmTemplateBody struct {
	IsTemplate bool `json:"isTemplate"`
}

// VMMarkTemplate marks/unmarks a VM as a TEMPLATE (golden image). Requires
// TemplateManager + CapTemplates. A running VM cannot be (un)marked (409).
func (s *Server) VMMarkTemplate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	tm, impl := p.(vprovider.TemplateManager)
	if !impl || !p.Capabilities().Has(vprovider.CapTemplates) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var body vmTemplateBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := tm.MarkTemplate(ctx, chi.URLParam(r, "vmID"), body.IsTemplate)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// --- Lot 5A: CPU/memory resource control + per-disk QoS + live storage migration ---

// VMSetResources applies CPU/memory reservation/limit/shares to a VM (<cputune>/
// <memtune>). Requires ResourceController + CapResourceControl.
func (s *Server) VMSetResources(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	rc, impl := p.(vprovider.ResourceController)
	if !impl || !p.Capabilities().Has(vprovider.CapResourceControl) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var spec vprovider.ResourceSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := rc.SetResources(ctx, chi.URLParam(r, "vmID"), spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMDiskQoS sets a disk's IOPS/bandwidth throttle (<iotune>) on an existing disk
// (no reboot). Requires DiskQoSManager + CapDiskQoS.
func (s *Server) VMDiskQoS(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	qm, impl := p.(vprovider.DiskQoSManager)
	if !impl || !p.Capabilities().Has(vprovider.CapDiskQoS) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var qos vprovider.DiskQoS
	if err := decodeJSON(w, r, &qos); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := qm.SetDiskQoS(ctx, chi.URLParam(r, "vmID"), chi.URLParam(r, "diskID"), qos)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// vmStorageMigrateBody is the live storage-migration request body.
type vmStorageMigrateBody struct {
	TargetStorageID string `json:"targetStorageId"`
}

// VMStorageMigrate live-migrates a VM's disk to another storage pool/path (no
// downtime; DomainBlockCopy + pivot). Requires StorageMigrator + CapStorageMigrate.
func (s *Server) VMStorageMigrate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	sm, impl := p.(vprovider.StorageMigrator)
	if !impl || !p.Capabilities().Has(vprovider.CapStorageMigrate) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var body vmStorageMigrateBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Minute)
	defer cancel()
	task, err := sm.MigrateStorage(ctx, chi.URLParam(r, "vmID"), chi.URLParam(r, "diskID"), body.TargetStorageID)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMTemplates lists the provider's TEMPLATE VMs (those carrying the
// "unihv.template=true" Label). Reuses ListVMs with a label filter so the result is
// the SAME normalized VM shape the list view already renders. Requires CapTemplates.
func (s *Server) VMTemplates(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	if !p.Capabilities().Has(vprovider.CapTemplates) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	vms, err := p.ListVMs(ctx, vprovider.ListOptions{
		Labels: map[string]string{"unihv.template": "true"},
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, vms)
}

// --- Lot 5A: resource pools (persisted, assignable, reported) ---

// poolView merges a stored pool with its live member count + aggregate usage so the
// UI can show "budget vs. used". MemberVMIDs are the VMs labeled unihv.pool=<id>.
type poolView struct {
	store.ResourcePool
	MemberVMIDs   []string `json:"memberVmIds"`
	MemberCount   int      `json:"memberCount"`
	UsedVCPUs     int      `json:"usedVcpus"`
	UsedMemoryMB  int64    `json:"usedMemoryMb"`
	Note          string   `json:"note,omitempty"`
}

// poolMembers lists the VMs assigned to a pool (label unihv.pool=<id>) on the pool's
// provider, returning ids + aggregate vCPU/memory. Best-effort: a provider read
// failure yields an empty member set (the pool definition is still valid).
func (s *Server) poolMembers(r *http.Request, pl *store.ResourcePool) ([]string, int, int64) {
	p, ok := s.vreg.Get(pl.ProviderID)
	if !ok {
		return nil, 0, 0
	}
	ctx, cancel := contextWithTimeout(r, 10*time.Second)
	defer cancel()
	vms, err := p.ListVMs(ctx, vprovider.ListOptions{Labels: map[string]string{labelPool: pl.ID}})
	if err != nil {
		return nil, 0, 0
	}
	ids := make([]string, 0, len(vms))
	var vcpu int
	var mem int64
	for _, v := range vms {
		ids = append(ids, v.ID)
		vcpu += v.VCPUs
		mem += v.MemoryMB
	}
	return ids, vcpu, mem
}

func (s *Server) toPoolView(r *http.Request, pl *store.ResourcePool) poolView {
	ids, vcpu, mem := s.poolMembers(r, pl)
	return poolView{
		ResourcePool: *pl,
		MemberVMIDs:  ids,
		MemberCount:  len(ids),
		UsedVCPUs:    vcpu,
		UsedMemoryMB: mem,
		Note:         "Pool limits are an advisory/reported allocation budget; plain libvirt has no native parent-cgroup pool enforcement.",
	}
}

// poolInput is the create/update resource-pool request body.
type poolInput struct {
	Name        string `json:"name"`
	ProviderID  string `json:"providerId"`
	CPUShares   int64  `json:"cpuShares"`
	CPULimitMHz int64  `json:"cpuLimitMhz"`
	MemShares   int64  `json:"memShares"`
	MemLimitMB  int64  `json:"memLimitMb"`
	Notes       string `json:"notes"`
}

// VMPools lists resource pools (optionally filtered by ?providerId=) with members.
func (s *Server) VMPools(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListResourcePools(r.Context(), r.URL.Query().Get("providerId"))
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]poolView, 0, len(rows))
	for _, p := range rows {
		out = append(out, s.toPoolView(r, p))
	}
	ok(w, out)
}

// VMPoolCreate creates a resource pool.
func (s *Server) VMPoolCreate(w http.ResponseWriter, r *http.Request) {
	var in poolInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if strings.TrimSpace(in.Name) == "" || strings.TrimSpace(in.ProviderID) == "" {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "name and providerId are required"))
		return
	}
	if _, ok := s.vreg.Get(in.ProviderID); !ok {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "unknown hypervisor provider: "+in.ProviderID))
		return
	}
	rec := &store.ResourcePool{
		ID: store.NewUUID(), Name: in.Name, ProviderID: in.ProviderID,
		CPUShares: in.CPUShares, CPULimitMHz: in.CPULimitMHz,
		MemShares: in.MemShares, MemLimitMB: in.MemLimitMB, Notes: in.Notes,
	}
	if err := s.store.CreateResourcePool(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	created(w, s.toPoolView(r, rec))
}

// VMPoolUpdate updates a pool's budget + notes.
func (s *Server) VMPoolUpdate(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "poolID")
	rec, err := s.store.GetResourcePool(r.Context(), id)
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	var in poolInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec.CPUShares, rec.CPULimitMHz = in.CPUShares, in.CPULimitMHz
	rec.MemShares, rec.MemLimitMB = in.MemShares, in.MemLimitMB
	rec.Notes = in.Notes
	if err := s.store.UpdateResourcePool(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ok(w, s.toPoolView(r, rec))
}

// VMPoolDelete removes a pool. Member VMs keep running; their unihv.pool label is
// left as-is (clearing labels is a reconfigure the caller can do separately).
func (s *Server) VMPoolDelete(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteResourcePool(r.Context(), chi.URLParam(r, "poolID")); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// vmPoolAssignBody assigns/unassigns a VM to a pool via the unihv.pool label.
type vmPoolAssignBody struct {
	PoolID string `json:"poolId"` // "" to UNASSIGN (clear the label to empty)
}

// VMPoolAssign assigns the VM to a pool (or clears it) by setting the unihv.pool
// label through a real ReconfigureVM call (which persists the label on the domain).
func (s *Server) VMPoolAssign(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var body vmPoolAssignBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if body.PoolID != "" {
		if _, err := s.store.GetResourcePool(r.Context(), body.PoolID); err != nil {
			authz.WriteError(w, r, err)
			return
		}
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := p.ReconfigureVM(ctx, chi.URLParam(r, "vmID"), vprovider.VMReconfigureSpec{
		Labels: map[string]string{labelPool: body.PoolID},
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

