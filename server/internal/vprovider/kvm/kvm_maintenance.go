// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// kvm_maintenance.go adds the OPTIONAL MaintenanceProvider surface (host
// maintenance mode + evacuation) to the KVM provider WITHOUT touching the create/
// render path (renderDomainXML/CreateVM are owned elsewhere). It composes the
// existing libvirtBackend seam (listNodes/listDomains) and the provider's own
// MigrateVM, so it works against both the in-memory sim backend and the real
// libvirt backend with no backend changes.
package kvm

import (
	"context"
	"fmt"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// isMaint reports whether host hostID is currently marked maintenance.
func (p *Provider) isMaint(hostID string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.maint[hostID]
}

// EnterMaintenance marks hostID maintenance and, when evacuate is set, live-
// migrates its RUNNING VMs to another available host. When evacuate is requested
// but no OTHER host exists (the common single-host KVM case), the node is still
// marked maintenance and the returned Task carries an honest "no evacuation
// target" note rather than failing — there is simply nowhere to move the VMs.
func (p *Provider) EnterMaintenance(ctx context.Context, hostID string, evacuate bool) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMaintenance) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getNode(hostID); !ok {
		return nil, vp.ErrNotFound
	}

	note := "host marked maintenance (no evacuation requested)"
	if evacuate {
		// Pick a target host: any OTHER node known to the backend.
		target := ""
		for _, n := range p.backend.listNodes() {
			if n.ID != hostID {
				target = n.ID
				break
			}
		}
		if target == "" {
			note = "host marked maintenance; no evacuation target (single host) — running VMs left in place"
		} else {
			migrated, failed := 0, 0
			for _, d := range p.backend.listDomains() {
				if d.HostID != hostID {
					continue
				}
				if normalizeState(d.State) != vp.StateRunning {
					continue // only running VMs need live evacuation
				}
				if _, err := p.MigrateVM(ctx, d.UUID, target, vp.MigrateOptions{Live: true}); err != nil {
					failed++
					continue
				}
				migrated++
			}
			note = fmt.Sprintf("host marked maintenance; evacuated %d running VM(s) to %s", migrated, target)
			if failed > 0 {
				note += fmt.Sprintf(" (%d failed to migrate)", failed)
			}
		}
	}

	// Mark the node maintenance AFTER evacuation so the placement reads above are
	// not themselves affected.
	p.mu.Lock()
	if p.maint == nil {
		p.maint = map[string]bool{}
	}
	p.maint[hostID] = true
	p.mu.Unlock()

	t := p.finishTask("enterMaintenance", hostID)
	t.Message = note
	return t, nil
}

// ExitMaintenance clears the maintenance mark on hostID, returning it to normal
// scheduling. It is idempotent (exiting a non-maintenance host succeeds).
func (p *Provider) ExitMaintenance(ctx context.Context, hostID string) (*vp.Task, error) {
	if !p.caps.Has(vp.CapMaintenance) {
		return nil, vp.ErrUnsupported
	}
	if _, ok := p.backend.getNode(hostID); !ok {
		return nil, vp.ErrNotFound
	}
	p.mu.Lock()
	delete(p.maint, hostID)
	p.mu.Unlock()
	return p.finishTask("exitMaintenance", hostID), nil
}

// compile-time assertion: *Provider satisfies the MaintenanceProvider contract.
var _ vp.MaintenanceProvider = (*Provider)(nil)
