//go:build windows

package hvconnect

import (
	"github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/hyperv"
)

// buildHyperV instantiates the real Hyper-V provider (WMI root\virtualization\v2)
// on a Windows UniHV node. conn.Endpoint is the target computer name ("" = local).
// For a REMOTE host, conn.Username/conn.Password are passed through to WMI's
// SWbemLocator.ConnectServer over DCOM; for a local connection they are ignored.
func buildHyperV(conn Conn) (vprovider.HypervisorProvider, error) {
	return hyperv.NewLiveRemote(conn.ID, conn.Endpoint, conn.Username, conn.Password)
}
