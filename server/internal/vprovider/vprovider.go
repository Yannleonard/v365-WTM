// Package vprovider defines the single, hypervisor-agnostic seam the UniHV API
// and unified inventory talk to, regardless of which hypervisor is behind it
// (KVM/libvirt, Microsoft Hyper-V, Xen/XAPI, VMware ESXi/vSphere). It is the VM
// counterpart of the container-domain provider package (see ADR-UNIHV-002), built
// with the same proven patterns: a declarative Capability matrix, ErrUnsupported
// for unsupported operations, a Registry, and normalized entities.
//
// This package MUST NOT import any hypervisor SDK (libvirt, govmomi, WMI, XAPI).
// Concrete implementations live in subpackages vprovider/kvm, vprovider/hyperv,
// vprovider/xen and vprovider/esxi. The conformance suite in vprovider/conformance
// validates ANY implementation against this contract.
package vprovider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

// ErrUnsupported is returned by methods a hypervisor genuinely cannot perform
// (declared via the absence of the matching Capability bit). The API maps it to
// HTTP 405. No action must ever fail silently because a capability is absent
// (prompt §3.4): the UI greys it out up-front using CapabilityMatrix.
var ErrUnsupported = errors.New("vprovider: operation not supported by this hypervisor")

// ErrNotFound is returned when an entity id is unknown to the provider. HTTP 404.
var ErrNotFound = errors.New("vprovider: entity not found")

// ErrConflict is returned when an operation conflicts with current state (e.g.
// deleting a running VM without force, reverting a snapshot on a running VM where
// the hypervisor forbids it). HTTP 409.
var ErrConflict = errors.New("vprovider: operation conflicts with current state")

// ErrInvalidSpec is returned when a create/reconfigure spec is invalid for this
// hypervisor (e.g. a guest OS or firmware the backend cannot provide). HTTP 422.
var ErrInvalidSpec = errors.New("vprovider: invalid specification for this hypervisor")

// HypervisorKind identifies the engine behind a Provider.
type HypervisorKind string

const (
	KindKVM    HypervisorKind = "kvm"    // libvirt/QEMU
	KindHyperV HypervisorKind = "hyperv" // Microsoft Hyper-V (WMI/CIM)
	KindXen    HypervisorKind = "xen"    // Xen via XAPI (XenServer / XCP-ng / Xen Orchestra)
	KindVMware HypervisorKind = "vmware" // VMware ESXi / vSphere (SOAP/REST, govmomi)
)

// PowerOp is a VM power transition requested through PowerOp.
type PowerOp string

const (
	PowerStart   PowerOp = "start"
	PowerStop    PowerOp = "stop"    // graceful guest shutdown where supported, else power-off
	PowerReset   PowerOp = "reset"   // hard reset
	PowerSuspend PowerOp = "suspend" // save state / pause
	PowerResume  PowerOp = "resume"  // resume from suspend
)

// Valid reports whether op is a known power operation.
func (op PowerOp) Valid() bool {
	switch op {
	case PowerStart, PowerStop, PowerReset, PowerSuspend, PowerResume:
		return true
	}
	return false
}

