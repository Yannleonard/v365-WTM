//go:build !windows

package hvconnect

import (
	"errors"

	"github.com/gtek-it/castor/server/internal/vprovider"
)

// ErrUnsupportedOnPlatform is returned when a hypervisor kind cannot run on the
// current UniHV node OS (Hyper-V/WMI requires a Windows node).
var ErrUnsupportedOnPlatform = errors.New("hvconnect: Hyper-V (WMI) requires a Windows UniHV node")

// buildHyperV is unavailable on non-Windows builds: the Hyper-V provider uses the
// Windows-only WMI API. Run a UniHV node on Windows to manage Hyper-V, or manage
// the other hypervisors (KVM/ESXi/Xen) from this node.
func buildHyperV(_ Conn) (vprovider.HypervisorProvider, error) {
	return nil, ErrUnsupportedOnPlatform
}
