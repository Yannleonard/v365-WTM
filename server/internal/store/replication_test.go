package store

import (
	"context"
	"testing"
)

func TestReplicationPolicyCRUD(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	p := &ReplicationPolicy{
		ID: NewUUID(), Name: "kvm->esxi DR",
		SourceProviderID: "kvm-1", SourceVMID: "vm-1",
		TargetProviderID: "esxi-1", TargetHostID: "host-1",
		IntervalSeconds: 300, Retain: 5, Enabled: true,
	}
	if err := st.CreateReplicationPolicy(ctx, p); err != nil {
		t.Fatalf("CreateReplicationPolicy: %v", err)
	}
	if p.Status != "idle" {
		t.Errorf("default status=%q, want idle", p.Status)
	}

	got, err := st.GetReplicationPolicy(ctx, p.ID)
	if err != nil {
		t.Fatalf("GetReplicationPolicy: %v", err)
	}
	if got.Name != p.Name || got.SourceProviderID != "kvm-1" || got.TargetProviderID != "esxi-1" {
		t.Errorf("round-trip mismatch: %+v", got)
	}
	if got.IntervalSeconds != 300 || got.Retain != 5 || !got.Enabled {
		t.Errorf("scalar fields mismatch: %+v", got)
	}
	if got.TargetHostID != "host-1" {
		t.Errorf("targetHostId=%q, want host-1", got.TargetHostID)
	}

	// Defaults applied when zero.
	p2 := &ReplicationPolicy{ID: NewUUID(), Name: "defaults", SourceProviderID: "a", SourceVMID: "v", TargetProviderID: "b"}
	if err := st.CreateReplicationPolicy(ctx, p2); err != nil {
		t.Fatalf("CreateReplicationPolicy defaults: %v", err)
	}
	g2, _ := st.GetReplicationPolicy(ctx, p2.ID)
	if g2.IntervalSeconds != 300 || g2.Retain != 5 {
		t.Errorf("defaults not applied: interval=%d retain=%d", g2.IntervalSeconds, g2.Retain)
	}

	// State update (cycle outcome).
	if err := st.UpdateReplicationPolicyState(ctx, p.ID, "idle", "", "replica-vm-9", 1700000123); err != nil {
		t.Fatalf("UpdateReplicationPolicyState: %v", err)
	}
	got, _ = st.GetReplicationPolicy(ctx, p.ID)
	if got.Status != "idle" || got.ReplicaVMID != "replica-vm-9" || got.LastSyncAt != 1700000123 {
		t.Errorf("state not persisted: %+v", got)
	}

	// COALESCE: a later update with empty replica/zero sync must NOT clobber them.
	if err := st.UpdateReplicationPolicyState(ctx, p.ID, "syncing", "", "", 0); err != nil {
		t.Fatalf("UpdateReplicationPolicyState #2: %v", err)
	}
	got, _ = st.GetReplicationPolicy(ctx, p.ID)
	if got.Status != "syncing" {
		t.Errorf("status=%q, want syncing", got.Status)
	}
	if got.ReplicaVMID != "replica-vm-9" || got.LastSyncAt != 1700000123 {
		t.Errorf("COALESCE clobbered preserved fields: %+v", got)
	}

	// Error state.
	if err := st.UpdateReplicationPolicyState(ctx, p.ID, "error", "export failed", "", 0); err != nil {
		t.Fatalf("UpdateReplicationPolicyState error: %v", err)
	}
	got, _ = st.GetReplicationPolicy(ctx, p.ID)
	if got.Status != "error" || got.LastError != "export failed" {
		t.Errorf("error state not persisted: %+v", got)
	}

	// List.
	list, err := st.ListReplicationPolicies(ctx)
	if err != nil {
		t.Fatalf("ListReplicationPolicies: %v", err)
	}
	if len(list) != 2 {
		t.Errorf("list len=%d, want 2", len(list))
	}

	// Delete.
	if err := st.DeleteReplicationPolicy(ctx, p.ID); err != nil {
		t.Fatalf("DeleteReplicationPolicy: %v", err)
	}
	if _, err := st.GetReplicationPolicy(ctx, p.ID); err != ErrNotFound {
		t.Errorf("after delete err=%v, want ErrNotFound", err)
	}
	if err := st.DeleteReplicationPolicy(ctx, p.ID); err != ErrNotFound {
		t.Errorf("double delete err=%v, want ErrNotFound", err)
	}
}
