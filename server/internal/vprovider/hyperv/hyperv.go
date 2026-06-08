// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// Package hyperv implements a HypervisorProvider for Microsoft Hyper-V. Per D-005
// it is split into a pure-Go, CGO-free core (this file + provider.go) that
// normalizes Hyper-V/WMI-native concepts into the vprovider contract entities, and
// a pluggable backend (wmiBackend) that the default build satisfies with an
// in-memory WMI fake (sim_backend.go). The live WMI transport (which can only run
// on Windows, via the root\virtualization\v2 WMI namespace / PowerShell Hyper-V
// cmdlets) is isolated behind the `//go:build windows` tag (live_windows.go) and is
// NOT imported by the default Linux/alpine build — keeping the CGO_ENABLED=0 image
// intact and go.mod free of any WMI dependency (route (b) from D-005).
//
// Normalization mapping (Hyper-V/WMI -> contract):
//
//	Msvm_ComputerSystem (a VM)            -> vp.VM
//	Msvm_ComputerSystem (the host role)   -> vp.Host
//	Failover Cluster (MSCluster_Cluster)  -> vp.Cluster
//	CSV / SMB 3.0 share storage           -> vp.StoragePool
//	Msvm_VirtualEthernetSwitch            -> vp.Network
//	VM checkpoints (Msvm_Snapshot)        -> vp.Snapshot
package hyperv

