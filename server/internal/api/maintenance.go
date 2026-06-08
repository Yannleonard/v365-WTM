package api

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// maintenanceBody is the enter-maintenance request: evacuate live-migrates the
// host's running VMs to another host first (where one exists).
type maintenanceBody struct {
	Evacuate bool `json:"evacuate"`
}

// VMHostEnterMaintenance puts a host into maintenance mode (optionally evacuating
// its running VMs). Requires the provider to implement vprovider.MaintenanceProvider
// + advertise CapMaintenance, else 405 (pre-flight greying mirrors console/hotplug).
func (s *Server) VMHostEnterMaintenance(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	mp, impl := p.(vprovider.MaintenanceProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapMaintenance) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	var body maintenanceBody
	// A missing/empty body defaults to evacuate=false (drain placement only).
	if r.ContentLength != 0 {
		if err := decodeJSON(w, r, &body); err != nil {
			authz.WriteError(w, r, err)
			return
		}
	}
	ctx, cancel := contextWithTimeout(r, 120*time.Second)
	defer cancel()
	task, err := mp.EnterMaintenance(ctx, chi.URLParam(r, "hostID"), body.Evacuate)
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}

// VMHostExitMaintenance clears a host's maintenance mark.
func (s *Server) VMHostExitMaintenance(w http.ResponseWriter, r *http.Request) {
	p, found := s.resolveVMProvider(w, r)
	if !found {
		return
	}
	mp, impl := p.(vprovider.MaintenanceProvider)
	if !impl || !p.Capabilities().Has(vprovider.CapMaintenance) {
		authz.WriteError(w, r, authz.ErrMethodNotAllowed)
		return
	}
	ctx, cancel := contextWithTimeout(r, 30*time.Second)
	defer cancel()
	task, err := mp.ExitMaintenance(ctx, chi.URLParam(r, "hostID"))
	if err != nil {
		authz.WriteError(w, r, vmProviderError(err))
		return
	}
	ok(w, task)
}
