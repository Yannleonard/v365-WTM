// Package hvconnect builds and registers LIVE hypervisor providers from persisted
// connection records (store.HypervisorConn). It is the bridge between the
// connection-management API/UI and the vprovider.Registry: given a connection
// (kind + endpoint + sealed credentials), it instantiates the real official-API
// provider (KVM/libvirt, ESXi/govmomi, Xen/XAPI, Hyper-V/WMI) — never a simulator.
//
// Hyper-V is Windows-only (WMI), so its constructor lives in factory_windows.go;
// on non-Windows builds attempting to connect a hyperv connection returns a clear
// error (the Hyper-V provider must run on a Windows UniHV node).
package hvconnect

import (
	"context"
	"fmt"

	"github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/esxi"
	"github.com/gtek-it/castor/server/internal/vprovider/kvm"
	"github.com/gtek-it/castor/server/internal/vprovider/xen"
)

// Conn is the minimal connection description the factory needs (decoupled from the
// store row; the API layer fills it, opening the sealed secret first).
type Conn struct {
	ID          string
	Name        string
	Kind        string // kvm | hyperv | vmware | xen
	Endpoint    string
	Username    string
	Password    string // already-opened plaintext (never persisted in the clear)
	InsecureTLS bool
}

// Build instantiates the LIVE provider for a connection. It does NOT register it.
// Returns ErrUnsupportedOnPlatform for hyperv on non-Windows builds.
func Build(conn Conn) (vprovider.HypervisorProvider, error) {
	switch conn.Kind {
	case string(vprovider.KindKVM):
		// KVM/libvirt RPC (pure Go). Endpoint is a libvirt URI or socket path.
		ep := conn.Endpoint
		if ep == "" {
			ep = "/var/run/libvirt/libvirt-sock"
		}
		return kvm.NewLiveWithID(conn.ID, ep)
	case string(vprovider.KindVMware):
		// vSphere/ESXi via govmomi (SOAP). Endpoint is the vCenter/ESXi URL.
		return esxi.NewLive(conn.ID, conn.Endpoint, conn.Username, conn.Password, conn.InsecureTLS)
	case string(vprovider.KindXen):
		// Xen via XAPI XML-RPC. Endpoint is the pool/host URL.
		return xen.NewLive(conn.ID, conn.Endpoint, conn.Username, conn.Password, conn.InsecureTLS)
	case string(vprovider.KindHyperV):
		// Implemented in factory_windows.go; stub elsewhere.
		return buildHyperV(conn)
	default:
		return nil, fmt.Errorf("hvconnect: unknown hypervisor kind %q", conn.Kind)
	}
}

// Verify builds the provider, runs a health check, then closes it — used by the
// "test connection" API before persisting/registering.
func Verify(ctx context.Context, conn Conn) error {
	p, err := Build(conn)
	if err != nil {
		return err
	}
	defer func() { _ = p.Close() }()
	hs, err := p.HealthCheck(ctx)
	if err != nil {
		return err
	}
	if !hs.Healthy {
		return fmt.Errorf("hypervisor unhealthy: %s", hs.Message)
	}
	return nil
}
