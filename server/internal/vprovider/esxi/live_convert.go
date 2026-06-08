// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// live_convert.go translates govmomi managed-object types (mo.VirtualMachine,
// mo.HostSystem, mo.ClusterComputeResource, mo.Datastore, mo.Network) — the real
// shapes the official vSphere API returns — into the vsphere* model structs the
// pure-Go normalization core (vsphere.go) already knows how to normalize into the
// vprovider contract. No build tag: govmomi is CGO-free.
package esxi

import (
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// convertVM maps a mo.VirtualMachine (config/runtime/guest/summary) -> vsphereVM.
func convertVM(vm *mo.VirtualMachine) *vsphereVM {
	out := &vsphereVM{
		MoRef: vm.Self.Value,
		Name:  vm.Name,
		Power: powerState(vm.Runtime.PowerState),
	}
	if vm.Runtime.Host != nil {
		out.HostRef = vm.Runtime.Host.Value
	}
	if vm.Config != nil {
		out.NumCPU = int(vm.Config.Hardware.NumCPU)
		out.MemoryMB = int64(vm.Config.Hardware.MemoryMB)
		out.GuestID = vm.Config.GuestId
		switch vm.Config.Firmware {
		case string(types.GuestOsDescriptorFirmwareTypeEfi):
			out.Firmware = vp.FirmwareUEFI
		case string(types.GuestOsDescriptorFirmwareTypeBios):
			out.Firmware = vp.FirmwareBIOS
		}
		if vm.Config.CreateDate != nil {
			out.Created = vm.Config.CreateDate.Unix()
		}
		for _, dev := range vm.Config.Hardware.Device {
			switch d := dev.(type) {
			case *types.VirtualDisk:
				disk := vsphereDisk{
					Key:        int(d.Key),
					CapacityKB: d.CapacityInKB,
				}
				if d.DeviceInfo != nil {
					disk.Label = d.DeviceInfo.GetDescription().Label
				}
				if be, ok := d.Backing.(*types.VirtualDiskFlatVer2BackingInfo); ok {
					disk.VMDKPath = be.FileName
					if be.Datastore != nil {
						disk.DatastoreID = be.Datastore.Value
					}
				}
				out.Disks = append(out.Disks, disk)
			case types.BaseVirtualEthernetCard:
				card := d.GetVirtualEthernetCard()
				nic := vsphereNIC{
					Key:         int(card.Key),
					MAC:         card.MacAddress,
					AdapterType: ethernetModel(dev),
				}
				if card.Connectable != nil {
					nic.Connected = card.Connectable.Connected
				}
				switch be := card.Backing.(type) {
				case *types.VirtualEthernetCardNetworkBackingInfo:
					if be.Network != nil {
						nic.PortgroupID = be.Network.Value
					}
				case *types.VirtualEthernetCardDistributedVirtualPortBackingInfo:
					nic.PortgroupID = be.Port.PortgroupKey
				}
				out.NICs = append(out.NICs, nic)
			}
		}
	}
	if vm.Guest != nil {
		for _, n := range vm.Guest.Net {
			out.GuestIPs = append(out.GuestIPs, n.IpAddress...)
		}
	}
	return out
}

// ethernetModel reports the vSphere adapter type token for a NIC device.
func ethernetModel(dev types.BaseVirtualDevice) string {
	switch dev.(type) {
	case *types.VirtualVmxnet3:
		return "vmxnet3"
	case *types.VirtualVmxnet2:
		return "vmxnet2"
	case *types.VirtualVmxnet:
		return "vmxnet"
	case *types.VirtualE1000e:
		return "e1000e"
	case *types.VirtualE1000:
		return "e1000"
	case *types.VirtualPCNet32:
		return "pcnet32"
	case *types.VirtualSriovEthernetCard:
		return "sriov"
	default:
		return "vmxnet3"
	}
}

// convertHost maps a mo.HostSystem -> vsphereHost.
func convertHost(h *mo.HostSystem) *vsphereHost {
	out := &vsphereHost{
		MoRef:           h.Self.Value,
		Name:            h.Name,
		ConnectionState: string(h.Runtime.ConnectionState),
		InMaintenance:   h.Runtime.InMaintenanceMode,
	}
	if h.Parent != nil && h.Parent.Type == "ClusterComputeResource" {
		out.ClusterID = h.Parent.Value
	}
	if h.Hardware != nil {
		out.NumCPUCores = int(h.Hardware.CpuInfo.NumCpuCores)
		out.CPUMHz = int(h.Hardware.CpuInfo.Hz / 1_000_000)
		out.MemoryBytes = h.Hardware.MemorySize
	}
	out.MemUsedMB = int64(h.Summary.QuickStats.OverallMemoryUsage)
	if h.Config != nil {
		out.Version = h.Config.Product.FullName
	}
	if out.Version == "" && h.Summary.Config.Product != nil {
		out.Version = h.Summary.Config.Product.FullName
	}
	return out
}

// convertCluster maps a mo.ClusterComputeResource -> vsphereCluster.
func convertCluster(c *mo.ClusterComputeResource) *vsphereCluster {
	out := &vsphereCluster{
		MoRef: c.Self.Value,
		Name:  c.Name,
	}
	for _, h := range c.Host {
		out.HostIDs = append(out.HostIDs, h.Value)
	}
	if c.Configuration.DasConfig.Enabled != nil {
		out.HA = *c.Configuration.DasConfig.Enabled
	}
	if c.Configuration.DrsConfig.Enabled != nil {
		out.DRS = *c.Configuration.DrsConfig.Enabled
	}
	return out
}

// convertDatastore maps a mo.Datastore -> vsphereDatastore.
func convertDatastore(d *mo.Datastore) *vsphereDatastore {
	out := &vsphereDatastore{
		MoRef:         d.Self.Value,
		Name:          d.Summary.Name,
		Type:          d.Summary.Type,
		CapacityBytes: d.Summary.Capacity,
		FreeBytes:     d.Summary.FreeSpace,
		Accessible:    d.Summary.Accessible,
	}
	for _, m := range d.Host {
		out.HostIDs = append(out.HostIDs, m.Key.Value)
	}
	return out
}

// convertNetwork maps a mo.Network -> vsphereNetwork.
func convertNetwork(n *mo.Network) *vsphereNetwork {
	return &vsphereNetwork{
		MoRef: n.Self.Value,
		Name:  n.Name,
		Type:  "portgroup",
	}
}
