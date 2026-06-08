// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package xen_test

import (
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/conformance"
	"github.com/gtek-it/castor/server/internal/vprovider/xen"
)

// TestXenConformance_FullCaps validates the Xen/XAPI provider, built against its
// in-memory XAPI fake (no XenServer, CGO_ENABLED=0, no XAPI SDK in go.mod),
// against the single contract conformance suite at 100%. The fake is seeded with
// 1 pool (=cluster), 2 hosts, 3 VMs (Running/Running/Halted), 1 SR, 1 network.
func TestXenConformance_FullCaps(t *testing.T) {
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := xen.New("xen-pool1")
		return p, func() { _ = p.Close() }
	})
}

// TestXenConformance_ReadOnlyCaps proves capability gating: every undeclared
// capability must yield ErrUnsupported, never a silent no-op.
func TestXenConformance_ReadOnlyCaps(t *testing.T) {
	readOnly := vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
		vp.CapListStorage | vp.CapListNetworks | vp.CapMetrics
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := xen.New("xen-ro", xen.WithCaps(readOnly))
		return p, func() { _ = p.Close() }
	})
}

// TestXenStateMapping asserts the XAPI vm_power_state -> vp.VMState mapping is
// faithful through the public read path (kind is Xen; states normalized) and that
// the native power_state token is preserved verbatim in StateRaw.
func TestXenStateMapping(t *testing.T) {
	p := xen.New("xen-states")
	defer p.Close()
	if p.Kind() != vp.KindXen {
		t.Fatalf("Kind=%q want %q", p.Kind(), vp.KindXen)
	}
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	seen := map[vp.VMState]bool{}
	for _, v := range vms {
		seen[v.State] = true
		if v.Kind != vp.KindXen {
			t.Errorf("vm %s kind=%q", v.ID, v.Kind)
		}
		// every VM ID must be an XAPI opaque ref.
		if len(v.ID) < len("OpaqueRef:") || v.ID[:len("OpaqueRef:")] != "OpaqueRef:" {
			t.Errorf("vm ID %q is not an XAPI opaque ref", v.ID)
		}
		// StateRaw must carry the verbatim XAPI token.
		switch v.State {
		case vp.StateRunning:
			if v.StateRaw != "Running" {
				t.Errorf("running vm StateRaw=%q want Running", v.StateRaw)
			}
		case vp.StateStopped:
			if v.StateRaw != "Halted" {
				t.Errorf("stopped vm StateRaw=%q want Halted", v.StateRaw)
			}
		}
	}
	if !seen[vp.StateRunning] {
		t.Error("expected a Running VM in the seed")
	}
	if !seen[vp.StateStopped] {
		t.Error("expected a Halted (->stopped) VM in the seed")
	}
}
