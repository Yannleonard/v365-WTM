package vprovider

import (
	"encoding/json"
	"time"
)

// VMState is the normalized lifecycle state across hypervisors.
type VMState string

const (
	StateRunning   VMState = "running"
	StateStopped   VMState = "stopped"
	StateSuspended VMState = "suspended"
	StatePaused    VMState = "paused"
	StateCreating  VMState = "creating"
	StateMigrating VMState = "migrating"
	StateError     VMState = "error"
	StateUnknown   VMState = "unknown"
)

// Firmware is the normalized VM firmware type.
type Firmware string

const (
	FirmwareBIOS Firmware = "bios"
	FirmwareUEFI Firmware = "uefi"
)

// DiskFormat is a normalized virtual-disk format (used by export + V2V).
type DiskFormat string

const (
	DiskVMDK  DiskFormat = "vmdk"  // VMware
	DiskQcow2 DiskFormat = "qcow2" // KVM/QEMU
	DiskRaw   DiskFormat = "raw"   // KVM/QEMU
	DiskVHDX  DiskFormat = "vhdx"  // Hyper-V
	DiskVHD   DiskFormat = "vhd"   // Hyper-V (legacy)
)

// Valid reports whether f is a known disk format.
func (f DiskFormat) Valid() bool {
	switch f {
	case DiskVMDK, DiskQcow2, DiskRaw, DiskVHDX, DiskVHD:
		return true
	}
	return false
}

// VM is the unified, hypervisor-agnostic virtual-machine shape the API, unified
// inventory and UI consume. One VM == one Hyper-V VM | one KVM domain | one Xen
// VM | one vSphere VirtualMachine.
type VM struct {
	// ID is the provider-native id (libvirt uuid, vSphere moRef, Hyper-V VMId,
	// Xen opaque ref). Unique within (ProviderID).
	ID string `json:"id"`
	// Name is the human-friendly VM name.
	Name string `json:"name"`
	// Kind is the hypervisor family this VM came from.
	Kind HypervisorKind `json:"kind"`
	// ProviderID is the provider instance that owns this VM. Equals Provider.ID().
	ProviderID string `json:"providerId"`
	// HostID is the host the VM currently runs on ("" if undefined/clustered-floating).
	HostID string `json:"hostId,omitempty"`
	// ClusterID is the cluster the VM belongs to ("" if standalone host).
	ClusterID string `json:"clusterId,omitempty"`

	State    VMState `json:"state"`
	StateRaw string  `json:"stateRaw,omitempty"` // engine-native state string, verbatim

	// Hardware (normalized).
	VCPUs     int    `json:"vcpus"`
	MemoryMB  int64  `json:"memoryMb"`
	GuestOS   string `json:"guestOs,omitempty"`  // best-effort normalized guest OS label
	Firmware  Firmware `json:"firmware,omitempty"`

	// Disks & NICs (summary; full detail in VMDetail.Raw).
	Disks []Disk `json:"disks,omitempty"`
	NICs  []NIC  `json:"nics,omitempty"`

	// IPAddresses are guest IPs reported by tools/agent (best-effort).
	IPAddresses []string `json:"ipAddresses,omitempty"`

	// Labels are merged tags/annotations used by the UI for grouping/filtering.
	Labels map[string]string `json:"labels,omitempty"`

	// SnapshotCount is the number of snapshots (0 if none/unknown).
	SnapshotCount int `json:"snapshotCount"`

	CreatedAt time.Time `json:"createdAt,omitempty"`

	// Protected marks system/critical VMs the UI must not delete by accident
	// (defense-in-depth with RBAC), mirroring Castor's Workload.Protected.
	Protected bool `json:"protected"`
}

// Disk is a normalized virtual disk attached to a VM.
type Disk struct {
	ID         string     `json:"id"`
	Label      string     `json:"label,omitempty"`
	Format     DiskFormat `json:"format"`
	CapacityGB float64    `json:"capacityGb"`
	StorageID  string     `json:"storageId,omitempty"` // owning StoragePool id
	Path       string     `json:"path,omitempty"`
}

// NIC is a normalized virtual network interface.
type NIC struct {
	ID        string `json:"id"`
	MAC       string `json:"mac,omitempty"`
	NetworkID string `json:"networkId,omitempty"` // owning Network id
	Model     string `json:"model,omitempty"`
	Connected bool   `json:"connected"`
}

