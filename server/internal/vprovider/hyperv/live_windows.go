//go:build windows

// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// This file is compiled ONLY on Windows (`//go:build windows`). It is the seam where
// a real, Windows-only Hyper-V transport would be wired into the existing pure-Go
// normalization core. Per D-005 and the brief, the default (CGO_ENABLED=0, Linux/
// alpine) CI build NEVER compiles this file and the project NEVER adds any WMI
// dependency — so go.mod stays clean and the conformance suite runs cross-platform
// against the in-memory WMI fake (sim_backend.go).
//
// Hyper-V management is fundamentally Windows-only: it lives in the WMI/CIM namespace
// root\virtualization\v2 (Msvm_ComputerSystem, Msvm_VirtualSystemManagementService,
// Msvm_VirtualEthernetSwitch, Msvm_Snapshot, ...) and the MSCluster_* failover
// cluster classes. There are two realistic Windows transports, both referenced here
// in COMMENTS only (no import, no go.mod entry, no cgo):
//
//  1. WMI via a pure-Go COM/WMI binding (e.g. github.com/go-ole/go-ole +
//     github.com/StackExchange/wmi or microsoft/wmi). Query Msvm_ComputerSystem and
//     associated *SettingData, invoke Msvm_VirtualSystemManagementService methods
//     (DefineSystem, RequestStateChange, CreateSnapshot, ...).
//
//  2. PowerShell Hyper-V + FailoverClusters modules shelled out via os/exec:
//     Get-VM | ConvertTo-Json, Start-VM, Stop-VM, Suspend-VM, Checkpoint-VM,
//     Restore-VMSnapshot, Move-VM, Export-VM, Get-ClusterNode, Measure-VM. This needs
//     NO third-party dependency at all — only the stdlib os/exec + encoding/json — so
//     it is the cleanest way to keep go.mod untouched while still being real on a
//     Windows host. The sketch below outlines that route without executing it.
//
// To make this real you would implement liveBackend's methods to populate the same
// hyperv* model structs the core already normalizes (EnabledState ints, VHDX paths,
// etc.). No change to provider.go / hyperv.go is needed — only this file.
package hyperv

import (
	"errors"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// errLiveUnavailable is returned until a real WMI/PowerShell backend is wired here.
var errLiveUnavailable = errors.New("hyperv: live Windows WMI/PowerShell backend not implemented in this build")

// liveBackend would hold the Hyper-V host connection (a WMI session or the target
// computer name for PowerShell -ComputerName remoting). Stubbed.
type liveBackend struct {
	computerName string
}

// NewLive constructs a Provider backed by a live Hyper-V host (the local machine when
// computerName is "", or a remote host via WinRM/PowerShell remoting). Available only
// on Windows. The default cross-platform build uses New(...) with the in-memory fake.
//
// Sketch of the os/exec/PowerShell route (no third-party deps, go.mod untouched):
//
//	// out, err := exec.Command("powershell", "-NoProfile", "-Command",
//	//     "Get-VM | Select-Object Id,Name,State,ProcessorCount,MemoryStartup | ConvertTo-Json").Output()
//	// ... json.Unmarshal(out, &raw); map raw.State -> enabledState; populate []*hypervVM ...
func NewLive(id, computerName string, opts ...Option) (*Provider, error) {
	be := &liveBackend{computerName: computerName}
	if !be.healthy() {
		return nil, errLiveUnavailable
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

func (l *liveBackend) version() string { return "" }
func (l *liveBackend) healthy() bool   { return false } // stub: never healthy
func (l *liveBackend) close() error    { return nil }

func (l *liveBackend) listHosts() []*hypervHost               { return nil }
func (l *liveBackend) getHost(string) (*hypervHost, bool)     { return nil, false }
func (l *liveBackend) listVMs() []*hypervVM                   { return nil }
func (l *liveBackend) getVM(string) (*hypervVM, bool)         { return nil, false }
func (l *liveBackend) listClusters() []*hypervCluster         { return nil }
func (l *liveBackend) getCluster(string) (*hypervCluster, bool) { return nil, false }
func (l *liveBackend) listStorage() []*hypervStorage          { return nil }
func (l *liveBackend) listSwitches() []*hypervSwitch          { return nil }

func (l *liveBackend) createVM(*hypervVM)            {}
func (l *liveBackend) destroyVM(string)              {}
func (l *liveBackend) setState(string, enabledState) {}
func (l *liveBackend) vmsOnHost(string) int          { return 0 }

func (l *liveBackend) listSnapshots(string) []vp.Snapshot     { return nil }
func (l *liveBackend) createSnapshot(string, vp.Snapshot)     {}
func (l *liveBackend) setCurrentSnapshot(string, string) bool { return false }

var _ wmiBackend = (*liveBackend)(nil)
