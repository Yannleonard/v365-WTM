// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// Package kvm implements a HypervisorProvider for KVM/libvirt. Per D-005 it is
// split into a pure-Go, CGO-free core (this file + kvm.go) that normalizes
// libvirt-native data (domain state ints, domain XML, storage pools, networks)
// into the vprovider contract entities, and a pluggable backend (libvirtBackend)
// that the default build satisfies with an in-memory simulator (libvirt test://
// faked in Go, see sim_backend.go). Real libvirt transport over the RPC socket
// (pure-Go github.com/digitalocean/go-libvirt) is sketched behind the
// `//go:build libvirt_live` tag (live.go) and is NOT imported by the default
// build — keeping the distroless CGO_ENABLED=0 image intact.
package kvm

import (
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// libvirtState mirrors libvirt's virDomainState enum (the integer codes a real
// libvirt connection reports). Normalization maps these to vp.VMState.
//
// Reference (libvirt.h virDomainState):
//
//	0 VIR_DOMAIN_NOSTATE     no state
//	1 VIR_DOMAIN_RUNNING     the domain is running
//	2 VIR_DOMAIN_BLOCKED     blocked on resource
//	3 VIR_DOMAIN_PAUSED      paused by user
//	4 VIR_DOMAIN_SHUTDOWN    being shut down
//	5 VIR_DOMAIN_SHUTOFF     shut off
//	6 VIR_DOMAIN_CRASHED     crashed
//	7 VIR_DOMAIN_PMSUSPENDED suspended by guest power management
type libvirtState int

const (
	domNostate     libvirtState = 0
	domRunning     libvirtState = 1
	domBlocked     libvirtState = 2
	domPaused      libvirtState = 3
	domShutdown    libvirtState = 4
	domShutoff     libvirtState = 5
	domCrashed     libvirtState = 6
	domPMSuspended libvirtState = 7
)

// raw returns the libvirt-native state string (verbatim, for VM.StateRaw).
func (s libvirtState) raw() string {
	switch s {
	case domNostate:
		return "nostate"
	case domRunning:
		return "running"
	case domBlocked:
		return "blocked"
	case domPaused:
		return "paused"
	case domShutdown:
		return "shutdown"
	case domShutoff:
		return "shutoff"
	case domCrashed:
		return "crashed"
	case domPMSuspended:
		return "pmsuspended"
	default:
		return "unknown"
	}
}

// normalizeState maps a libvirt virDomainState int to the contract VMState.
//
//   - running/blocked  -> running   (blocked is "running but on a resource wait")
//   - shutoff/shutdown -> stopped   (shutdown is the transient toward shutoff)
//   - paused           -> paused    (libvirt "paused" == CPUs paused, RAM kept)
//   - pmsuspended      -> suspended (S3/S4 guest-PM suspend == saved/suspended)
//   - crashed          -> error
//   - nostate/default  -> unknown
func normalizeState(s libvirtState) vp.VMState {
	switch s {
	case domRunning, domBlocked:
		return vp.StateRunning
	case domShutoff, domShutdown:
		return vp.StateStopped
	case domPaused:
		return vp.StatePaused
	case domPMSuspended:
		return vp.StateSuspended
	case domCrashed:
		return vp.StateError
	default:
		return vp.StateUnknown
	}
}

// libvirtDomain is a tiny subset of a libvirt domain's parsed XML/runtime info —
// enough to normalize faithfully without depending on a libvirt SDK. A real
// backend would populate this from <domain> XML + virDomainGetInfo.
type libvirtDomain struct {
	UUID     string // libvirt domain UUID -> vp.VM.ID
	Name     string
	State    libvirtState
	HostID   string // host the domain is placed on (logical, KVM has no native host id)
	VCPUs    int    // <vcpu>
	MemoryKB int64  // <memory unit='KiB'> ; libvirt reports memory in KiB
	OSType   string // <os><type> / metadata -> GuestOS
	Firmware vp.Firmware
	// TPM requests an emulated TPM 2.0 device (<tpm model='tpm-crb'><backend
	// type='emulator' version='2.0'/></tpm>) — required for Windows 11. Backed by
	// swtpm on the libvirt host.
	TPM bool
	// SecureBoot enables UEFI Secure Boot: it forces the secure-boot OVMF firmware
	// (loader secure='yes' + the MS-keys-enrolled VARS template) and <smm state='on'/>
	// (SMM is mandatory for secure boot). Implies UEFI firmware.
	SecureBoot bool
	Disks    []libvirtDisk
	NICs     []libvirtNIC
	IPs      []string
	Labels   map[string]string
	Created  int64  // unix seconds
	BootISO  string // optional boot CD-ROM ISO path (from VMSpec.BootISO)

	// --- guest customization (cloud-init / NoCloud) ---
	// CloudInit, when non-nil, requests a NoCloud seed ISO at define time. The LIVE
	// backend builds it (buildSeedISO) and sets SeedISO to the resulting path; the
	// sim backend ignores it (cloud-init is a live-only feature).
	CloudInit *vp.CloudInitSpec
	// SeedISO is the on-disk path of the generated NoCloud 'cidata' seed ISO. When
	// set, renderDomainXML attaches it as an additional read-only cdrom so the guest
	// reads the cloud-init datasource from a CD labeled 'cidata'.
	SeedISO string
}

// libvirtDisk subsets a <disk> element.
type libvirtDisk struct {
	Target   string // dev target e.g. "vda" -> Disk.ID/Label
	Driver   string // <driver type='qcow2'> -> DiskFormat
	Source   string // file path
	Pool     string // owning storage pool name -> StorageID
	CapBytes int64  // capacity in bytes
}

// libvirtNIC subsets an <interface> element.
type libvirtNIC struct {
	MAC     string
	Network string // <source network='...'> -> NetworkID
	Model   string // <model type='virtio'>
	Link    bool   // <link state='up'>
}

// libvirtPool subsets a storage pool (virStoragePoolGetInfo + XML).
type libvirtPool struct {
	UUID       string
	Name       string
	Type       string // dir|nfs|iscsi|logical|rbd|zfs ...
	CapBytes   int64
	AvailBytes int64
	Active     bool
	Hosts      []string
}

// libvirtNet subsets a virtual network (virNetworkGetXMLDesc).
type libvirtNet struct {
	UUID   string
	Name   string
	Mode   string // bridge|nat|route|isolated ...
	VLAN   int
}

// libvirtNode subsets a libvirt node/host (virNodeGetInfo). KVM has no native
// cluster, so hosts are modeled logically by the backend.
type libvirtNode struct {
	ID       string
	Name     string
	Online   bool
	CPUs     int
	MHz      int
	MemoryKB int64
	UsedKB   int64
	Version  string
}

const bytesPerGB = 1 << 30

// normalizeDomain turns a libvirt domain into the contract VM.
func (p *Provider) normalizeDomain(d *libvirtDomain) vp.VM {
	v := vp.VM{
		ID:          d.UUID,
		Name:        d.Name,
		Kind:        p.kind,
		ProviderID:  p.id,
		HostID:      d.HostID,
		ClusterID:   p.clusterID,
		State:       normalizeState(d.State),
		StateRaw:    d.State.raw(),
		VCPUs:       d.VCPUs,
		MemoryMB:    d.MemoryKB / 1024, // KiB -> MiB
		GuestOS:     d.OSType,
		Firmware:    d.Firmware,
		IPAddresses: append([]string(nil), d.IPs...),
		Labels:      d.Labels,
	}
	if d.Created > 0 {
		v.CreatedAt = unixUTC(d.Created)
	}
	for _, dk := range d.Disks {
		v.Disks = append(v.Disks, vp.Disk{
			ID:         d.UUID + "-" + dk.Target,
			Label:      dk.Target,
			Format:     normalizeDiskFormat(dk.Driver),
			CapacityGB: float64(dk.CapBytes) / bytesPerGB,
			StorageID:  dk.Pool,
			Path:       dk.Source,
		})
	}
	for i, n := range d.NICs {
		v.NICs = append(v.NICs, vp.NIC{
			ID:        d.UUID + "-nic" + itoa(i),
			MAC:       n.MAC,
			NetworkID: n.Network,
			Model:     n.Model,
			Connected: n.Link,
		})
	}
	v.SnapshotCount = len(p.backend.listSnapshots(d.UUID))
	return v
}

// normalizeDiskFormat maps a libvirt <driver type='...'> to the contract format.
func normalizeDiskFormat(driver string) vp.DiskFormat {
	switch driver {
	case "qcow2":
		return vp.DiskQcow2
	case "raw":
		return vp.DiskRaw
	default:
		// libvirt/QEMU domains are qcow2/raw; default to qcow2.
		return vp.DiskQcow2
	}
}

func (p *Provider) normalizePool(pl *libvirtPool) vp.StoragePool {
	return vp.StoragePool{
		ID:         pl.UUID,
		Name:       pl.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       pl.Type,
		CapacityGB: float64(pl.CapBytes) / bytesPerGB,
		FreeGB:     float64(pl.AvailBytes) / bytesPerGB,
		HostIDs:    append([]string(nil), pl.Hosts...),
		Accessible: pl.Active,
	}
}

func (p *Provider) normalizeNet(n *libvirtNet) vp.Network {
	return vp.Network{
		ID:         n.UUID,
		Name:       n.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		Type:       n.Mode,
		VLAN:       n.VLAN,
	}
}

func (p *Provider) normalizeNode(n *libvirtNode) vp.Host {
	state := vp.NodeDown
	if n.Online {
		state = vp.NodeUp
	}
	return vp.Host{
		ID:         n.ID,
		Name:       n.Name,
		Kind:       p.kind,
		ProviderID: p.id,
		ClusterID:  p.clusterID,
		State:      state,
		CPUCores:   n.CPUs,
		CPUMHz:     n.MHz,
		MemoryMB:   n.MemoryKB / 1024,
		MemUsedMB:  n.UsedKB / 1024,
		VMCount:    p.backend.domainsOnHost(n.ID),
		Version:    n.Version,
	}
}
