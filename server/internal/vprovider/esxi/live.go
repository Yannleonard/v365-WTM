//go:build vsphere_live

// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// This file is compiled ONLY under `-tags vsphere_live`. It is the seam where a
// real, pure-Go govmomi vSphere client would be wired into the existing pure-Go
// normalization core. Per D-005 and the brief, the default (CGO_ENABLED=0,
// distroless) build never compiles this file and the project NEVER imports govmomi
// — so go.mod stays clean and govmomi-free. This file references govmomi
// CONCEPTUALLY in comments only; it adds no import and no go.mod entry.
//
// To make this real you would (under the vsphere_live tag and only then, after
// adding govmomi to go.mod):
//
//	import (
//	    "github.com/vmware/govmomi"        // pure-Go vSphere SDK (no cgo)
//	    "github.com/vmware/govmomi/view"   // ContainerView for inventory walks
//	    "github.com/vmware/govmomi/vim25/mo"   // managed-object types
//	    "github.com/vmware/govmomi/vim25/types"
//	)
//
// and implement liveBackend's methods against a *govmomi.Client connected via
// govmomi.NewClient(ctx, sdkURL, insecure), using a property.Collector /
// view.ContainerView to retrieve mo.VirtualMachine, mo.HostSystem,
// mo.ClusterComputeResource, mo.Datastore and mo.Network, then translating those
// (runtime.powerState, config.hardware, summary.*) into the vsphere* model structs
// the core already normalizes. No change to esxi.go / vsphere.go is needed — only
// this file. (vcsim, govmomi's in-process simulator, would back the live tests.)
package esxi

import (
	"errors"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// errLiveUnavailable is returned until a real govmomi connection is wired here.
var errLiveUnavailable = errors.New("esxi: live govmomi backend not implemented in this build")

// liveBackend would hold the pure-Go govmomi *govmomi.Client. Stubbed.
type liveBackend struct {
	sdkURL string
}

// NewLive constructs a Provider backed by a live vSphere connection at sdkURL (e.g.
// "https://vcenter.lab.local/sdk"). Available only under the vsphere_live build tag.
func NewLive(id, sdkURL string, opts ...Option) (*Provider, error) {
	be := &liveBackend{sdkURL: sdkURL}
	if !be.healthy() {
		return nil, errLiveUnavailable
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

func (l *liveBackend) version() string { return "" }
func (l *liveBackend) healthy() bool   { return false } // stub: never healthy
func (l *liveBackend) close() error    { return nil }

func (l *liveBackend) listHosts() []*vsphereHost            { return nil }
func (l *liveBackend) getHost(string) (*vsphereHost, bool)  { return nil, false }
func (l *liveBackend) listVMs() []*vsphereVM                { return nil }
func (l *liveBackend) getVM(string) (*vsphereVM, bool)      { return nil, false }
func (l *liveBackend) listClusters() []*vsphereCluster      { return nil }
func (l *liveBackend) getCluster(string) (*vsphereCluster, bool) { return nil, false }
func (l *liveBackend) listDatastores() []*vsphereDatastore  { return nil }
func (l *liveBackend) listNetworks() []*vsphereNetwork      { return nil }

func (l *liveBackend) createVM(*vsphereVM)         {}
func (l *liveBackend) destroyVM(string)            {}
func (l *liveBackend) setPower(string, powerState) {}
func (l *liveBackend) vmsOnHost(string) int        { return 0 }

func (l *liveBackend) listSnapshots(string) []vp.Snapshot     { return nil }
func (l *liveBackend) createSnapshot(string, vp.Snapshot)     {}
func (l *liveBackend) setCurrentSnapshot(string, string) bool { return false }

var _ vsphereBackend = (*liveBackend)(nil)
