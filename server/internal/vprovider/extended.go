package vprovider

import (
	"context"
	"io"
)

// This file defines OPTIONAL capability interfaces that a HypervisorProvider may
// additionally implement: graphical console, network write, storage/ISO write.
// They are kept separate from the core HypervisorProvider interface so providers
// can adopt them incrementally without a breaking change to the core contract, and
// the API discovers support via a type assertion + the matching CapabilityMatrix
// bit. A provider that lacks a feature simply does not implement the interface (or
// does not set the bit); the API then greys out the action pre-flight (§3.4).

// --- graphical console ---

// ConsoleKind is the remote-console protocol a provider exposes.
type ConsoleKind string

const (
	ConsoleVNC   ConsoleKind = "vnc"   // KVM/libvirt, ESXi (MKS over VNC)
	ConsoleSPICE ConsoleKind = "spice" // KVM/libvirt SPICE
	ConsoleRDP   ConsoleKind = "rdp"   // Hyper-V (VMConnect/RDP)
)

// ConsoleEndpoint describes how to reach a VM's graphical console. The API turns
// this into a browser-reachable websocket (noVNC) or an RDP hand-off. Password is
// a one-shot/short-lived console ticket where the hypervisor issues one.
type ConsoleEndpoint struct {
	Kind     ConsoleKind `json:"kind"`
	Host     string      `json:"host"`               // console host (often the hypervisor host)
	Port     int         `json:"port"`               // console TCP port (VNC/SPICE)
	Password string      `json:"password,omitempty"` // one-shot console ticket, if any
	// TLSPort, if non-zero, is a TLS-wrapped console port (SPICE/VNC over TLS).
	TLSPort int `json:"tlsPort,omitempty"`
	// Path is an optional websocket path when the hypervisor fronts the console
	// with its own websocket proxy (e.g. ESXi /ticket).
	Path string `json:"path,omitempty"`
}

// ConsoleProvider is implemented by providers that expose a graphical console.
// Requires CapConsole in the CapabilityMatrix.
type ConsoleProvider interface {
	// Console returns the connection details for a VM's graphical console.
	Console(ctx context.Context, vmID string) (*ConsoleEndpoint, error)
}

// --- network write ---

// NetworkSpec is a normalized virtual-network create request.
type NetworkSpec struct {
	Name   string `json:"name"`
	Type   string `json:"type"`             // bridge|nat|vlan|isolated|portgroup
	Bridge string `json:"bridge,omitempty"` // host bridge name (bridge type)
	VLAN   int    `json:"vlan,omitempty"`
	CIDR   string `json:"cidr,omitempty"`   // for nat/managed networks (e.g. 192.168.50.0/24)
	HostID string `json:"hostId,omitempty"`
}

// NetworkWriter is implemented by providers that can create/delete virtual
// networks/switches. Requires CapNetworkWrite.
type NetworkWriter interface {
	CreateNetwork(ctx context.Context, spec NetworkSpec) (*Task, error)
	DeleteNetwork(ctx context.Context, networkID string) (*Task, error)
}

// --- storage / image / ISO write ---

// VolumeSpec is a normalized disk-volume create request.
type VolumeSpec struct {
	Name       string     `json:"name"`
	StorageID  string     `json:"storageId"` // owning pool/datastore
	CapacityGB float64    `json:"capacityGb"`
	Format     DiskFormat `json:"format,omitempty"`
}

// Volume is a normalized storage volume / disk image / ISO in a pool.
type Volume struct {
	ID         string     `json:"id"`
	Name       string     `json:"name"`
	StorageID  string     `json:"storageId"`
	Format     DiskFormat `json:"format,omitempty"`
	CapacityGB float64    `json:"capacityGb"`
	AllocGB    float64    `json:"allocGb"`
	IsISO      bool       `json:"isIso"`
	Path       string     `json:"path,omitempty"`
}

// StorageProvider is implemented by providers that can list/create/delete volumes
// and upload ISO images. Requires CapStorageWrite (writes) / CapListStorage (list).
type StorageProvider interface {
	// ListVolumes returns the volumes (disks + ISOs) in a storage pool.
	ListVolumes(ctx context.Context, storageID string) ([]Volume, error)
	// CreateVolume provisions a new disk volume.
	CreateVolume(ctx context.Context, spec VolumeSpec) (*Task, error)
	// DeleteVolume removes a volume.
	DeleteVolume(ctx context.Context, storageID, volumeID string) (*Task, error)
	// UploadISO streams an ISO image into a pool under name; returns the volume.
	UploadISO(ctx context.Context, storageID, name string, size int64, r io.Reader) (*Volume, error)
}

// --- hot-plug device management (live attach/detach on a RUNNING VM) ---

// DeviceManager is implemented by providers that can hot-add / hot-remove devices
// (disks, NICs) and insert/eject ISO media on a RUNNING VM with NO reboot. The
// changes apply to the live instance AND persist to the domain config so they
// survive a power cycle. Requires CapHotPlug.
//
// Disks/NICs are described with the SAME normalized DiskSpec/NICSpec the create
// path uses. Detach is by the normalized device id (Disk.ID / NIC.ID as surfaced
// by GetVM). MountISO inserts media into the VM's existing CD-ROM; UnmountISO
// ejects it (leaving the empty CD-ROM in place).
type DeviceManager interface {
	// AttachDisk hot-attaches a disk to a running VM. A size-only spec (no
	// SourcePath) provisions a backing volume first.
	AttachDisk(ctx context.Context, vmID string, spec DiskSpec) (*Task, error)
	// DetachDisk hot-removes the disk identified by diskID.
	DetachDisk(ctx context.Context, vmID, diskID string) (*Task, error)
	// AttachNIC hot-attaches a virtual NIC to a running VM.
	AttachNIC(ctx context.Context, vmID string, spec NICSpec) (*Task, error)
	// DetachNIC hot-removes the NIC identified by nicID.
	DetachNIC(ctx context.Context, vmID, nicID string) (*Task, error)
	// MountISO inserts the ISO at isoPath into the VM's CD-ROM (no reboot).
	MountISO(ctx context.Context, vmID, isoPath string) (*Task, error)
	// UnmountISO ejects the media from the VM's CD-ROM.
	UnmountISO(ctx context.Context, vmID string) (*Task, error)
}

