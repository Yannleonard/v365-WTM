package replication_test

import (
	"context"
	"testing"

	"github.com/gtek-it/castor/server/internal/migrate"
	"github.com/gtek-it/castor/server/internal/replication"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/sim"
)

// reg is a tiny ProviderResolver over a map (same pattern as the migrate tests).
type reg struct{ m map[string]vp.HypervisorProvider }

func (r reg) Get(id string) (vp.HypervisorProvider, bool) { p, ok := r.m[id]; return p, ok }

func newReg(ps ...vp.HypervisorProvider) reg {
	m := map[string]vp.HypervisorProvider{}
	for _, p := range ps {
		m[p.ID()] = p
	}
	return reg{m: m}
}

// newEngine builds an engine over a KVM source + ESXi target with the passthrough
// converter (qemu-img is absent in CI), no persistence.
func newEngine(t *testing.T) (*replication.Engine, reg, replication.Policy) {
	t.Helper()
	kvm := sim.New("kvm-1", sim.WithKind(vp.KindKVM))
	esxi := sim.New("esxi-1", sim.WithKind(vp.KindVMware))
	r := newReg(kvm, esxi)
	eng := replication.New(r, migrate.PassthroughConverter{}, nil)
	eng.Start()
	pol := replication.Policy{
		ID: "pol-1", Name: "kvm-vm1->esxi DR",
		SourceProviderID: "kvm-1", SourceVMID: "vm-1",
		TargetProviderID: "esxi-1", TargetHostID: "host-1",
		IntervalSeconds: 0, // 0 => no auto-ticking; we drive cycles manually
		Retain:          3, Enabled: true,
	}
	eng.Upsert(pol)
	return eng, r, pol
}