import (
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// enabledState mirrors the CIM/WMI Msvm_ComputerSystem.EnabledState property — the
// native integer a real Hyper-V host reports for a VM's run state. Normalization
// maps these to vp.VMState; the native token is kept verbatim in VM.StateRaw.
//
// Reference (Msvm_ComputerSystem EnabledState, root\virtualization\v2):
//
//	2     Enabled    (running)
//	3     Disabled   (stopped / off)
//	9     Paused     (Quiesced — CPU paused, e.g. low disk)
//	32768 Starting
//	32769 Saved      (Suspended — state saved to disk)
//	32770 Stopping
//	32771 Pausing
//	32773 Resuming
type enabledState int

const (
	enabledRunning   enabledState = 2     // Enabled
	enabledStopped   enabledState = 3     // Disabled
	enabledPaused    enabledState = 9     // Paused / Quiesced
	enabledStarting  enabledState = 32768 // Starting
	enabledSaved     enabledState = 32769 // Saved (Suspended)
	enabledStopping  enabledState = 32770 // Stopping
	enabledPausing   enabledState = 32771 // Pausing
	enabledResuming  enabledState = 32773 // Resuming
)

// raw returns the WMI-native EnabledState token (the integer rendered as a string,
// stored verbatim in VM.StateRaw so the inspect panel shows what WMI reported).
func (s enabledState) raw() string {
	switch s {
	case enabledRunning:
		return "2"
	case enabledStopped:
		return "3"
	case enabledPaused:
		return "9"
	case enabledStarting:
		return "32768"
	case enabledSaved:
		return "32769"
	case enabledStopping:
		return "32770"
	case enabledPausing:
		return "32771"
	case enabledResuming:
		return "32773"
	default:
		return itoa(int(s))
	}
}

// normalizeState maps a Hyper-V Msvm_ComputerSystem.EnabledState to the contract
// VMState, faithfully per the brief:
//
//   - 2 (Enabled)   -> running
//   - 3 (Disabled)  -> stopped
//   - 32769 (Saved) -> suspended
//   - 9 (Paused)    -> paused
//   - 32768 Starting / 32773 Resuming -> creating-ish transient -> running-bound;
//     we map transient "going to on" states to running and "going to off" to
//     stopped as a reasonable normalization, keeping the native token in StateRaw.
//   - anything else -> unknown
func normalizeState(s enabledState) vp.VMState {
	switch s {
	case enabledRunning:
		return vp.StateRunning
	case enabledStopped:
		return vp.StateStopped
	case enabledSaved:
		return vp.StateSuspended
	case enabledPaused, enabledPausing:
		return vp.StatePaused
	case enabledStarting, enabledResuming:
		// transient transitions toward "on": reasonable mapping -> running
		return vp.StateRunning
	case enabledStopping:
		// transient transition toward "off": reasonable mapping -> stopped
		return vp.StateStopped
	default:
		return vp.StateUnknown
	}
}

// hypervVM is a tiny subset of an Msvm_ComputerSystem managed object representing a
// VM — enough to normalize faithfully without depending on a WMI library. A real
// backend would populate this from Msvm_ComputerSystem + its associated
// Msvm_VirtualSystemSettingData / Msvm_ProcessorSettingData / Msvm_MemorySettingData.
type hypervVM struct {
	VMID      string // Msvm_ComputerSystem.Name (a GUID) -> vp.VM.ID
	Name      string // Msvm_ComputerSystem.ElementName
	State     enabledState
	HostID    string // owning host (Msvm_ComputerSystem host role / cluster node)
	ClusterID string // owning failover cluster ("" if standalone)
	VCPUs     int    // Msvm_ProcessorSettingData.VirtualQuantity
	MemoryMB  int64  // Msvm_MemorySettingData.VirtualQuantity (MB)
	GuestOS   string // Msvm_KvpExchangeComponent OSName, best effort
	Firmware  vp.Firmware
	Generation int  // Hyper-V VM generation: 1=BIOS, 2=UEFI
	Disks     []hypervDisk
	NICs      []hypervNIC
	GuestIPs  []string // Msvm_GuestNetworkAdapterConfiguration.IPAddresses
	Labels    map[string]string
	Protected bool  // e.g. a system/critical VM the UI must not delete
	Created   int64 // unix seconds (Msvm_VirtualSystemSettingData.CreationTime)
}

// hypervDisk subsets a VHDX/VHD virtual hard disk attached via a synthetic SCSI/IDE
// controller (Msvm_StorageAllocationSettingData backed by a .vhdx on a CSV/SMB path).
type hypervDisk struct {
	Index     int    // controller LUN -> Disk.ID/Label suffix
	Label     string // friendly label e.g. "Hard Drive 0"
	Path      string // HostResource e.g. "C:\\ClusterStorage\\Volume1\\vm\\vm.vhdx"
	StorageID string // owning CSV / SMB share id -> StorageID
	Format    vp.DiskFormat
	SizeBytes int64 // MaxInternalSize
}

// hypervNIC subsets a synthetic network adapter (Msvm_SyntheticEthernetPortSettingData)
// connected to an Msvm_VirtualEthernetSwitch.
type hypervNIC struct {
	Index     int
	MAC       string // Address
	SwitchID  string // connected Msvm_VirtualEthernetSwitch -> NetworkID
	Connected bool
}

// hypervHost subsets the host role of Msvm_ComputerSystem (the Hyper-V server itself)
// / a failover-cluster node (MSCluster_Node).
type hypervHost struct {
	HostID        string // computer name -> "host-NN"
	Name          string
	ClusterID     string
	NodeState     string // "Up" | "Down" | "Paused" | "Joining" (MSCluster_Node.State)
	InMaintenance bool   // node drained/paused for maintenance
	CPUCores      int
	CPUMHz        int
	MemoryBytes   int64
	MemUsedMB     int64
	Version       string // Hyper-V / Windows Server version
}

// hypervCluster subsets MSCluster_Cluster (a Windows Failover Cluster hosting
// Hyper-V roles / Clustered VMs).
type hypervCluster struct {
	ClusterID string // "cluster-NN"
	Name      string
	NodeIDs   []string
	HAEnabled bool // failover clustering provides HA by definition
}

// hypervStorage subsets a Cluster Shared Volume (CSV) or SMB 3.0 share used to store
// VM files.
type hypervStorage struct {
	StorageID     string // "csv-NN" / "smb-NN"
	Name          string
	Type          string // "csv" | "smb" | "local"
	Path          string // "C:\\ClusterStorage\\Volume1" or "\\\\fs\\share"
	CapacityBytes int64
	FreeBytes     int64
	HostIDs       []string
	Accessible    bool
}

// hypervSwitch subsets an Msvm_VirtualEthernetSwitch (External/Internal/Private).
type hypervSwitch struct {
	SwitchID string // "switch-NN" (the switch's Name GUID in reality)
	Name     string
	Type     string // "external" | "internal" | "private"
	VLAN     int
}

const bytesPerGB = 1 << 30

// normalizeVM turns a Hyper-V Msvm_ComputerSystem (VM) into the contract VM.
func (p *Provider) normalizeVM(vm *hypervVM) vp.VM {
	v := vp.VM{
		ID:          vm.VMID,
		Name:        vm.Name,
		Kind:        p.kind,
		ProviderID:  p.id,
		HostID:      vm.HostID,
		ClusterID:   vm.ClusterID,
		State:       normalizeState(vm.State),
		StateRaw:    vm.State.raw(),
		VCPUs:       vm.VCPUs,
		MemoryMB:    vm.MemoryMB,
		GuestOS:     vm.GuestOS,
		Firmware:    normalizeFirmware(vm),
		IPAddresses: append([]string(nil), vm.GuestIPs...),
		Labels:      vm.Labels,
		Protected:   vm.Protected,
	}
	if vm.Created > 0 {
		v.CreatedAt = unixUTC(vm.Created)
	}
	for _, d := range vm.Disks {
		v.Disks = append(v.Disks, vp.Disk{
			ID:         vm.VMID + "-disk-" + itoa(d.Index),
			Label:      d.Label,
			Format:     diskFormatOrDefault(d.Format),
			CapacityGB: float64(d.SizeBytes) / bytesPerGB,
			StorageID:  d.StorageID,
			Path:       d.Path,
		})
	}
	for _, n := range vm.NICs {
		v.NICs = append(v.NICs, vp.NIC{
			ID:        vm.VMID + "-nic-" + itoa(n.Index),
			MAC:       n.MAC,
			NetworkID: n.SwitchID,
			Model:     "synthetic", // Hyper-V synthetic network adapter
			Connected: n.Connected,
		})
	}
	v.SnapshotCount = len(p.backend.listSnapshots(vm.VMID))
	return v
}

// normalizeFirmware derives the contract firmware from the Hyper-V VM generation:
// Generation 2 VMs are UEFI; Generation 1 VMs are BIOS. An explicit Firmware on the
// native struct (if set) wins.
func normalizeFirmware(vm *hypervVM) vp.Firmware {
	if vm.Firmware != "" {
		return vm.Firmware
	}
	if vm.Generation >= 2 {
		return vp.FirmwareUEFI
	}
	return vp.FirmwareBIOS
}

func (p *Provider) normalizeHost(h *hypervHost) vp.Host {
	return vp.Host{
		ID:         h.HostID,
		Name:       h.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		ClusterID:  h.ClusterID,
		State:      normalizeNodeState(h),
		CPUCores:   h.CPUCores,
		CPUMHz:     h.CPUMHz,
		MemoryMB:   h.MemoryBytes / (1 << 20),
		MemUsedMB:  h.MemUsedMB,
		VMCount:    p.backend.vmsOnHost(h.HostID),
		Version:    h.Version,
	}
}

// normalizeNodeState maps an MSCluster_Node.State to the contract NodeStateKind.
func normalizeNodeState(h *hypervHost) vp.NodeStateKind {
	if h.InMaintenance {
		return vp.NodeMaintenance
	}
	switch h.NodeState {
	case "Up":
		return vp.NodeUp
	case "Down":
		return vp.NodeDown
	case "Paused":
		return vp.NodeMaintenance
	case "Joining":
		return vp.NodeDegraded
	default:
		return vp.NodeUnknown
	}
}

func (p *Provider) normalizeCluster(c *hypervCluster) vp.Cluster {
	return vp.Cluster{
		ID:         c.ClusterID,
		Name:       c.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		HostIDs:    append([]string(nil), c.NodeIDs...),
		HAEnabled:  c.HAEnabled,
		// Hyper-V has no DRS-equivalent first-class entity here (Dynamic Optimization
		// lives in SCVMM, not bare Hyper-V), so DRSEnabled stays false.
	}
}

func (p *Provider) normalizeStorage(s *hypervStorage) vp.StoragePool {
	return vp.StoragePool{
		ID:         s.StorageID,
		Name:       s.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       s.Type,
		CapacityGB: float64(s.CapacityBytes) / bytesPerGB,
		FreeGB:     float64(s.FreeBytes) / bytesPerGB,
		HostIDs:    append([]string(nil), s.HostIDs...),
		Accessible: s.Accessible,
	}
}

func (p *Provider) normalizeSwitch(s *hypervSwitch) vp.Network {
	return vp.Network{
		ID:         s.SwitchID,
		Name:       s.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       s.Type,
		VLAN:       s.VLAN,
	}
}

func (p *Provider) hostNodeState(h *hypervHost) vp.NodeState {
	nh := p.normalizeHost(h)
	return vp.NodeState{
		NodeID:    nh.ID,
		State:     nh.State,
		VMCount:   nh.VMCount,
		UpdatedAt: nowUTC(),
	}
}

func diskFormatOrDefault(f vp.DiskFormat) vp.DiskFormat {
	if f == "" {
		return vp.DiskVHDX // Hyper-V default modern disk format
	}
	return f
}
