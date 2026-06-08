// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package hyperv_test

import (
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/conformance"
	"github.com/gtek-it/castor/server/internal/vprovider/hyperv"
)

// TestHyperVConformance_FullCaps validates the Microsoft Hyper-V provider, built
// against its in-memory WMI fake (a recorded fixture/mock of root\virtualization\v2,
// faked in Go; no Windows host, CGO_ENABLED=0 on Linux/alpine, no WMI dep in go.mod),
// against the single contract conformance suite at 100%. The fake is seeded with
// 1 failover cluster, 2 cluster-node hosts, 3 VMs (EnabledState 2/2/3), 1 CSV +
// 1 SMB share, 1 external virtual switch.
func TestHyperVConformance_FullCaps(t *testing.T) {
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := hyperv.New("hyperv-lab1")
		return p, func() { _ = p.Close() }
	})
}

// TestHyperVConformance_ReadOnlyCaps proves capability gating: every undeclared
// capability must yield ErrUnsupported, never a silent no-op (prompt §3.4).
func TestHyperVConformance_ReadOnlyCaps(t *testing.T) {
	readOnly := vp.CapListHosts | vp.CapListVMs | vp.CapGetVM | vp.CapListClusters |
		vp.CapListStorage | vp.CapListNetworks | vp.CapMetrics
	conformance.RunConformance(t, func(t *testing.T) (vp.HypervisorProvider, func()) {
		p := hyperv.New("hyperv-ro", hyperv.WithCaps(readOnly))
		return p, func() { _ = p.Close() }
	})
}

// TestHyperVStateMapping asserts the Hyper-V Msvm_ComputerSystem.EnabledState ->
// vp.VMState mapping is faithful through the public read path (kind is hyperv;
// 2->running, 3->stopped) and that the native EnabledState integer token is
// preserved verbatim in StateRaw.
func TestHyperVStateMapping(t *testing.T) {
	p := hyperv.New("hyperv-states")
	defer p.Close()
	if p.Kind() != vp.KindHyperV {
		t.Fatalf("Kind=%q want %q", p.Kind(), vp.KindHyperV)
	}
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	seen := map[vp.VMState]bool{}
	for _, v := range vms {
		seen[v.State] = true
		if v.Kind != vp.KindHyperV {
			t.Errorf("vm %s kind=%q", v.ID, v.Kind)
		}
		switch v.State {
		case vp.StateRunning:
			if v.StateRaw != "2" {
				t.Errorf("vm %s running StateRaw=%q want 2 (Enabled)", v.ID, v.StateRaw)
			}
		case vp.StateStopped:
			if v.StateRaw != "3" {
				t.Errorf("vm %s stopped StateRaw=%q want 3 (Disabled)", v.ID, v.StateRaw)
			}
		}
	}
	if !seen[vp.StateRunning] {
		t.Error("expected a running (EnabledState=2) VM in the seed")
	}
	if !seen[vp.StateStopped] {
		t.Error("expected a stopped (EnabledState=3) VM in the seed")
	}

	// Suspend transition must normalize to suspended (Saved=32769) with the native token.
	if len(vms) > 0 {
		id := vms[0].ID
		if _, err := p.PowerOp(t.Context(), id, vp.PowerSuspend); err != nil {
			t.Fatalf("PowerOp(suspend): %v", err)
		}
		d, err := p.GetVM(t.Context(), id)
		if err != nil {
			t.Fatalf("GetVM: %v", err)
		}
		if d.State != vp.StateSuspended || d.StateRaw != "32769" {
			t.Errorf("after suspend: state=%q raw=%q want suspended/32769 (Saved)", d.State, d.StateRaw)
		}
	}

	// Generation 2 VMs normalize to UEFI firmware; Generation 1 to BIOS.
	var sawUEFI, sawBIOS bool
	for _, v := range vms {
		switch v.Firmware {
		case vp.FirmwareUEFI:
			sawUEFI = true
		case vp.FirmwareBIOS:
			sawBIOS = true
		}
	}
	if !sawUEFI {
		t.Error("expected at least one Generation-2 (UEFI) VM in the seed")
	}
	if !sawBIOS {
		t.Error("expected at least one Generation-1 (BIOS) VM in the seed")
	}
}
