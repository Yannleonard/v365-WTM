package api

import "github.com/go-chi/chi/v5"

// mountVMRoutes wires the UniHV VM / hypervisor surface and the unified inventory.
// Reads are gated by vm.* / inventory.read at provider scope (a global grant
// matches everything). Mutations follow the same fixed chain as the container
// side: AuditWrap (OUTERMOST, records one row even when a later gate denies) ->
// RequireAAL -> RequirePermission -> handler. Power/snapshot/reconfigure/clone/
// migrate/export are operator-grade (granted to the operator role); create and
// delete are admin-grade (only admin's "*" satisfies them).
func (s *Server) mountVMRoutes(pr chi.Router) {
	az := s.authz

	// Unified inventory (VM + containers) — the single-pane read.
	pr.With(az.RequirePermission("inventory.read", nil)).Get("/inventory", s.UnifiedInventory)

	// Hypervisor providers + capabilities (pre-flight greying).
	pr.With(az.RequirePermission("vm.read", nil)).Get("/vm/providers", s.VMProviders)

	// Per-provider reads.
	pr.With(az.RequirePermission("vm.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/vms", s.VMs)
	pr.With(az.RequirePermission("vm.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/vms/{vmID}", s.VMDetailHandler)
	pr.With(az.RequirePermission("vm.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/vms/{vmID}/snapshots", s.VMSnapshots)
	pr.With(az.RequirePermission("vm.metrics.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/vms/{vmID}/metrics", s.VMMetrics)
	pr.With(az.RequirePermission("vm.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/hosts", s.VMHosts)
	pr.With(az.RequirePermission("vm.cluster.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/clusters", s.VMClusters)
	pr.With(az.RequirePermission("vm.cluster.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/clusters/{clusterID}/topology", s.VMClusterTopology)
	pr.With(az.RequirePermission("vm.storage.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/storage", s.VMStorage)
	pr.With(az.RequirePermission("vm.network.read", scopeFromProvider)).
		Get("/vm/providers/{providerID}/networks", s.VMNetworks)

	// Lifecycle mutations.
	pr.With(az.AuditWrap("vm.power"), az.RequireAAL, az.RequirePermission("vm.power", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/power/{op}", s.VMPowerOp)
	pr.With(az.AuditWrap("vm.create"), az.RequireAAL, az.RequirePermission("vm.create", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms", s.VMCreate)
	pr.With(az.AuditWrap("vm.reconfigure"), az.RequireAAL, az.RequirePermission("vm.reconfigure", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/reconfigure", s.VMReconfigure)
	pr.With(az.AuditWrap("vm.delete"), az.RequireAAL, az.RequirePermission("vm.delete", scopeFromProvider)).
		Delete("/vm/providers/{providerID}/vms/{vmID}", s.VMDelete)

	// Snapshots & clones.
	pr.With(az.AuditWrap("vm.snapshot.create"), az.RequireAAL, az.RequirePermission("vm.snapshot", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/snapshots", s.VMSnapshotCreate)
	pr.With(az.AuditWrap("vm.snapshot.revert"), az.RequireAAL, az.RequirePermission("vm.snapshot", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/snapshots/{snapID}/revert", s.VMSnapshotRevert)
	pr.With(az.AuditWrap("vm.clone"), az.RequireAAL, az.RequirePermission("vm.clone", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/clone", s.VMClone)

	// Migration (intra-hypervisor; cross-hypervisor V2V is the migrate engine, Phase 4).
	pr.With(az.AuditWrap("vm.migrate"), az.RequireAAL, az.RequirePermission("vm.migrate", scopeFromProvider)).
		Post("/vm/providers/{providerID}/vms/{vmID}/migrate", s.VMMigrate)
}
