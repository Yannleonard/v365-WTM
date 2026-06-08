// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package esxi_test

import (
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/conformance"
	"github.com/gtek-it/castor/server/internal/vprovider/esxi"
)

// TestESXiConformance_FullCaps validates the vSphere/ESXi provider, built against
// its in-memory vSphere simulator (the moral equivalent of vcsim, faked in Go; no
// vCenter, CGO_ENABLED=0, no govmomi in go.mod), against the single contract
// conformance suite at 100%. The simulator is seeded with 1 cluster (HA+DRS),
// 2 hosts, 3 VMs (poweredOn/poweredOn/poweredOff), 1 VMFS datastore, 1 port group.
func TestESXiConformance_FullCaps(t *testing.T) {
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := esxi.New("esxi-lab1")
		return p, func() { _ = p.Close() }
	})
}

// TestESXiConformance_ReadOnlyCaps proves capability gating: every undeclared
// capability must yield ErrUnsupported, never a silent no-op (prompt §3.4).
func TestESXiConformance_ReadOnlyCaps(t *testing.T) {
	readOnly := vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
		vp.CapListStorage | vp.CapListNetworks | vp.CapMetrics
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := esxi.New("esxi-ro", esxi.WithCaps(readOnly))
		return p, func() { _ = p.Close() }
	})
}

// TestESXiStateMapping asserts the vSphere VirtualMachinePowerState -> vp.VMState
// mapping is faithful through the public read path (kind is VMware; states
// normalized: poweredOn->running, poweredOff->stopped) and that the native token
// is preserved verbatim in StateRaw.
func TestESXiStateMapping(t *testing.T) {
	p := esxi.New("esxi-states")
	defer p.Close()
	if p.Kind() != vp.KindVMware {
		t.Fatalf("Kind=%q want %q", p.Kind(), vp.KindVMware)
	}
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	seen := map[vp.VMState]bool{}
	for _, v := range vms {
		seen[v.State] = true
		if v.Kind != vp.KindVMware {
			t.Errorf("vm %s kind=%q", v.ID, v.Kind)
		}
		switch v.State {
		case vp.StateRunning:
			if v.StateRaw != "poweredOn" {
				t.Errorf("vm %s running StateRaw=%q want poweredOn", v.ID, v.StateRaw)
			}
		case vp.StateStopped:
			if v.StateRaw != "poweredOff" {
				t.Errorf("vm %s stopped StateRaw=%q want poweredOff", v.ID, v.StateRaw)
			}
		}
	}
	if !seen[vp.StateRunning] {
		t.Error("expected a running (poweredOn) VM in the seed")
	}
	if !seen[vp.StateStopped] {
		t.Error("expected a stopped (poweredOff) VM in the seed")
	}

	// Suspend transition must normalize to suspended with the native token.
	if len(vms) > 0 {
		id := vms[0].ID
		if _, err := p.PowerOp(t.Context(), id, vp.PowerSuspend); err != nil {
			t.Fatalf("PowerOp(suspend): %v", err)
		}
		d, err := p.GetVM(t.Context(), id)
		if err != nil {
			t.Fatalf("GetVM: %v", err)
		}
		if d.State != vp.StateSuspended || d.StateRaw != "suspended" {
			t.Errorf("after suspend: state=%q raw=%q want suspended/suspended", d.State, d.StateRaw)
		}
	}
}
