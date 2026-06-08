package vprovider

// CapabilityMatrix is a bitset of operations a HypervisorProvider supports. The
// API serializes the active flags to the UI (as a string array) so unsupported
// affordances are greyed out BEFORE the user clicks — never "click then 405"
// (prompt §3.4: no action fails silently due to an absent capability). It mirrors
// the container-domain Capability bitset.
type CapabilityMatrix uint64

const (
	// --- inventory (read) ---
	CapListHosts CapabilityMatrix = 1 << iota
	CapListVMs
	CapGetVM
	CapListClusters
	CapListStorage
	CapListNetworks

	// --- VM lifecycle ---
	CapCreateVM
	CapPowerStart
	CapPowerStop
	CapPowerReset
	CapPowerSuspend // suspend + resume
	CapDeleteVM
	CapReconfigureVM

	// --- snapshots & clones ---
	CapSnapshot
	CapRevertSnapshot
	CapClone

	// --- migration ---
	CapMigrate // intra-hypervisor migrate
	CapExport  // export disks for V2V

	// --- cluster & HA ---
	CapClusterTopology
	CapNodeState

	// --- observability ---
	CapMetrics
	CapEvents
)

// Has reports whether all bits in want are set in c.
func (c CapabilityMatrix) Has(want CapabilityMatrix) bool { return c&want == want }

// capTokens maps each capability bit to its stable lowercase wire token, in a
// deterministic order matching the REST contract.
var capTokens = []struct {
	bit   CapabilityMatrix
	token string
}{
	{CapListHosts, "list_hosts"},
	{CapListVMs, "list_vms"},
	{CapGetVM, "get_vm"},
	{CapListClusters, "list_clusters"},
	{CapListStorage, "list_storage"},
	{CapListNetworks, "list_networks"},
	{CapCreateVM, "create_vm"},
	{CapPowerStart, "power_start"},
	{CapPowerStop, "power_stop"},
	{CapPowerReset, "power_reset"},
	{CapPowerSuspend, "power_suspend"},
	{CapDeleteVM, "delete_vm"},
	{CapReconfigureVM, "reconfigure_vm"},
	{CapSnapshot, "snapshot"},
	{CapRevertSnapshot, "revert_snapshot"},
	{CapClone, "clone"},
	{CapMigrate, "migrate"},
	{CapExport, "export"},
	{CapClusterTopology, "cluster_topology"},
	{CapNodeState, "node_state"},
	{CapMetrics, "metrics"},
	{CapEvents, "events"},
}

// Strings returns the active capabilities as stable lowercase tokens for the
// API/UI. Order is deterministic.
func (c CapabilityMatrix) Strings() []string {
	out := make([]string, 0, len(capTokens))
	for _, ct := range capTokens {
		if c.Has(ct.bit) {
			out = append(out, ct.token)
		}
	}
	return out
}

// PowerOpCapability returns the capability bit required for a given power op.
// PowerResume shares the suspend capability bit.
func PowerOpCapability(op PowerOp) CapabilityMatrix {
	switch op {
	case PowerStart:
		return CapPowerStart
	case PowerStop:
		return CapPowerStop
	case PowerReset:
		return CapPowerReset
	case PowerSuspend, PowerResume:
		return CapPowerSuspend
	}
	return 0
}
