package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// VMPowerOp performs a power transition (start/stop/reset/suspend/resume).
func (s *Server) VMPowerOp(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	op := vprovider.PowerOp(chi.URLParam(r, "op"))
	if !op.Valid() {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, "Unknown power operation: "+string(op)))
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := p.PowerOp(ctx, chi.URLParam(r, "vmID"), op)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMCreate creates a VM from a normalized spec.
func (s *Server) VMCreate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var spec vprovider.VMSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := p.CreateVM(ctx, spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMReconfigure applies a partial reconfigure.
func (s *Server) VMReconfigure(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var spec vprovider.VMReconfigureSpec
	if err := decodeJSON(w, r, &spec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := p.ReconfigureVM(ctx, chi.URLParam(r, "vmID"), spec)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMDelete deletes a VM.
func (s *Server) VMDelete(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	opts := vprovider.DeleteOptions{
		Force:       r.URL.Query().Get("force") == "true",
		DeleteDisks: r.URL.Query().Get("deleteDisks") == "true",
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := p.DeleteVM(ctx, chi.URLParam(r, "vmID"), opts)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// vmSnapshotBody is the create-snapshot request.
type vmSnapshotBody struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Memory      bool   `json:"memory"`
	Quiesce     bool   `json:"quiesce"`
}

// VMSnapshotCreate takes a snapshot of a VM.
func (s *Server) VMSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var body vmSnapshotBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := p.Snapshot(ctx, chi.URLParam(r, "vmID"), vprovider.SnapshotOptions{
		Name: body.Name, Description: body.Description, Memory: body.Memory, Quiesce: body.Quiesce,
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// VMSnapshotRevert reverts a VM to a snapshot.
func (s *Server) VMSnapshotRevert(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	ctx, cancel := contextWithTimeout(r, 60*time.Second)
	defer cancel()
	task, err := p.RevertSnapshot(ctx, chi.URLParam(r, "vmID"), chi.URLParam(r, "snapID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// vmCloneBody is the clone request.
type vmCloneBody struct {
	Name      string `json:"name"`
	HostID    string `json:"hostId"`
	StorageID string `json:"storageId"`
	Linked    bool   `json:"linked"`
	PowerOn   bool   `json:"powerOn"`
}

// VMClone clones a VM.
func (s *Server) VMClone(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var body vmCloneBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	task, err := p.Clone(ctx, chi.URLParam(r, "vmID"), vprovider.CloneSpec{
		Name: body.Name, HostID: body.HostID, StorageID: body.StorageID, Linked: body.Linked, PowerOn: body.PowerOn,
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	created(w, task)
}

// vmMigrateBody is the intra-hypervisor migrate request.
type vmMigrateBody struct {
	TargetHost    string `json:"targetHost"`
	Live          bool   `json:"live"`
	TargetStorage string `json:"targetStorage"`
}

// VMMigrate performs an intra-hypervisor migration.
func (s *Server) VMMigrate(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	var body vmMigrateBody
	if err := decodeJSON(w, r, &body); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	task, err := p.MigrateVM(ctx, chi.URLParam(r, "vmID"), body.TargetHost, vprovider.MigrateOptions{
		Live: body.Live, TargetStorage: body.TargetStorage,
	})
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}
