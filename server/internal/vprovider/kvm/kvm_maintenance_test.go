package kvm

import (
	"context"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// findHostState returns the normalized state of host id from ListHosts.
func findHostState(t *testing.T, p *Provider, id string) vp.NodeStateKind {
	t.Helper()
	hosts, err := p.ListHosts(context.Background())
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	for _, h := range hosts {
		if h.ID == id {
			return h.State
		}
	}
	t.Fatalf("host %s not found", id)
	return ""
}

func TestMaintenanceEnterExitReflectedInListHosts(t *testing.T) {
	p := New("kvm-test") // sim backend: 2 nodes (node-1, node-2)
	ctx := context.Background()

	if got := findHostState(t, p, "node-1"); got != vp.NodeUp {
		t.Fatalf("node-1 initial state = %q, want up", got)
	}

	// Enter maintenance WITHOUT evacuation.
	task, err := p.EnterMaintenance(ctx, "node-1", false)
	if err != nil {
		t.Fatalf("EnterMaintenance: %v", err)
	}
	if task.Message == "" {
		t.Fatalf("expected a note on the maintenance task")
	}
	if got := findHostState(t, p, "node-1"); got != vp.NodeMaintenance {
		t.Fatalf("node-1 after enter = %q, want maintenance", got)
	}
	// Other hosts are unaffected.
	if got := findHostState(t, p, "node-2"); got != vp.NodeUp {
		t.Fatalf("node-2 = %q, want up", got)
	}

	// Exit returns it to service.
	if _, err := p.ExitMaintenance(ctx, "node-1"); err != nil {
		t.Fatalf("ExitMaintenance: %v", err)
	}
	if got := findHostState(t, p, "node-1"); got != vp.NodeUp {
		t.Fatalf("node-1 after exit = %q, want up", got)
	}
}

func TestMaintenanceEvacuateMovesRunningVMs(t *testing.T) {
	p := New("kvm-test")
	ctx := context.Background()

	// Find a host that currently has at least one running VM.
	vms, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	srcHost := ""
	for _, v := range vms {
		if v.State == vp.StateRunning && v.HostID != "" {
			srcHost = v.HostID
			break
		}
	}
	if srcHost == "" {
		t.Skip("no running VM with a host in the seed")
	}

	if _, err := p.EnterMaintenance(ctx, srcHost, true); err != nil {
		t.Fatalf("EnterMaintenance(evacuate): %v", err)
	}

	// After evacuation, no RUNNING VM should remain on the source host.
	after, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs after: %v", err)
	}
	for _, v := range after {
		if v.HostID == srcHost && v.State == vp.StateRunning {
			t.Fatalf("running VM %s still on evacuated host %s", v.ID, srcHost)
		}
	}
	if got := findHostState(t, p, srcHost); got != vp.NodeMaintenance {
		t.Fatalf("source host state = %q, want maintenance", got)
	}
}

func TestMaintenanceUnknownHost(t *testing.T) {
	p := New("kvm-test")
	if _, err := p.EnterMaintenance(context.Background(), "no-such-host", false); err != vp.ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown host, got %v", err)
	}
}

func TestMaintenanceUnsupportedWithoutCap(t *testing.T) {
	p := New("kvm-test", WithCaps(vp.CapListHosts)) // no CapMaintenance
	if _, err := p.EnterMaintenance(context.Background(), "node-1", false); err != vp.ErrUnsupported {
		t.Fatalf("expected ErrUnsupported without CapMaintenance, got %v", err)
	}
}