// TestReplication_FullCycle drives one cross-hypervisor (KVM->ESXi) cycle end-to-end
// and verifies a replica was created on the TARGET, DR state was tracked, and a
// second cycle re-syncs (incremental) the replica.
func TestReplication_FullCycle(t *testing.T) {
	eng, r, pol := newEngine(t)
	ctx := context.Background()

	cyc, err := eng.RunNow(ctx, pol.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	if !cyc.OK {
		t.Fatalf("cycle not OK: %s", cyc.Error)
	}
	if !cyc.FirstCycle {
		t.Error("first cycle should be marked FirstCycle")
	}
	if cyc.ReplicaVMID == "" {
		t.Fatal("no replica VM id produced")
	}

	// The replica must exist on the TARGET (esxi-1), be of the VMware kind, and be OFF
	// (a DR replica stays powered down until failover).
	tgt, _ := r.Get("esxi-1")
	d, err := tgt.GetVM(ctx, cyc.ReplicaVMID)
	if err != nil {
		t.Fatalf("replica not found on target: %v", err)
	}
	if d.Kind != vp.KindVMware {
		t.Errorf("replica kind=%s, want vmware (cross-hypervisor)", d.Kind)
	}
	if d.State != vp.StateStopped {
		t.Errorf("replica state=%s, want stopped before failover", d.State)
	}

	// State tracking: status idle/degraded, RPO target echoed, history has 1 entry.
	st, ok := eng.State(pol.ID)
	if !ok {
		t.Fatal("state missing")
	}
	if st.ReplicaVMID != cyc.ReplicaVMID {
		t.Errorf("state replica=%s, cycle replica=%s", st.ReplicaVMID, cyc.ReplicaVMID)
	}
	if st.LastSyncAt.IsZero() {
		t.Error("lastSyncAt not set after a successful cycle")
	}
	if st.CycleCount != 1 {
		t.Errorf("cycleCount=%d, want 1", st.CycleCount)
	}
	if len(st.History) != 1 {
		t.Errorf("history len=%d, want 1", len(st.History))
	}
	if st.Status != replication.StatusIdle && st.Status != replication.StatusDegraded {
		t.Errorf("status=%s, want idle or degraded", st.Status)
	}

	// Second cycle (incremental re-sync).
	cyc2, err := eng.RunNow(ctx, pol.ID)
	if err != nil {
		t.Fatalf("RunNow #2: %v", err)
	}
	if cyc2.FirstCycle {
		t.Error("second cycle should NOT be FirstCycle")
	}
	st2, _ := eng.State(pol.ID)
	if st2.CycleCount != 2 {
		t.Errorf("cycleCount=%d, want 2", st2.CycleCount)
	}
}

// TestReplication_Failover powers on the replica and pauses replication.
func TestReplication_Failover(t *testing.T) {
	eng, r, pol := newEngine(t)
	ctx := context.Background()

	// Failover before any cycle must error (no replica yet).
	if _, err := eng.Failover(ctx, pol.ID); err == nil {
		t.Fatal("failover without a replica should error")
	}

	cyc, err := eng.RunNow(ctx, pol.ID)
	if err != nil {
		t.Fatalf("RunNow: %v", err)
	}

	st, err := eng.Failover(ctx, pol.ID)
	if err != nil {
		t.Fatalf("Failover: %v", err)
	}
	if st.Status != replication.StatusFailedOver {
		t.Errorf("status=%s, want failed_over", st.Status)
	}

	// The replica must now be RUNNING on the target.
	tgt, _ := r.Get("esxi-1")
	d, err := tgt.GetVM(ctx, cyc.ReplicaVMID)
	if err != nil {
		t.Fatalf("replica missing: %v", err)
	}
	if d.State != vp.StateRunning {
		t.Errorf("replica state=%s, want running after failover", d.State)
	}

	// Further cycles must be refused while failed-over.
	if _, err := eng.RunNow(ctx, pol.ID); err == nil {
		t.Error("RunNow on a failed-over policy should be refused")
	}
}

// TestReplication_RPOAndPersistence verifies the measured RPO is computed and the
// durable summary is persisted via the Persister.
func TestReplication_RPOAndPersistence(t *testing.T) {
	kvm := sim.New("kvm-1", sim.WithKind(vp.KindKVM))
	esxi := sim.New("esxi-1", sim.WithKind(vp.KindVMware))
	p := &capturePersister{}
	eng := replication.New(newReg(kvm, esxi), migrate.PassthroughConverter{}, p)
	eng.Start()
	pol := replication.Policy{
		ID: "pol-x", Name: "rpo", SourceProviderID: "kvm-1", SourceVMID: "vm-1",
		TargetProviderID: "esxi-1", IntervalSeconds: 60, Retain: 2, Enabled: true,
	}
	eng.Upsert(pol)

	if _, err := eng.RunNow(context.Background(), pol.ID); err != nil {
		t.Fatalf("RunNow: %v", err)
	}
	st, _ := eng.State(pol.ID)
	if st.RPOTargetSeconds != 60 {
		t.Errorf("RPOTargetSeconds=%d, want 60", st.RPOTargetSeconds)
	}
	if st.MeasuredRPOSeconds < 0 {
		t.Errorf("MeasuredRPOSeconds=%d, want >= 0", st.MeasuredRPOSeconds)
	}
	if p.lastStatus == "" {
		t.Error("persister never received a state update")
	}
	if p.lastReplica == "" {
		t.Error("persister did not record the replica id")
	}
}

// TestReplication_UnknownPolicy guards the lookups.
func TestReplication_UnknownPolicy(t *testing.T) {
	eng := replication.New(newReg(), migrate.PassthroughConverter{}, nil)
	if _, err := eng.RunNow(context.Background(), "nope"); err == nil {
		t.Error("RunNow on unknown policy should error")
	}
	if _, ok := eng.State("nope"); ok {
		t.Error("State on unknown policy should be !ok")
	}
}

// capturePersister records the last persisted summary.
type capturePersister struct {
	lastStatus  string
	lastReplica string
	lastSyncAt  int64
}

func (c *capturePersister) UpdateReplicationPolicyState(_ context.Context, _, status, _, replicaVMID string, lastSyncAt int64) error {
	c.lastStatus = status
	if replicaVMID != "" {
		c.lastReplica = replicaVMID
	}
	if lastSyncAt > 0 {
		c.lastSyncAt = lastSyncAt
	}
	return nil
}