// Host is a normalized hypervisor host (physical node running VMs).
type Host struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        HypervisorKind `json:"kind"`
	ProviderID  string         `json:"providerId"`
	ClusterID   string         `json:"clusterId,omitempty"`
	State       NodeStateKind  `json:"state"`
	CPUCores    int            `json:"cpuCores"`
	CPUMHz      int            `json:"cpuMhz,omitempty"`
	MemoryMB    int64          `json:"memoryMb"`
	MemUsedMB   int64          `json:"memUsedMb,omitempty"`
	VMCount     int            `json:"vmCount"`
	Version     string         `json:"version,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// Cluster is a normalized cluster of hypervisor hosts.
type Cluster struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       HypervisorKind `json:"kind"`
	ProviderID string         `json:"providerId"`
	HostIDs    []string       `json:"hostIds"`
	HAEnabled  bool           `json:"haEnabled"`
	DRSEnabled bool           `json:"drsEnabled,omitempty"` // vSphere DRS / equivalent
}

// StoragePool is a normalized datastore / storage pool.
type StoragePool struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Kind        HypervisorKind `json:"kind"`
	ProviderID  string         `json:"providerId"`
	Type        string         `json:"type,omitempty"` // nfs|iscsi|local|ceph|zfs|vmfs|...
	CapacityGB  float64        `json:"capacityGb"`
	FreeGB      float64        `json:"freeGb"`
	HostIDs     []string       `json:"hostIds,omitempty"`
	Accessible  bool           `json:"accessible"`
}

// Network is a normalized virtual network / port group.
type Network struct {
	ID         string         `json:"id"`
	Name       string         `json:"name"`
	Kind       HypervisorKind `json:"kind"`
	ProviderID string         `json:"providerId"`
	Type       string         `json:"type,omitempty"` // bridge|nat|vlan|portgroup|...
	VLAN       int            `json:"vlan,omitempty"`
}

// Snapshot is a normalized VM snapshot.
type Snapshot struct {
	ID          string    `json:"id"`
	VMID        string    `json:"vmId"`
	Name        string    `json:"name"`
	Description string    `json:"description,omitempty"`
	ParentID    string    `json:"parentId,omitempty"`
	HasMemory   bool      `json:"hasMemory"`
	IsCurrent   bool      `json:"isCurrent"`
	CreatedAt   time.Time `json:"createdAt"`
}

// --- specs (write operations) ---

// VMSpec is the normalized create-VM specification.
type VMSpec struct {
	Name      string            `json:"name"`
	HostID    string            `json:"hostId,omitempty"`
	ClusterID string            `json:"clusterId,omitempty"`
	VCPUs     int               `json:"vcpus"`
	MemoryMB  int64             `json:"memoryMb"`
	GuestOS   string            `json:"guestOs,omitempty"`
	Firmware  Firmware          `json:"firmware,omitempty"`
	Disks     []DiskSpec        `json:"disks"`
	NICs      []NICSpec         `json:"nics,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	BootISO   string            `json:"bootIso,omitempty"` // optional install ISO path/id

	// --- security/firmware (vTPM + Secure Boot) ---
	// TPM requests an emulated TPM 2.0 device (swtpm) — required for Windows 11.
	TPM bool `json:"tpm,omitempty"`
	// SecureBoot enables UEFI Secure Boot (implies UEFI firmware + signed OVMF).
	SecureBoot bool `json:"secureBoot,omitempty"`

	// --- guest customization (cloud-init / NoCloud) ---
	// CloudInit, when non-nil, generates a NoCloud seed ISO (user-data + meta-data)
	// attached to the VM so a cloud-init-enabled guest image self-configures on boot.
	CloudInit *CloudInitSpec `json:"cloudInit,omitempty"`

	// --- CPU topology / model (vSphere "CPU" advanced) ---
	// CPU, when non-nil, sets explicit sockets/cores/threads + an optional CPU model
	// (instead of a flat vCPU count). Improves guest perf and live-migrate/EVC compat.
	CPU *CPUSpec `json:"cpu,omitempty"`

	// --- templates / customization ---
	// IsTemplate marks the created VM as a TEMPLATE (golden image for clone-from-
	// template; not run as-is). For libvirt this is domain metadata + a label.
	IsTemplate bool `json:"isTemplate,omitempty"`
	// Sysprep, when non-nil, requests Windows guest customization (autounattend.xml
	// seed) — the Windows analogue of cloud-init.
	Sysprep *SysprepSpec `json:"sysprep,omitempty"`
}

// CPUSpec is the normalized CPU topology + model.
type CPUSpec struct {
	Sockets        int    `json:"sockets,omitempty"`
	CoresPerSocket int    `json:"coresPerSocket,omitempty"`
	ThreadsPerCore int    `json:"threadsPerCore,omitempty"`
	Model          string `json:"model,omitempty"` // "" = hypervisor default (e.g. host-passthrough)
}

// SysprepSpec is normalized Windows guest customization (autounattend.xml).
type SysprepSpec struct {
	ComputerName     string `json:"computerName,omitempty"`
	AdminPassword    string `json:"adminPassword,omitempty"`
	ProductKey       string `json:"productKey,omitempty"`
	OrgName          string `json:"orgName,omitempty"`
	TimeZone         string `json:"timeZone,omitempty"`
	Locale           string `json:"locale,omitempty"`
	UnattendXMLExtra string `json:"unattendXmlExtra,omitempty"` // advanced: raw extra
}

