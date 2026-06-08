package insights

import (
	"testing"
	"time"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

func ruleSet(f Feed) map[string]int {
	m := map[string]int{}
	for _, in := range f.Insights {
		m[in.Rule]++
	}
	return m
}

func TestVMNoBackupAndSnapshotSprawl(t *testing.T) {
	now := time.Now()
	u := inventory.Unified{
		VMs: []vprovider.VM{
			// Running, no backup label, lots of snapshots.
			{ID: "vm1", Name: "prod-db", ProviderID: "p1", State: vprovider.StateRunning, SnapshotCount: 7},
			// Running but backed up + few snapshots => clean.
			{ID: "vm2", Name: "safe", ProviderID: "p1", State: vprovider.StateRunning,
				Labels: map[string]string{"backup": "true"}, SnapshotCount: 1},
		},
	}
	f := Analyze(u, DefaultThresholds(), now)
	rs := ruleSet(f)
	if rs["vm.no_backup"] != 1 {
		t.Errorf("expected 1 no_backup (only vm1), got %d", rs["vm.no_backup"])
	}
	if rs["vm.snapshot_sprawl"] != 1 {
		t.Errorf("expected 1 snapshot_sprawl, got %d", rs["vm.snapshot_sprawl"])
	}
}

func TestReclaimCandidate(t *testing.T) {
	now := time.Now()
	old := now.Add(-60 * 24 * time.Hour)
	recent := now.Add(-2 * 24 * time.Hour)
	u := inventory.Unified{
		VMs: []vprovider.VM{
			{ID: "old", Name: "ancient", ProviderID: "p1", State: vprovider.StateStopped, CreatedAt: old},
			{ID: "new", Name: "fresh", ProviderID: "p1", State: vprovider.StateStopped, CreatedAt: recent},
		},
	}
	f := Analyze(u, DefaultThresholds(), now)
	if ruleSet(f)["vm.reclaim_candidate"] != 1 {
		t.Errorf("expected exactly 1 reclaim candidate (old only), got %d", ruleSet(f)["vm.reclaim_candidate"])
	}
}

func TestClusterRules(t *testing.T) {
	now := time.Now()
	u := inventory.Unified{
		Clusters: []vprovider.Cluster{
			{ID: "c1", Name: "single", ProviderID: "p1", HostIDs: []string{"h1"}},
			{ID: "c2", Name: "noha", ProviderID: "p1", HostIDs: []string{"h1", "h2"}, HAEnabled: false},
			{ID: "c3", Name: "good", ProviderID: "p1", HostIDs: []string{"h1", "h2"}, HAEnabled: true},
		},
	}
	f := Analyze(u, DefaultThresholds(), now)
	rs := ruleSet(f)
	if rs["cluster.single_host"] != 1 {
		t.Errorf("expected 1 single_host, got %d", rs["cluster.single_host"])
	}
	if rs["cluster.ha_disabled"] != 1 {
		t.Errorf("expected 1 ha_disabled, got %d", rs["cluster.ha_disabled"])
	}
	// single_host should be critical; verify severity ordering puts it first.
	if len(f.Insights) == 0 || f.Insights[0].Severity != SeverityCritical {
		t.Errorf("critical findings should sort first")
	}
}

func TestContainerAndDegradedRules(t *testing.T) {
	now := time.Now()
	u := inventory.Unified{
		Workloads: []inventory.UnifiedWorkload{
			{HostID: "h1", Workload: provider.Workload{ID: "ct1", Name: "loop", State: provider.StateRestarting}},
			{HostID: "h1", Workload: provider.Workload{ID: "ct2", Name: "ok", State: provider.StateRunning}},
		},
		Degraded: []string{"hypervisor:esxi-1"},
	}
	f := Analyze(u, DefaultThresholds(), now)
	rs := ruleSet(f)
	if rs["container.unstable"] != 1 {
		t.Errorf("expected 1 unstable container, got %d", rs["container.unstable"])
	}
	if rs["provider.degraded"] != 1 {
		t.Errorf("expected 1 degraded provider, got %d", rs["provider.degraded"])
	}
	if f.Counts[SeverityCritical] < 1 {
		t.Errorf("degraded provider should be critical")
	}
}

func TestDeterministicIDs(t *testing.T) {
	now := time.Now()
	u := inventory.Unified{
		VMs: []vprovider.VM{{ID: "vm1", Name: "x", ProviderID: "p1", State: vprovider.StateRunning}},
	}
	a := Analyze(u, DefaultThresholds(), now)
	b := Analyze(u, DefaultThresholds(), now)
	if len(a.Insights) != len(b.Insights) {
		t.Fatalf("non-deterministic count")
	}
	for i := range a.Insights {
		if a.Insights[i].ID != b.Insights[i].ID {
			t.Errorf("insight id not stable: %q vs %q", a.Insights[i].ID, b.Insights[i].ID)
		}
	}
}
