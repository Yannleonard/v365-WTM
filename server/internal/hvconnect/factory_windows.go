//go:build windows

package hvconnect

import (
	"github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/hyperv"
)

// buildHyperV instantiates the real Hyper-V provider (WMI root\virtualization\v2)
// on a Windows UniHV node. conn.Endpoint is the target computer name ("" = local).
func buildHyperV(conn Conn) (vprovider.HypervisorProvider, error) {
	return hyperv.NewLive(conn.ID, conn.Endpoint)
}
