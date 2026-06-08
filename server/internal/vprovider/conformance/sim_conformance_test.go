package conformance_test

import (
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/conformance"
	"github.com/gtek-it/castor/server/internal/vprovider/sim"
)

// The sim provider is the reference implementation: it must pass the conformance
// suite at 100% with full capabilities. This simultaneously proves the suite and
// the contract are coherent before any real provider is built.
func TestSimConformance_FullCaps(t *testing.T) {
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := sim.New("sim-full")
		return p, func() { _ = p.Close() }
	})
}

// A reduced-capability sim proves the suite correctly verifies capability gating:
// every undeclared capability must yield ErrUnsupported, never a silent no-op.
func TestSimConformance_ReadOnlyCaps(t *testing.T) {
	readOnly := vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
		vp.CapListStorage | vp.CapListNetworks | vp.CapMetrics
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := sim.New("sim-ro", sim.WithCaps(readOnly), sim.WithKind(vp.KindVMware))
		return p, func() { _ = p.Close() }
	})
}
