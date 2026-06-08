// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package kvm_test

import (
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/conformance"
	"github.com/gtek-it/castor/server/internal/vprovider/kvm"
)

// TestKVMConformance_FullCaps validates the KVM provider, built against its in-memory
// libvirt simulator (no libvirtd, CGO_ENABLED=0), against the single contract
// conformance suite at 100%. The simulator is seeded with 1 logical cluster,
// 2 hosts, 3 domains (running/running/shutoff), 1 storage pool, 1 network.
func TestKVMConformance_FullCaps(t *testing.T) {
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := kvm.New("kvm-lab1")
		return p, func() { _ = p.Close() }
	})
}

// TestKVMConformance_ReadOnlyCaps proves capability gating: every undeclared
// capability must yield ErrUnsupported, never a silent no-op.
func TestKVMConformance_ReadOnlyCaps(t *testing.T) {
	readOnly := vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
		vp.CapListStorage | vp.CapListNetworks | vp.CapMetrics
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := kvm.New("kvm-ro", kvm.WithCaps(readOnly))
		return p, func() { _ = p.Close() }
	})
}

// TestKVMStateMapping asserts the libvirt virDomainState -> vp.VMState mapping is
// faithful through the public read path (kind is KVM; states normalized).
func TestKVMStateMapping(t *testing.T) {
	p := kvm.New("kvm-states")
	defer p.Close()
	if p.Kind() != vp.KindKVM {
		t.Fatalf("Kind=%q want %q", p.Kind(), vp.KindKVM)
	}
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	seen := map[vp.VMState]bool{}
	for _, v := range vms {
		seen[v.State] = true
		if v.Kind != vp.KindKVM {
			t.Errorf("vm %s kind=%q", v.ID, v.Kind)
		}
	}
	if !seen[vp.StateRunning] {
		t.Error("expected a running (libvirt state 1) domain in the seed")
	}
	if !seen[vp.StateStopped] {
		t.Error("expected a stopped (libvirt shutoff state 5) domain in the seed")
	}
}
