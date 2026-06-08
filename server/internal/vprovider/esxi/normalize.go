// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// normalize.go holds the pure-Go mapping from vSphere managed objects (mo.*) into
// the hypervisor-agnostic vprovider contract entities, plus the small spec/value
// helpers used by esxi.go. Keeping normalization isolated makes it unit-testable
// without any client/transport.
package esxi

import (
	"fmt"
	"strings"

	"github.com/vmware/govmomi/object"
	"github.com/vmware/govmomi/vim25/mo"
	"github.com/vmware/govmomi/vim25/types"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// powerStateMap maps vSphere power states to normalized VMState.
func powerStateMap(s types.VirtualMachinePowerState) vp.VMState {
	switch s {
	case types.VirtualMachinePowerStatePoweredOn:
		return vp.StateRunning
	case types.VirtualMachinePowerStatePoweredOff:
		return vp.StateStopped
	case types.VirtualMachinePowerStateSuspended:
		return vp.StateSuspended
	default:
		return vp.StateUnknown
	}
}

// normalizeVM converts a vSphere VirtualMachine managed object into a contract VM.
func (p *Provider) normalizeVM(v *mo.VirtualMachine) vp.VM {
	out := vp.VM{
		ID:         v.Self.Value,
		Name:       v.Name,
		Kind:       vp.KindVMware,
		ProviderID: p.id,
		State:      powerStateMap(v.Runtime.PowerState),
		StateRaw:   string(v.Runtime.PowerState),
		Labels:     map[string]string{},
	}
	if v.Runtime.Host != nil {
		out.HostID = v.Runtime.Host.Value
	}
	if v.ResourcePool != nil {
		// Best-effort cluster id: the resource pool's owner is filled below from
		// summary when available.
	}
	if v.Config != nil {
		out.VCPUs = int(v.Config.Hardware.NumCPU)
		out.MemoryMB = int64(v.Config.Hardware.MemoryMB)
		out.GuestOS = normalizeGuestOS(v.Config.GuestId, v.Config.GuestFullName)
		out.Firmware = vmxToFirmware(v.Config.Firmware)
		if v.Config.CreateDate != nil {
			out.CreatedAt = v.Config.CreateDate.UTC()
		}
		out.Protected = v.Config.Template
		for k, val := range decodeLabels(v.Config.Annotation) {
			out.Labels[k] = val
		}
		out.Disks = normalizeDisks(v.Config.Hardware.Device)
		out.NICs = normalizeNICs(v.Config.Hardware.Device)
	}
	if v.Summary.Runtime.Host != nil && out.HostID == "" {
		out.HostID = v.Summary.Runtime.Host.Value
	}
	// Cluster id via the host's parent compute resource is resolved lazily by the
	// caller; for list views we leave ClusterID derived from summary if present.
	if v.Summary.Config.Template {
		out.Protected = true
	}
	// Guest IPs (best-effort via VMware tools).
	if v.Guest != nil {
		if v.Guest.IpAddress != "" {
			out.IPAddresses = append(out.IPAddresses, v.Guest.IpAddress)
		}
		for _, n := range v.Guest.Net {
			for _, ip := range n.IpAddress {
				if ip != "" && ip != v.Guest.IpAddress {
					out.IPAddresses = append(out.IPAddresses, ip)
				}
			}
		}
	}
	if v.Snapshot != nil {
		out.SnapshotCount = countSnapshots(v.Snapshot.RootSnapshotList)
	}
	if len(out.Labels) == 0 {
		out.Labels = nil
	}
	return out
}

func countSnapshots(list []types.VirtualMachineSnapshotTree) int {
	n := 0
	for i := range list {
		n++
		n += countSnapshots(list[i].ChildSnapshotList)
	}
	return n
}

func normalizeDisks(devices []types.BaseVirtualDevice) []vp.Disk {
	var out []vp.Disk
	for _, d := range devices {
		disk, ok := d.(*types.VirtualDisk)
		if !ok {
			continue
		}
		nd := vp.Disk{
			ID:         fmt.Sprintf("disk-%d", disk.Key),
			Format:     vp.DiskVMDK,
			CapacityGB: float64(disk.CapacityInKB) / (1024 * 1024),
		}
		if disk.DeviceInfo != nil {
			nd.Label = disk.DeviceInfo.GetDescription().Label
		}
		switch b := disk.Backing.(type) {
		case *types.VirtualDiskFlatVer2BackingInfo:
			nd.Path = b.FileName
			if b.Datastore != nil {
				nd.StorageID = b.Datastore.Value
			}
		case *types.VirtualDiskSparseVer2BackingInfo:
			nd.Path = b.FileName
			if b.Datastore != nil {
				nd.StorageID = b.Datastore.Value
			}
		}
		out = append(out, nd)
	}
	return out
}

func normalizeNICs(devices []types.BaseVirtualDevice) []vp.NIC {
	var out []vp.NIC
	for _, d := range devices {
		eth, ok := d.(types.BaseVirtualEthernetCard)
		if !ok {
			continue
		}
		card := eth.GetVirtualEthernetCard()
		nic := vp.NIC{
			ID:        fmt.Sprintf("nic-%d", card.Key),
			MAC:       card.MacAddress,
			Connected: card.Connectable != nil && card.Connectable.Connected,
			Model:     ethModel(d),
		}
		switch b := card.Backing.(type) {
		case *types.VirtualEthernetCardNetworkBackingInfo:
			if b.Network != nil {
				nic.NetworkID = b.Network.Value
			}
		case *types.VirtualEthernetCardDistributedVirtualPortBackingInfo:
			nic.NetworkID = b.Port.PortgroupKey
		}
		out = append(out, nic)
	}
	return out
}

func ethModel(d types.BaseVirtualDevice) string {
	switch d.(type) {
	case *types.VirtualVmxnet3:
		return "vmxnet3"
	case *types.VirtualVmxnet2:
		return "vmxnet2"
	case *types.VirtualE1000:
		return "e1000"
	case *types.VirtualE1000e:
		return "e1000e"
	default:
		return "vmxnet3"
	}
}

// normalizeHost converts a HostSystem managed object into a contract Host.
func (p *Provider) normalizeHost(h *mo.HostSystem) vp.Host {
	out := vp.Host{
		ID:         h.Self.Value,
		Name:       h.Name,
		Kind:       vp.KindVMware,
		ProviderID: p.id,
		State:      hostState(h),
		VMCount:    len(h.Vm),
	}
	if h.Parent != nil && h.Parent.Type == "ClusterComputeResource" {
		out.ClusterID = h.Parent.Value
	}
	if h.Hardware != nil {
		out.CPUCores = int(h.Hardware.CpuInfo.NumCpuCores)
		out.CPUMHz = int(h.Hardware.CpuInfo.Hz / 1_000_000)
		out.MemoryMB = h.Hardware.MemorySize / (1024 * 1024)
	}
	out.MemUsedMB = int64(h.Summary.QuickStats.OverallMemoryUsage)
	if h.Config != nil && h.Config.Product.Version != "" {
		out.Version = h.Config.Product.FullName
	} else if h.Summary.Config.Product != nil {
		out.Version = h.Summary.Config.Product.FullName
	}
	return out
}

func hostState(h *mo.HostSystem) vp.NodeStateKind {
	if h.Runtime.InMaintenanceMode {
		return vp.NodeMaintenance
	}
	switch h.Runtime.ConnectionState {
	case types.HostSystemConnectionStateConnected:
		switch h.Runtime.PowerState {
		case types.HostSystemPowerStatePoweredOn:
			return vp.NodeUp
		case types.HostSystemPowerStateStandBy:
			return vp.NodeDegraded
		default:
			return vp.NodeDown
		}
	case types.HostSystemConnectionStateDisconnected:
		return vp.NodeDown
	case types.HostSystemConnectionStateNotResponding:
		return vp.NodeDown
	default:
		return vp.NodeUnknown
	}
}

func isProtected(v *mo.VirtualMachine) bool {
	if v.Config != nil && v.Config.Template {
		return true
	}
	if v.Summary.Config.Template {
		return true
	}
	for k, val := range allLabels(v) {
		if strings.EqualFold(k, "protected") && strings.EqualFold(val, "true") {
			return true
		}
	}
	return false
}

func allLabels(v *mo.VirtualMachine) map[string]string {
	if v.Config == nil {
		return nil
	}
	return decodeLabels(v.Config.Annotation)
}

func bytesToGB(b int64) float64 { return float64(b) / (1024 * 1024 * 1024) }

func boolVal(b bool) bool { return b }

// snapshotExists reports whether snapID is present anywhere in the snapshot tree.
func snapshotExists(list []types.VirtualMachineSnapshotTree, snapID string) bool {
	for i := range list {
		if list[i].Snapshot.Value == snapID {
			return true
		}
		if snapshotExists(list[i].ChildSnapshotList, snapID) {
			return true
		}
	}
	return false
}

// --- guest OS / firmware normalization ---

func normalizeGuestOS(guestID, fullName string) string {
	id := strings.ToLower(guestID)
	switch {
	case strings.Contains(id, "win"):
		return "windows"
	case strings.Contains(id, "rhel"), strings.Contains(id, "centos"),
		strings.Contains(id, "ubuntu"), strings.Contains(id, "debian"),
		strings.Contains(id, "suse"), strings.Contains(id, "linux"):
		return "linux"
	case strings.Contains(id, "freebsd"):
		return "freebsd"
	case strings.Contains(id, "darwin"):
		return "macos"
	case fullName != "":
		return fullName
	case guestID != "":
		return guestID
	default:
		return ""
	}
}

// guestIDFor maps a normalized guest OS label to a vSphere GuestId for CreateVM.
func guestIDFor(guestOS string) string {
	switch strings.ToLower(guestOS) {
	case "windows":
		return string(types.VirtualMachineGuestOsIdentifierWindows9_64Guest)
	case "freebsd":
		return string(types.VirtualMachineGuestOsIdentifierFreebsd64Guest)
	case "linux", "":
		return string(types.VirtualMachineGuestOsIdentifierOtherLinux64Guest)
	default:
		return string(types.VirtualMachineGuestOsIdentifierOtherGuest64)
	}
}

func vmxToFirmware(fw string) vp.Firmware {
	if strings.EqualFold(fw, string(types.GuestOsDescriptorFirmwareTypeEfi)) {
		return vp.FirmwareUEFI
	}
	if fw == "" {
		return ""
	}
	return vp.FirmwareBIOS
}

func firmwareToVMX(fw vp.Firmware) string {
	switch fw {
	case vp.FirmwareUEFI:
		return string(types.GuestOsDescriptorFirmwareTypeEfi)
	case vp.FirmwareBIOS:
		return string(types.GuestOsDescriptorFirmwareTypeBios)
	default:
		return ""
	}
}

// --- label <-> annotation codec ---
//
// vSphere VMs carry a free-text Annotation. We encode contract Labels into it as
// lines "unihv.label/<key>=<value>" so they round-trip without needing the tagging
// service (which is REST/cis, not SOAP).

const labelPrefix = "unihv.label/"

func encodeLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	var b strings.Builder
	for k, v := range labels {
		fmt.Fprintf(&b, "%s%s=%s\n", labelPrefix, k, v)
	}
	return b.String()
}

