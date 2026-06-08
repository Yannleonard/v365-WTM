// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// Package esxi implements a HypervisorProvider for VMware ESXi/vSphere. Per D-005
// it is split into a pure-Go, CGO-free core (this file + esxi.go) that normalizes
// vSphere-native data (VirtualMachine, HostSystem, ClusterComputeResource,
// Datastore, port group/Network, snapshots, managed-object refs and
// poweredOn/poweredOff/suspended power states) into the vprovider contract
// entities, and a pluggable backend (vsphereBackend) that the default build
// satisfies with an in-memory simulator (the moral equivalent of vcsim, faked in
// Go — see sim_backend.go). Real vSphere transport via the pure-Go govmomi SDK is
// sketched behind the `//go:build vsphere_live` tag (live.go) and is NOT imported
// by the default build — keeping the distroless CGO_ENABLED=0 image intact and
// go.mod free of govmomi (this is route (b) from the brief).
package esxi

import (
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// powerState mirrors vSphere's VirtualMachinePowerState enum (the native string a
// real vCenter/ESXi reports via VirtualMachine.runtime.powerState). Normalization
// maps these to vp.VMState; the native token is kept verbatim in VM.StateRaw.
//
// Reference (vim25 VirtualMachinePowerState):
//
//	poweredOff  the VM is powered off
//	poweredOn   the VM is powered on
//	suspended   the VM is suspended (memory state saved)
type powerState string

const (
	powerOff       powerState = "poweredOff"
	powerOn        powerState = "poweredOn"
	powerSuspended powerState = "suspended"
)

// raw returns the vSphere-native power-state token (verbatim, for VM.StateRaw).
func (s powerState) raw() string { return string(s) }

// normalizeState maps a vSphere VirtualMachinePowerState to the contract VMState,
// faithfully per the brief:
//
//   - poweredOn  -> running
//   - poweredOff -> stopped
//   - suspended  -> suspended
//   - anything else -> unknown
func normalizeState(s powerState) vp.VMState {
	switch s {
	case powerOn:
		return vp.StateRunning
	case powerOff:
		return vp.StateStopped
	case powerSuspended:
		return vp.StateSuspended
	default:
		return vp.StateUnknown
	}
}

// vsphereVM is a tiny subset of a vSphere VirtualMachine managed object — enough to
// normalize faithfully without depending on govmomi. A real backend would populate
// this from VirtualMachine.{config,runtime,guest,summary}.
type vsphereVM struct {
	MoRef     string // managed-object ref, e.g. "vm-123" -> vp.VM.ID
	Name      string
	Power     powerState
	HostRef   string // runtime.host -> HostSystem moRef ("host-NN")
	ClusterID string // owning ClusterComputeResource moRef ("domain-cNN")
	NumCPU    int    // config.hardware.numCPU
	MemoryMB  int64  // config.hardware.memoryMB (vSphere reports memory in MB)
	GuestID   string // config.guestId / guest.guestFullName -> GuestOS
	Firmware  vp.Firmware
	Disks     []vsphereDisk
	NICs      []vsphereNIC
	GuestIPs  []string // guest.net[].ipAddress
	Labels    map[string]string
	Protected bool  // e.g. vCLS/system VM the UI must not delete
	Created   int64 // unix seconds (config.createDate)
}

// vsphereDisk subsets a VirtualDisk device backed by a .vmdk on a datastore.
type vsphereDisk struct {
	Key          int    // device key -> Disk.ID/Label suffix
	Label        string // deviceInfo.label e.g. "Hard disk 1"
	VMDKPath     string // backing.fileName e.g. "[datastore1] vm/vm.vmdk"
	DatastoreID  string // owning Datastore moRef -> StorageID
	CapacityKB   int64  // capacityInKB
}

// vsphereNIC subsets a VirtualEthernetCard device on a port group.
type vsphereNIC struct {
	Key          int
	MAC          string
	PortgroupID  string // backing port group moRef -> NetworkID
	AdapterType  string // "vmxnet3" | "e1000e" | ...
	Connected    bool   // connectable.connected
}

// vsphereHost subsets a HostSystem managed object (ESXi host).
type vsphereHost struct {
	MoRef           string // "host-NN"
	Name            string
	ClusterID       string
	ConnectionState string // "connected" | "disconnected" | "notResponding"
	InMaintenance   bool
	NumCPUCores     int     // hardware.cpuInfo.numCpuCores
	CPUMHz          int     // hardware.cpuInfo.hz -> MHz
	MemoryBytes     int64   // hardware.memorySize
	MemUsedMB       int64   // summary.quickStats.overallMemoryUsage
	Version         string  // config.product.fullName
}

// vsphereCluster subsets a ClusterComputeResource managed object.
type vsphereCluster struct {
	MoRef    string // "domain-cNN"
	Name     string
	HostIDs  []string
	HA       bool // configuration.dasConfig.enabled
	DRS      bool // configuration.drsConfig.enabled
}

// vsphereDatastore subsets a Datastore managed object.
type vsphereDatastore struct {
	MoRef        string // "datastore-NN"
	Name         string
	Type         string // "VMFS" | "NFS" | "vSAN" | ...
	CapacityBytes int64 // summary.capacity
	FreeBytes    int64  // summary.freeSpace
	HostIDs      []string
	Accessible   bool // summary.accessible
}

// vsphereNetwork subsets a Network / DistributedVirtualPortgroup managed object.
type vsphereNetwork struct {
	MoRef string // "network-NN" / "dvportgroup-NN"
	Name  string
	Type  string // "portgroup" | "dvportgroup"
	VLAN  int
}

const bytesPerGB = 1 << 30

// normalizeVM turns a vSphere VirtualMachine into the contract VM.
func (p *Provider) normalizeVM(vm *vsphereVM) vp.VM {
	v := vp.VM{
		ID:          vm.MoRef,
		Name:        vm.Name,
		Kind:        p.kind,
		ProviderID:  p.id,
		HostID:      vm.HostRef,
		ClusterID:   vm.ClusterID,
		State:       normalizeState(vm.Power),
		StateRaw:    vm.Power.raw(),
		VCPUs:       vm.NumCPU,
		MemoryMB:    vm.MemoryMB,
		GuestOS:     vm.GuestID,
		Firmware:    vm.Firmware,
		IPAddresses: append([]string(nil), vm.GuestIPs...),
		Labels:      vm.Labels,
		Protected:   vm.Protected,
	}
	if vm.Created > 0 {
		v.CreatedAt = unixUTC(vm.Created)
	}
	for _, d := range vm.Disks {
		v.Disks = append(v.Disks, vp.Disk{
			ID:         vm.MoRef + "-disk-" + itoa(d.Key),
			Label:      d.Label,
			Format:     vp.DiskVMDK, // vSphere disks are always VMDK
			CapacityGB: float64(d.CapacityKB) * 1024 / bytesPerGB,
			StorageID:  d.DatastoreID,
			Path:       d.VMDKPath,
		})
	}
	for _, n := range vm.NICs {
		v.NICs = append(v.NICs, vp.NIC{
			ID:        vm.MoRef + "-nic-" + itoa(n.Key),
			MAC:       n.MAC,
			NetworkID: n.PortgroupID,
			Model:     n.AdapterType,
			Connected: n.Connected,
		})
	}
	v.SnapshotCount = len(p.backend.listSnapshots(vm.MoRef))
	return v
}

func (p *Provider) normalizeHost(h *vsphereHost) vp.Host {
	state := vp.NodeDown
	switch {
	case h.InMaintenance:
		state = vp.NodeMaintenance
	case h.ConnectionState == "connected":
		state = vp.NodeUp
	case h.ConnectionState == "notResponding":
		state = vp.NodeDegraded
	}
	return vp.Host{
		ID:         h.MoRef,
		Name:       h.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		ClusterID:  h.ClusterID,
		State:      state,
		CPUCores:   h.NumCPUCores,
		CPUMHz:     h.CPUMHz,
		MemoryMB:   h.MemoryBytes / (1 << 20),
		MemUsedMB:  h.MemUsedMB,
		VMCount:    p.backend.vmsOnHost(h.MoRef),
		Version:    h.Version,
	}
}

func (p *Provider) normalizeCluster(c *vsphereCluster) vp.Cluster {
	return vp.Cluster{
		ID:         c.MoRef,
		Name:       c.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		HostIDs:    append([]string(nil), c.HostIDs...),
		HAEnabled:  c.HA,
		DRSEnabled: c.DRS,
	}
}

func (p *Provider) normalizeDatastore(d *vsphereDatastore) vp.StoragePool {
	return vp.StoragePool{
		ID:         d.MoRef,
		Name:       d.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       d.Type,
		CapacityGB: float64(d.CapacityBytes) / bytesPerGB,
		FreeGB:     float64(d.FreeBytes) / bytesPerGB,
		HostIDs:    append([]string(nil), d.HostIDs...),
		Accessible: d.Accessible,
	}
}

func (p *Provider) normalizeNetwork(n *vsphereNetwork) vp.Network {
	return vp.Network{
		ID:         n.MoRef,
		Name:       n.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       n.Type,
		VLAN:       n.VLAN,
	}
}

func (p *Provider) hostNodeState(h *vsphereHost) vp.NodeState {
	nh := p.normalizeHost(h)
	return vp.NodeState{
		NodeID:    nh.ID,
		State:     nh.State,
		VMCount:   nh.VMCount,
		UpdatedAt: nowUTC(),
	}
}
