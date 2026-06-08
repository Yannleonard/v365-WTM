package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

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