func decodeLabels(annotation string) map[string]string {
	out := map[string]string{}
	for _, line := range strings.Split(annotation, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, labelPrefix) {
			continue
		}
		kv := strings.SplitN(strings.TrimPrefix(line, labelPrefix), "=", 2)
		if len(kv) == 2 {
			out[kv[0]] = kv[1]
		}
	}
	return out
}

// --- placement helpers ---

// diskStorageID returns the first explicit disk StorageID in the spec, if any.
func diskStorageID(spec vp.VMSpec) string {
	for _, d := range spec.Disks {
		if d.StorageID != "" {
			return d.StorageID
		}
	}
	return ""
}

// pickDatastore resolves a datastore by moRef id, else returns the default one.
func (p *Provider) pickDatastore(ctx context.Context, id string) (*object.Datastore, error) {
	if id != "" {
		return object.NewDatastore(p.vc, types.ManagedObjectReference{Type: "Datastore", Value: id}), nil
	}
	ds, err := p.finder.DefaultDatastore(ctx)
	if err != nil {
		return nil, mapErr(err)
	}
	return ds, nil
}

// resourcePoolForHost returns the resource pool of the host's owning compute resource.
func (p *Provider) resourcePoolForHost(ctx context.Context, h *mo.HostSystem) (*object.ResourcePool, error) {
	if h.Parent == nil {
		return p.finder.DefaultResourcePool(ctx)
	}
	var cr mo.ComputeResource
	if err := property.DefaultCollector(p.vc).RetrieveOne(ctx, *h.Parent, []string{"resourcePool"}, &cr); err != nil {
		return nil, mapErr(err)
	}
	if cr.ResourcePool == nil {
		return p.finder.DefaultResourcePool(ctx)
	}
	return object.NewResourcePool(p.vc, *cr.ResourcePool), nil
}