// CloudInitSpec is normalized guest customization applied via a NoCloud seed.
type CloudInitSpec struct {
	Hostname       string   `json:"hostname,omitempty"`
	Username       string   `json:"username,omitempty"`
	Password       string   `json:"password,omitempty"` // plaintext; provider hashes/seeds it
	SSHAuthorizedKeys []string `json:"sshAuthorizedKeys,omitempty"`
	// RunCmd is an optional list of shell commands run on first boot.
	RunCmd []string `json:"runCmd,omitempty"`
	// NetworkConfig is optional raw cloud-init network-config v2 YAML ("" = DHCP).
	NetworkConfig string `json:"networkConfig,omitempty"`
	// UserDataExtra is optional raw extra #cloud-config appended (advanced).
	UserDataExtra string `json:"userDataExtra,omitempty"`
}

// DiskSpec is a disk to create/attach.
type DiskSpec struct {
	CapacityGB float64    `json:"capacityGb"`
	Format     DiskFormat `json:"format,omitempty"` // "" = hypervisor default
	StorageID  string     `json:"storageId,omitempty"`
	SourcePath string     `json:"sourcePath,omitempty"` // import an existing disk image
}

// NICSpec is a NIC to create/attach.
type NICSpec struct {
	NetworkID string `json:"networkId"`
	Model     string `json:"model,omitempty"`
	MAC       string `json:"mac,omitempty"`
}

// VMReconfigureSpec is a partial reconfigure; nil fields are left unchanged.
type VMReconfigureSpec struct {
	VCPUs    *int     `json:"vcpus,omitempty"`
	MemoryMB *int64   `json:"memoryMb,omitempty"`
	AddDisks []DiskSpec `json:"addDisks,omitempty"`
	AddNICs  []NICSpec  `json:"addNics,omitempty"`
	Labels   map[string]string `json:"labels,omitempty"`
}

// CloneSpec is the normalized clone specification.
type CloneSpec struct {
	Name      string `json:"name"`
	HostID    string `json:"hostId,omitempty"`
	StorageID string `json:"storageId,omitempty"`
	Linked    bool   `json:"linked"` // linked clone where supported, else full
	PowerOn   bool   `json:"powerOn"`
}

// --- cluster/HA observability ---

// NodeStateKind is the normalized health/availability of a host/node.
type NodeStateKind string

const (
	NodeUp          NodeStateKind = "up"
	NodeDown        NodeStateKind = "down"
	NodeMaintenance NodeStateKind = "maintenance"
	NodeDegraded    NodeStateKind = "degraded"
	NodeUnknown     NodeStateKind = "unknown"
)

// NodeState is the detailed state of one node.
type NodeState struct {
	NodeID    string        `json:"nodeId"`
	State     NodeStateKind `json:"state"`
	Message   string        `json:"message,omitempty"`
	VMCount   int           `json:"vmCount"`
	UpdatedAt time.Time     `json:"updatedAt"`
}

// Topology is a cluster's node/placement topology.
type Topology struct {
	ClusterID string         `json:"clusterId"`
	Nodes     []NodeState    `json:"nodes"`
	Placement map[string]string `json:"placement,omitempty"` // vmId -> nodeId
}

// --- metrics & events ---

// MetricSeries is a normalized metric series for one entity.
type MetricSeries struct {
	EntityID string         `json:"entityId"`
	Samples  []MetricSample `json:"samples"`
}

// MetricSample is one normalized resource sample (VM or host), aligned with the
// container-domain StatSample so the unified monitoring layer treats both alike.
type MetricSample struct {
	Timestamp     time.Time `json:"timestamp"`
	CPUPercent    float64   `json:"cpuPercent"`
	MemUsageBytes uint64    `json:"memUsageBytes"`
	MemLimitBytes uint64    `json:"memLimitBytes"`
	NetRxBytes    uint64    `json:"netRxBytes"`
	NetTxBytes    uint64    `json:"netTxBytes"`
	DiskReadBytes uint64    `json:"diskReadBytes"`
	DiskWriteBytes uint64   `json:"diskWriteBytes"`
}

// EventKind classifies a hypervisor event.
type EventKind string

const (
	EventVMStateChanged EventKind = "vm.state"
	EventVMCreated      EventKind = "vm.created"
	EventVMDeleted      EventKind = "vm.deleted"
	EventTaskUpdated    EventKind = "task.updated"
	EventHostStateChanged EventKind = "host.state"
	EventAlert          EventKind = "alert"
)

// Event is a normalized hypervisor event streamed by StreamEvents.
type Event struct {
	Kind       EventKind       `json:"kind"`
	ProviderID string          `json:"providerId"`
	EntityID   string          `json:"entityId,omitempty"`
	Message    string          `json:"message"`
	Timestamp  time.Time       `json:"timestamp"`
	Data       json.RawMessage `json:"data,omitempty"`
}