// HypervisorProvider is the single seam the API/inventory layer talks to,
// regardless of hypervisor. It mirrors the prompt §3.2 contract. Read methods
// MUST be implemented by every provider; capability-gated methods MUST return
// ErrUnsupported when the corresponding Capability bit is not set.
type HypervisorProvider interface {
	// --- identity, connection & health ---

	// Kind returns the hypervisor family.
	Kind() HypervisorKind
	// ID is the stable provider-instance id (e.g. "kvm-lab1"). Every entity's
	// ProviderID equals this, making multi-host aggregation trivial.
	ID() string
	// Capabilities returns the declarative matrix the UI uses to grey out actions.
	Capabilities() CapabilityMatrix
	// HealthCheck verifies connectivity/health of the underlying hypervisor.
	HealthCheck(ctx context.Context) (HealthStatus, error)
	// Close releases the underlying client/connection.
	Close() error

	// --- inventory (read) ---

	ListHosts(ctx context.Context) ([]Host, error)
	ListVMs(ctx context.Context, opts ListOptions) ([]VM, error)
	GetVM(ctx context.Context, id string) (*VMDetail, error)
	ListClusters(ctx context.Context) ([]Cluster, error)
	ListStorage(ctx context.Context) ([]StoragePool, error)
	ListNetworks(ctx context.Context) ([]Network, error)

	// --- VM lifecycle ---

	CreateVM(ctx context.Context, spec VMSpec) (*Task, error)
	PowerOp(ctx context.Context, vmID string, op PowerOp) (*Task, error)
	DeleteVM(ctx context.Context, vmID string, opts DeleteOptions) (*Task, error)
	ReconfigureVM(ctx context.Context, vmID string, spec VMReconfigureSpec) (*Task, error)

	// --- snapshots & clones ---

	Snapshot(ctx context.Context, vmID string, opts SnapshotOptions) (*Task, error)
	RevertSnapshot(ctx context.Context, vmID, snapID string) (*Task, error)
	ListSnapshots(ctx context.Context, vmID string) ([]Snapshot, error)
	Clone(ctx context.Context, vmID string, spec CloneSpec) (*Task, error)

	// --- migration ---

	// MigrateVM performs intra-hypervisor live/cold migration to targetHost.
	MigrateVM(ctx context.Context, vmID, targetHost string, opts MigrateOptions) (*Task, error)
	// ExportVM streams the VM's disk(s) in the given format for cross-hypervisor
	// V2V (consumed by internal/migrate). The caller closes the reader.
	ExportVM(ctx context.Context, vmID string, format DiskFormat) (io.ReadCloser, *ExportInfo, error)

	// --- cluster & HA ---

	GetClusterTopology(ctx context.Context, clusterID string) (*Topology, error)
	NodeState(ctx context.Context, nodeID string) (*NodeState, error)

	// --- observability ---

	// GetMetrics returns a metric series for an entity over a time window.
	GetMetrics(ctx context.Context, entityID string, window MetricWindow) (*MetricSeries, error)
	// StreamEvents streams hypervisor events until ctx is cancelled.
	StreamEvents(ctx context.Context) (<-chan Event, error)
}

// HealthStatus is the result of HealthCheck.
type HealthStatus struct {
	Healthy   bool      `json:"healthy"`
	Message   string    `json:"message,omitempty"`
	Version   string    `json:"version,omitempty"` // hypervisor version string
	CheckedAt time.Time `json:"checkedAt"`
}

// ListOptions filters ListVMs. All fields optional.
type ListOptions struct {
	HostID    string            // restrict to one host ("" = all)
	ClusterID string            // restrict to one cluster
	State     VMState           // restrict to one state ("" = any)
	Labels    map[string]string // tag/annotation equality filters
}

// DeleteOptions controls DeleteVM.
type DeleteOptions struct {
	Force       bool // delete even if running (power off first)
	DeleteDisks bool // also delete the backing disks
}

// SnapshotOptions controls Snapshot.
type SnapshotOptions struct {
	Name        string
	Description string
	Memory      bool // include live memory state (where supported)
	Quiesce     bool // quiesce the guest FS (needs guest tools)
}

// MigrateOptions controls MigrateVM.
type MigrateOptions struct {
	Live          bool   // live migrate (else cold)
	TargetStorage string // optional target storage pool/datastore id
}

// MigrateMetadata is non-stream metadata returned alongside an export.
type ExportInfo struct {
	Format      DiskFormat `json:"format"`
	SizeBytes   int64      `json:"sizeBytes"`   // 0 if unknown/streamed
	DiskCount   int        `json:"diskCount"`
	SourceVMID  string     `json:"sourceVmId"`
	GuestOS     string     `json:"guestOs,omitempty"`
	Firmware    Firmware   `json:"firmware,omitempty"`
}

// MetricWindow selects a time range and resolution for GetMetrics.
type MetricWindow struct {
	Since      time.Time
	Until      time.Time // zero = now
	StepSecond int       // sample step; 0 = provider default
}

// VMDetail is GetVM's full payload: normalized header + opaque engine JSON.
type VMDetail struct {
	VM
	// Raw is the hypervisor-native object marshalled to JSON, shown verbatim in
	// the inspect panel. Opaque to the API/UI.
	Raw json.RawMessage `json:"raw,omitempty"`
}
