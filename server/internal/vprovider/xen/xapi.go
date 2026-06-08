// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// Package xen implements a HypervisorProvider for Xen via XAPI (XenServer /
// XCP-ng / Xen Orchestra). Per D-005 it is split into a pure-Go, CGO-free core
// (this file + xen.go) that normalizes XAPI-native concepts — VM, host, pool
// (=cluster), SR (storage repository), network, VM snapshots, all addressed by
// opaque refs like "OpaqueRef:..." and carrying XAPI power_state values
// (Running/Halted/Suspended/Paused) — into the vprovider contract entities, and a
// pluggable backend (xapiBackend) that the default build satisfies with an
// in-memory XAPI fake (sim_backend.go). Real XAPI transport (XML-RPC / JSON-RPC
// over HTTP, e.g. VM.get_all_records / VM.start / VM.pool_migrate / event.next)
// is sketched behind the `//go:build xen_live` tag (live.go) and is NOT imported
// by the default build — keeping the distroless CGO_ENABLED=0 image intact and
// go.mod untouched (no XAPI SDK dependency).
package xen

import (
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// xapiPowerState mirrors XAPI's vm_power_state enum (the verbatim token a real
// XAPI VM record reports in its power_state field). Normalization maps these to
// vp.VMState; the raw token is preserved in VM.StateRaw.
//
// Reference (XenAPI vm_power_state):
//
//	Halted     the VM is powered off
//	Running    the VM is running
//	Suspended  the VM is suspended (whole-VM state saved to disk)
//	Paused     the VM is paused (vCPUs paused, RAM resident)
type xapiPowerState string

// The canonical XAPI vm_power_state tokens (case-sensitive, verbatim).
const (
	psHalted    xapiPowerState = "Halted"
	psRunning   xapiPowerState = "Running"
	psSuspended xapiPowerState = "Suspended"
	psPaused    xapiPowerState = "Paused"
)

// raw returns the XAPI-native power_state token (verbatim, for VM.StateRaw).
func (s xapiPowerState) raw() string { return string(s) }

// normalizeState maps an XAPI vm_power_state to the contract VMState, faithfully
// (prompt item 3):
//
//   - Running   -> running
//   - Halted    -> stopped
//   - Suspended -> suspended
//   - Paused    -> paused
//   - anything else -> unknown
func normalizeState(s xapiPowerState) vp.VMState {
	switch s {
	case psRunning:
		return vp.StateRunning
	case psHalted:
		return vp.StateStopped
	case psSuspended:
		return vp.StateSuspended
	case psPaused:
		return vp.StatePaused
	default:
		return vp.StateUnknown
	}
}

// xapiVM is a tiny subset of an XAPI VM record — enough to normalize faithfully
// without depending on a XAPI SDK. A real backend would populate this from
// VM.get_record / VM.get_all_records.
type xapiVM struct {
	Ref        string // opaque ref "OpaqueRef:..." -> vp.VM.ID
	UUID       string
	NameLabel  string
	PowerState xapiPowerState
	ResidentOn string // host opaque ref the VM runs on -> HostID
	VCPUsMax   int    // VCPUs_max
	MemoryB    int64  // memory_static_max (bytes)
	OSDistro   string // guest_metrics os_version distro -> GuestOS
	HVM        bool   // HVM_boot_policy != "" ; false => PV (treated as BIOS-ish)
	UEFI       bool   // platform "firmware"=="uefi"
	VBDs       []xapiVBD
	VIFs       []xapiVIF
	IPs        []string
	Labels     map[string]string // other_config tags
	Created    int64             // unix seconds
	IsControl  bool              // is_control_domain / is a template -> Protected
}

// xapiVBD is a subset of a VM block device + its VDI (virtual disk image).
type xapiVBD struct {
	Ref      string // VBD opaque ref
	Device   string // userdevice e.g. "0" / "xvda"
	VDIRef   string // VDI opaque ref
	SRRef    string // owning SR opaque ref -> StorageID
	VirtualB int64  // VDI virtual_size (bytes)
	Path     string // location
}

// xapiVIF is a subset of a VM virtual interface.
type xapiVIF struct {
	Ref        string // VIF opaque ref
	MAC        string
	NetworkRef string // owning network opaque ref -> NetworkID
	Model      string // device model where relevant
	Attached   bool   // currently_attached
}

// xapiHost is a subset of an XAPI host record (Host.get_record).
type xapiHost struct {
	Ref        string // host opaque ref -> Host.ID
	UUID       string
	NameLabel  string
	Enabled    bool   // host.enabled (false => maintenance)
	Live       bool   // host_metrics.live
	CPUCount   int    // host_cpu count
	CPUMHz     int    // host_cpu speed
	MemoryTotB int64  // metrics memory_total (bytes)
	MemoryFreB int64  // metrics memory_free (bytes)
	Version    string // software_version product_version_text
}

// xapiPool is a subset of an XAPI pool record (Pool.get_record). The pool groups
// the hosts and is normalized into the contract Cluster (prompt item 3).
type xapiPool struct {
	Ref       string // pool opaque ref -> Cluster.ID
	UUID      string
	NameLabel string
	MasterRef string   // pool master host ref
	HostRefs  []string // member host refs
	HAEnabled bool     // ha_enabled
}

// xapiSR is a subset of an XAPI storage repository record (SR.get_record).
type xapiSR struct {
	Ref        string // SR opaque ref -> StoragePool.ID
	UUID       string
	NameLabel  string
	Type       string   // lvmoiscsi|nfs|ext|lvm|... -> StoragePool.Type
	PhysSizeB  int64    // physical_size (bytes)
	PhysUtilB  int64    // physical_utilisation (bytes)
	HostRefs   []string // PBD-connected hosts
	Shared     bool
	Accessible bool
}

// xapiNetwork is a subset of an XAPI network record (Network.get_record).
type xapiNetwork struct {
	Ref       string // network opaque ref -> Network.ID
	UUID      string
	NameLabel string
	Bridge    string // bridge name -> Type hint
	VLAN      int    // VLAN tag (0 = none)
}

const bytesPerGB = 1 << 30

// --- normalization (XAPI-native -> contract) ---

func (p *Provider) normalizeVM(v *xapiVM) vp.VM {
	out := vp.VM{
		ID:          v.Ref,
		Name:        v.NameLabel,
		Kind:        p.kind,
		ProviderID:  p.id,
		HostID:      v.ResidentOn,
		ClusterID:   p.clusterID,
		State:       normalizeState(v.PowerState),
		StateRaw:    v.PowerState.raw(),
		VCPUs:       v.VCPUsMax,
		MemoryMB:    v.MemoryB / (1 << 20), // bytes -> MiB
		GuestOS:     v.OSDistro,
		Firmware:    normalizeFirmware(v),
		IPAddresses: append([]string(nil), v.IPs...),
		Labels:      v.Labels,
		Protected:   v.IsControl,
	}
	if v.Created > 0 {
		out.CreatedAt = unixUTC(v.Created)
	}
	for _, d := range v.VBDs {
		out.Disks = append(out.Disks, vp.Disk{
			ID:         d.Ref,
			Label:      d.Device,
			Format:     vp.DiskRaw, // Xen VDIs are presented to the guest as raw block devices
			CapacityGB: float64(d.VirtualB) / bytesPerGB,
			StorageID:  d.SRRef,
			Path:       d.Path,
		})
	}
	for _, n := range v.VIFs {
		out.NICs = append(out.NICs, vp.NIC{
			ID:        n.Ref,
			MAC:       n.MAC,
			NetworkID: n.NetworkRef,
			Model:     n.Model,
			Connected: n.Attached,
		})
	}
	out.SnapshotCount = len(p.backend.listSnapshots(v.Ref))
	return out
}

// normalizeFirmware maps XAPI VM platform/boot info to the contract firmware.
// HVM guests with platform firmware=uefi are UEFI; everything else is BIOS-ish
// (PV/PVHVM legacy boot).
func normalizeFirmware(v *xapiVM) vp.Firmware {
	if v.UEFI {
		return vp.FirmwareUEFI
	}
	return vp.FirmwareBIOS
}

func (p *Provider) normalizeHost(h *xapiHost) vp.Host {
	state := vp.NodeUp
	switch {
	case !h.Live:
		state = vp.NodeDown
	case !h.Enabled:
		state = vp.NodeMaintenance // host disabled => entering/in maintenance
	}
	return vp.Host{
		ID:         h.Ref,
		Name:       h.NameLabel,
		Kind:       p.kind,
		ProviderID: p.id,
		ClusterID:  p.clusterID,
		State:      state,
		CPUCores:   h.CPUCount,
		CPUMHz:     h.CPUMHz,
		MemoryMB:   h.MemoryTotB / (1 << 20),
		MemUsedMB:  (h.MemoryTotB - h.MemoryFreB) / (1 << 20),
		VMCount:    p.backend.vmsOnHost(h.Ref),
		Version:    h.Version,
	}
}

func (p *Provider) normalizeSR(s *xapiSR) vp.StoragePool {
	return vp.StoragePool{
		ID:         s.Ref,
		Name:       s.NameLabel,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       s.Type,
		CapacityGB: float64(s.PhysSizeB) / bytesPerGB,
		FreeGB:     float64(s.PhysSizeB-s.PhysUtilB) / bytesPerGB,
		HostIDs:    append([]string(nil), s.HostRefs...),
		Accessible: s.Accessible,
	}
}

func (p *Provider) normalizeNetwork(n *xapiNetwork) vp.Network {
	typ := "bridge"
	if n.VLAN > 0 {
		typ = "vlan"
	}
	return vp.Network{
		ID:         n.Ref,
		Name:       n.NameLabel,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       typ,
		VLAN:       n.VLAN,
	}
}