// --- guest agent (qemu-ga) ---

// GuestInfo is the in-guest view a running qemu-guest-agent reports through
// libvirt (DomainGetGuestInfo + DomainInterfaceAddresses source=AGENT). It is
// best-effort: AgentConnected is false (and the rest may be empty) when the agent
// is not installed/running in the guest — the caller MUST treat its absence as a
// soft, non-error condition (the prompt's "fall back silently" requirement).
type GuestInfo struct {
	// AgentConnected reports whether the qemu-guest-agent answered at all.
	AgentConnected bool `json:"agentConnected"`
	// Hostname is the guest's reported hostname ("" if unknown).
	Hostname string `json:"hostname,omitempty"`
	// OS is the guest OS pretty-name/name as the agent reports it ("" if unknown).
	OS string `json:"os,omitempty"`
	// IPAddresses are the guest-reported IPs (loopback filtered out).
	IPAddresses []string `json:"ipAddresses,omitempty"`
	// Note carries a human-readable explanation when the agent is absent or partial.
	Note string `json:"note,omitempty"`
}

// GuestAgentProvider is implemented by providers that can query a running
// in-guest agent (KVM: qemu-guest-agent over libvirt). Requires CapGuestAgent.
// GuestInfo NEVER returns ErrUnsupported for "agent not connected" — it returns a
// GuestInfo with AgentConnected=false so the UI can show the soft state.
type GuestAgentProvider interface {
	// GuestInfo returns the in-guest hostname/OS/IPs reported by the guest agent.
	GuestInfo(ctx context.Context, vmID string) (*GuestInfo, error)
}

// --- online disk resize ---

// DiskResizer is implemented by providers that can grow a VM's virtual disk
// online (KVM: DomainBlockResize on the running domain's block device, growing the
// underlying volume first where needed). Shrink is rejected with ErrInvalidSpec.
// Requires CapDiskResize.
type DiskResizer interface {
	// ResizeDisk grows the disk identified by diskID to newCapacityGB. Shrinking is
	// not permitted (returns ErrInvalidSpec).
	ResizeDisk(ctx context.Context, vmID, diskID string, newCapacityGB float64) (*Task, error)
}

// --- snapshot management (delete-single, tree) ---

// SnapshotManager is implemented by providers that can delete an INDIVIDUAL
// snapshot (KVM: DomainSnapshotDelete). The core ListSnapshots already returns the
// tree fields (ParentID/IsCurrent/HasMemory/CreatedAt). Requires CapSnapshot.
type SnapshotManager interface {
	// DeleteSnapshot removes a single snapshot by id (its children are re-parented
	// by the hypervisor; the disk chain is consolidated as the engine sees fit).
	DeleteSnapshot(ctx context.Context, vmID, snapID string) (*Task, error)
}

// Capability bits for the optional interfaces (continue the CapabilityMatrix).
// They are defined here (not in capability.go) to keep the extension self-contained;
// being in the same package they share the same iota space is NOT required because
// these are explicit high bits chosen to never collide with the core bits in
// capability.go (which use 1<<iota for the first ~22 bits).
const (
	CapConsole      CapabilityMatrix = 1 << 32
	CapNetworkWrite CapabilityMatrix = 1 << 33
	CapStorageWrite CapabilityMatrix = 1 << 34
	// CapHotPlug declares live hot-add/hot-remove of disks/NICs and ISO mount/eject
	// on a RUNNING VM (DeviceManager).
	CapHotPlug CapabilityMatrix = 1 << 35
	// CapGuestAgent declares an in-guest agent integration (qemu-guest-agent over
	// libvirt): guest IPs/hostname/OS, agent-driven graceful shutdown, quiesced
	// (app-consistent) snapshots (GuestAgentProvider).
	CapGuestAgent CapabilityMatrix = 1 << 36
	// CapDiskResize declares online (live) virtual-disk grow (DiskResizer).
	CapDiskResize CapabilityMatrix = 1 << 37
)

// extTokens maps the extension capability bits to wire tokens (appended to the
// core Strings() output by the API layer when present).
var extTokens = []struct {
	bit   CapabilityMatrix
	token string
}{
	{CapConsole, "console"},
	{CapNetworkWrite, "network_write"},
	{CapStorageWrite, "storage_write"},
	{CapHotPlug, "hotplug"},
	{CapGuestAgent, "guest_agent"},
	{CapDiskResize, "disk_resize"},
}

// ExtStrings returns the active EXTENSION capability tokens (console/network_write/
// storage_write). The API concatenates these with the core Strings().
func (c CapabilityMatrix) ExtStrings() []string {
	out := make([]string, 0, len(extTokens))
	for _, t := range extTokens {
		if c.Has(t.bit) {
			out = append(out, t.token)
		}
	}
	return out
}
