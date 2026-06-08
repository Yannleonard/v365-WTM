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

// Capability bits for the optional interfaces (continue the CapabilityMatrix).
// They are defined here (not in capability.go) to keep the extension self-contained;
// being in the same package they share the same iota space is NOT required because
// these are explicit high bits chosen to never collide with the core bits in
// capability.go (which use 1<<iota for the first ~22 bits).
const (
	CapConsole      CapabilityMatrix = 1 << 32
	CapNetworkWrite CapabilityMatrix = 1 << 33
	CapStorageWrite CapabilityMatrix = 1 << 34
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
