package inventory_test

import (
	"context"
	"testing"
	"time"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/sim"
)

// fakeVMReg is a minimal VMProviders.
type fakeVMReg struct{ ps []vprovider.HypervisorProvider }

func (f fakeVMReg) List() []vprovider.HypervisorProvider { return f.ps }

// fakeContainers is a minimal ContainerSnapshots.
type fakeContainers struct{ snaps []inventory.ContainerHostSnapshot }

func (f fakeContainers) ContainerHostSnapshots() []inventory.ContainerHostSnapshot { return f.snaps }

func TestAggregator_MergesBothDomains(t *testing.T) {
	vmReg := fakeVMReg{ps: []vprovider.HypervisorProvider{
		sim.New("sim-kvm"),
		sim.New("sim-esxi", sim.WithKind(vprovider.KindVMware)),
	}}
	cont := fakeContainers{snaps: []inventory.ContainerHostSnapshot{
		{HostID: "local", Workloads: []provider.Workload{
			{ID: "c1", Name: "nginx", Kind: provider.KindDocker, State: provider.StateRunning},
			{ID: "c2", Name: "redis", Kind: provider.KindDocker, State: provider.StateStopped},
		}},
	}}

	agg := inventory.New(vmReg, cont)
	u := agg.All(context.Background(), time.Unix(1700000000, 0).UTC())

	// Each sim seeds 3 VMs -> 2 providers = 6 VMs.
	if u.Counts.VMs != 6 {
		t.Errorf("VMs=%d, want 6", u.Counts.VMs)
	}
	// Each sim seeds 2 running + 1 stopped -> 4 running.
	if u.Counts.VMsRunning != 4 {
		t.Errorf("VMsRunning=%d, want 4", u.Counts.VMsRunning)
	}
	if u.Counts.HypervisorProviders != 2 {
		t.Errorf("HypervisorProviders=%d, want 2", u.Counts.HypervisorProviders)
	}
	if u.Counts.Containers != 2 {
		t.Errorf("Containers=%d, want 2", u.Counts.Containers)
	}
	if u.Counts.ContainersUp != 1 {
		t.Errorf("ContainersUp=%d, want 1", u.Counts.ContainersUp)
	}
	if u.Counts.ContainerHosts != 1 {
		t.Errorf("ContainerHosts=%d, want 1", u.Counts.ContainerHosts)
	}
	// VMs from both providers present, deterministically sorted by ID.
	if len(u.VMs) != 6 || u.VMs[0].ID == "" {
		t.Fatalf("unexpected VMs: %+v", u.VMs)
	}
	// Workloads tagged with host id.
	for _, w := range u.Workloads {
		if w.HostID != "local" {
			t.Errorf("workload %s host=%q, want local", w.ID, w.HostID)
		}
	}
}

func TestAggregator_NilDomainsSafe(t *testing.T) {
	// VM-only.
	agg := inventory.New(fakeVMReg{ps: []vprovider.HypervisorProvider{sim.New("only-vm")}}, nil)
	u := agg.All(context.Background(), time.Now().UTC())
	if u.Counts.VMs != 3 || u.Counts.Containers != 0 {
		t.Errorf("VM-only: got VMs=%d containers=%d", u.Counts.VMs, u.Counts.Containers)
	}
	// Container-only.
	agg2 := inventory.New(nil, fakeContainers{snaps: []inventory.ContainerHostSnapshot{
		{HostID: "h", Workloads: []provider.Workload{{ID: "x", State: provider.StateRunning}}},
	}})
	u2 := agg2.All(context.Background(), time.Now().UTC())
	if u2.Counts.VMs != 0 || u2.Counts.Containers != 1 {
		t.Errorf("container-only: got VMs=%d containers=%d", u2.Counts.VMs, u2.Counts.Containers)
	}
}
