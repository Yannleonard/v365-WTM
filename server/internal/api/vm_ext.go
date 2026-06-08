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

