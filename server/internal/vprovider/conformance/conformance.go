// Package conformance provides the SINGLE contract-conformance test suite that
// validates ANY HypervisorProvider against the vprovider contract (prompt §3.3).
// Each real provider (kvm/hyperv/xen/esxi) calls RunConformance from its own test
// with a factory that returns a provider wired to its simulator (libvirt test://,
// vcsim, WMI mock, XAPI mock). Passing this suite at 100% is the auto-verifiable
// acceptance criterion (DoD) for every provider — no human sign-off.
package conformance

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// Factory builds a fresh provider instance for one test. It returns the provider
// and a cleanup func. The provider should be seeded with at least one cluster,
// one host and one VM so read assertions are meaningful.
type Factory func(t *testing.T) (vp.HypervisorProvider, func())

// RunConformance runs the full suite against the provider produced by f. Call it
// from a provider package test: `conformance.RunConformance(t, myFactory)`.
//
// The suite adapts to the provider's declared CapabilityMatrix: a capability that
// is NOT declared must return ErrUnsupported (gating is verified); a capability
// that IS declared must actually work. This enforces prompt §3.4 — no action ever
// fails silently due to an absent capability.
func RunConformance(t *testing.T, f Factory) {
	t.Helper()

	t.Run("Identity", func(t *testing.T) {
		p, done := f(t)
		defer done()
		if p.ID() == "" {
			t.Error("ID() must be non-empty")
		}
		if p.Kind() == "" {
			t.Error("Kind() must be non-empty")
		}
		// Capabilities must be stable across calls.
		if p.Capabilities() != p.Capabilities() {
			t.Error("Capabilities() must be deterministic")
		}
	})

	t.Run("HealthCheck", func(t *testing.T) {
		p, done := f(t)
		defer done()
		hs, err := p.HealthCheck(context.Background())
		if err != nil {
			t.Fatalf("HealthCheck error: %v", err)
		}
		if !hs.Healthy {
			t.Errorf("expected healthy provider, got: %s", hs.Message)
		}
		if hs.CheckedAt.IsZero() {
			t.Error("HealthStatus.CheckedAt must be set")
		}
	})

	t.Run("CapabilityTokensStable", func(t *testing.T) {
		p, done := f(t)
		defer done()
		a := p.Capabilities().Strings()
		b := p.Capabilities().Strings()
		if len(a) != len(b) {
			t.Fatal("capability token list length must be stable")
		}
		for i := range a {
			if a[i] != b[i] {
				t.Errorf("capability token order not deterministic at %d: %q vs %q", i, a[i], b[i])
			}
		}
	})

	t.Run("Inventory", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()

		gateList(t, "ListHosts", caps.Has(vp.CapListHosts), func() error {
			hs, err := p.ListHosts(ctx)
			if err == nil {
				for _, h := range hs {
					if h.ProviderID != p.ID() {
						t.Errorf("host %s ProviderID=%q, want %q", h.ID, h.ProviderID, p.ID())
					}
				}
			}
			return err
		})

		gateList(t, "ListVMs", caps.Has(vp.CapListVMs), func() error {
			vms, err := p.ListVMs(ctx, vp.ListOptions{})
			if err == nil {
				if len(vms) == 0 {
					t.Error("ListVMs returned empty; seed at least one VM")
				}
				for _, v := range vms {
					if v.ID == "" || v.Name == "" {
						t.Errorf("VM has empty ID/Name: %+v", v)
					}
					if v.ProviderID != p.ID() {
						t.Errorf("VM %s ProviderID=%q, want %q", v.ID, v.ProviderID, p.ID())
					}
					if v.Kind != p.Kind() {
						t.Errorf("VM %s Kind=%q, want %q", v.ID, v.Kind, p.Kind())
					}
				}
			}
			return err
		})

		gateList(t, "ListClusters", caps.Has(vp.CapListClusters), func() error {
			_, err := p.ListClusters(ctx)
			return err
		})
		gateList(t, "ListStorage", caps.Has(vp.CapListStorage), func() error {
			_, err := p.ListStorage(ctx)
			return err
		})
		gateList(t, "ListNetworks", caps.Has(vp.CapListNetworks), func() error {
			_, err := p.ListNetworks(ctx)
			return err
		})
	})

	t.Run("GetVM", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		if !p.Capabilities().Has(vp.CapGetVM) {
			if _, err := p.GetVM(ctx, "anything"); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("GetVM without CapGetVM must return ErrUnsupported, got %v", err)
			}
			return
		}
		// Unknown id -> ErrNotFound.
		if _, err := p.GetVM(ctx, "definitely-missing-id"); !errors.Is(err, vp.ErrNotFound) {
			t.Errorf("GetVM(missing) must return ErrNotFound, got %v", err)
		}
		// Known id (from ListVMs) -> success with raw.
		vms, err := p.ListVMs(ctx, vp.ListOptions{})
		if err != nil || len(vms) == 0 {
			t.Skip("no VMs to inspect")
		}
		d, err := p.GetVM(ctx, vms[0].ID)
		if err != nil {
			t.Fatalf("GetVM(known) error: %v", err)
		}
		if d.ID != vms[0].ID {
			t.Errorf("GetVM returned id %q, want %q", d.ID, vms[0].ID)
		}
	})

	t.Run("PowerLifecycle", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		vms, err := pickVMs(ctx, p)
		if err != nil {
			t.Skip("cannot list VMs")
		}
		if len(vms) == 0 {
			t.Skip("no VMs")
		}
		id := vms[0].ID
		for _, op := range []vp.PowerOp{vp.PowerStop, vp.PowerStart, vp.PowerSuspend, vp.PowerResume, vp.PowerReset} {
			cap := vp.PowerOpCapability(op)
			task, err := p.PowerOp(ctx, id, op)
			if p.Capabilities().Has(cap) {
				if err != nil {
					t.Errorf("PowerOp(%s) failed though capable: %v", op, err)
					continue
				}
				if task == nil || !task.Done() || task.State != vp.TaskSucceeded {
					t.Errorf("PowerOp(%s) task not successfully done: %+v", op, task)
				}
			} else {
				if !errors.Is(err, vp.ErrUnsupported) {
					t.Errorf("PowerOp(%s) without capability must return ErrUnsupported, got %v", op, err)
				}
			}
		}
		// Invalid op always rejected.
		if _, err := p.PowerOp(ctx, id, vp.PowerOp("bogus")); err == nil {
			t.Error("PowerOp(bogus) must error")
		}
		// Unknown VM (when capable) -> ErrNotFound.
		if p.Capabilities().Has(vp.CapPowerStart) {
			if _, err := p.PowerOp(ctx, "missing-vm", vp.PowerStart); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("PowerOp(missing) must return ErrNotFound, got %v", err)
			}
		}
	})

	t.Run("CreateReconfigureDelete", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()

		if !caps.Has(vp.CapCreateVM) {
			if _, err := p.CreateVM(ctx, vp.VMSpec{Name: "x", VCPUs: 1, MemoryMB: 512}); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("CreateVM without cap must return ErrUnsupported, got %v", err)
			}
			return
		}
		// Invalid spec rejected.
		if _, err := p.CreateVM(ctx, vp.VMSpec{Name: ""}); !errors.Is(err, vp.ErrInvalidSpec) {
			t.Errorf("CreateVM(invalid) must return ErrInvalidSpec, got %v", err)
		}
		// Valid create.
		spec := vp.VMSpec{Name: "conf-created", VCPUs: 2, MemoryMB: 2048, GuestOS: "linux",
			Disks: []vp.DiskSpec{{CapacityGB: 10}}}
		ct, err := p.CreateVM(ctx, spec)
		if err != nil {
			t.Fatalf("CreateVM failed: %v", err)
		}
		if ct.EntityID == "" {
			t.Fatal("CreateVM task missing EntityID")
		}
		newID := ct.EntityID
		// The created VM must be listable.
		vms, _ := p.ListVMs(ctx, vp.ListOptions{})
		if !containsVM(vms, newID) {
			t.Errorf("created VM %q not present in ListVMs", newID)
		}

		// Reconfigure (if capable).
		if caps.Has(vp.CapReconfigureVM) {
			four := 4
			if _, err := p.ReconfigureVM(ctx, newID, vp.VMReconfigureSpec{VCPUs: &four}); err != nil {
				t.Errorf("ReconfigureVM failed: %v", err)
			}
			if caps.Has(vp.CapGetVM) {
				d, err := p.GetVM(ctx, newID)
				if err == nil && d.VCPUs != 4 {
					t.Errorf("ReconfigureVM did not apply VCPUs: got %d want 4", d.VCPUs)
				}
			}
		}

		// Delete (if capable).
		if caps.Has(vp.CapDeleteVM) {
			if _, err := p.DeleteVM(ctx, newID, vp.DeleteOptions{Force: true, DeleteDisks: true}); err != nil {
				t.Errorf("DeleteVM failed: %v", err)
			}
			vms, _ := p.ListVMs(ctx, vp.ListOptions{})
			if containsVM(vms, newID) {
				t.Errorf("deleted VM %q still present", newID)
			}
			if _, err := p.DeleteVM(ctx, "missing-vm", vp.DeleteOptions{}); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("DeleteVM(missing) must return ErrNotFound, got %v", err)
			}
		}
	})

	t.Run("Snapshots", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()
		vms, err := pickVMs(ctx, p)
		if err != nil || len(vms) == 0 {
			t.Skip("no VMs")
		}
		id := vms[0].ID
		if !caps.Has(vp.CapSnapshot) {
			if _, err := p.Snapshot(ctx, id, vp.SnapshotOptions{Name: "s"}); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("Snapshot without cap must return ErrUnsupported, got %v", err)
			}
			return
		}
		if _, err := p.Snapshot(ctx, id, vp.SnapshotOptions{Name: "snap-a"}); err != nil {
			t.Fatalf("Snapshot failed: %v", err)
		}
		snaps, err := p.ListSnapshots(ctx, id)
		if err != nil {
			t.Fatalf("ListSnapshots failed: %v", err)
		}
		if len(snaps) == 0 {
			t.Fatal("expected at least one snapshot")
		}
		if caps.Has(vp.CapRevertSnapshot) {
			if _, err := p.RevertSnapshot(ctx, id, snaps[0].ID); err != nil {
				t.Errorf("RevertSnapshot failed: %v", err)
			}
			if _, err := p.RevertSnapshot(ctx, id, "missing-snap"); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("RevertSnapshot(missing) must return ErrNotFound, got %v", err)
			}
		}
	})

	t.Run("Clone", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		vms, err := pickVMs(ctx, p)
		if err != nil || len(vms) == 0 {
			t.Skip("no VMs")
		}
		if !p.Capabilities().Has(vp.CapClone) {
			if _, err := p.Clone(ctx, vms[0].ID, vp.CloneSpec{Name: "c"}); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("Clone without cap must return ErrUnsupported, got %v", err)
			}
			return
		}
		ct, err := p.Clone(ctx, vms[0].ID, vp.CloneSpec{Name: "conf-clone"})
		if err != nil {
			t.Fatalf("Clone failed: %v", err)
		}
		if ct.EntityID == "" {
			t.Error("Clone task missing EntityID")
		}
	})

	t.Run("MigrateAndExport", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()
		vms, err := pickVMs(ctx, p)
		if err != nil || len(vms) == 0 {
			t.Skip("no VMs")
		}
		id := vms[0].ID

		// Migrate (intra-hypervisor).
		if caps.Has(vp.CapMigrate) {
			hosts, _ := p.ListHosts(ctx)
			if len(hosts) > 0 {
				if _, err := p.MigrateVM(ctx, id, hosts[len(hosts)-1].ID, vp.MigrateOptions{Live: true}); err != nil {
					t.Errorf("MigrateVM failed: %v", err)
				}
			}
		} else {
			if _, err := p.MigrateVM(ctx, id, "h", vp.MigrateOptions{}); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("MigrateVM without cap must return ErrUnsupported, got %v", err)
			}
		}

		// Export (for V2V).
		if caps.Has(vp.CapExport) {
			rc, info, err := p.ExportVM(ctx, id, vp.DiskQcow2)
			if err != nil {
				t.Fatalf("ExportVM failed: %v", err)
			}
			defer rc.Close()
			data, err := io.ReadAll(rc)
			if err != nil {
				t.Fatalf("reading export stream: %v", err)
			}
			if len(data) == 0 {
				t.Error("ExportVM produced empty stream")
			}
			if info == nil || info.SourceVMID != id {
				t.Errorf("ExportInfo missing/incorrect: %+v", info)
			}
			// Invalid format rejected.
			if _, _, err := p.ExportVM(ctx, id, vp.DiskFormat("bogus")); !errors.Is(err, vp.ErrInvalidSpec) {
				t.Errorf("ExportVM(bad format) must return ErrInvalidSpec, got %v", err)
			}
		} else {
			if _, _, err := p.ExportVM(ctx, id, vp.DiskQcow2); !errors.Is(err, vp.ErrUnsupported) {
				t.Errorf("ExportVM without cap must return ErrUnsupported, got %v", err)
			}
		}
	})

	t.Run("ClusterTopologyAndNodeState", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()

		if caps.Has(vp.CapClusterTopology) {
			cls, _ := p.ListClusters(ctx)
			if len(cls) > 0 {
				top, err := p.GetClusterTopology(ctx, cls[0].ID)
				if err != nil {
					t.Errorf("GetClusterTopology failed: %v", err)
				} else if top.ClusterID != cls[0].ID {
					t.Errorf("topology cluster id mismatch: %q vs %q", top.ClusterID, cls[0].ID)
				}
			}
			if _, err := p.GetClusterTopology(ctx, "missing-cluster"); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("GetClusterTopology(missing) must return ErrNotFound, got %v", err)
			}
		}
		if caps.Has(vp.CapNodeState) {
			hosts, _ := p.ListHosts(ctx)
			if len(hosts) > 0 {
				if _, err := p.NodeState(ctx, hosts[0].ID); err != nil {
					t.Errorf("NodeState failed: %v", err)
				}
			}
			if _, err := p.NodeState(ctx, "missing-node"); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("NodeState(missing) must return ErrNotFound, got %v", err)
			}
		}
	})

	t.Run("MetricsAndEvents", func(t *testing.T) {
		p, done := f(t)
		defer done()
		ctx := context.Background()
		caps := p.Capabilities()
		vms, _ := pickVMs(ctx, p)

		if caps.Has(vp.CapMetrics) && len(vms) > 0 {
			ms, err := p.GetMetrics(ctx, vms[0].ID, vp.MetricWindow{StepSecond: 10})
			if err != nil {
				t.Errorf("GetMetrics failed: %v", err)
			} else if len(ms.Samples) == 0 {
				t.Error("GetMetrics returned no samples")
			}
			if _, err := p.GetMetrics(ctx, "missing-entity", vp.MetricWindow{}); !errors.Is(err, vp.ErrNotFound) {
				t.Errorf("GetMetrics(missing) must return ErrNotFound, got %v", err)
			}
		}
		if caps.Has(vp.CapEvents) {
			cctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
			defer cancel()
			ch, err := p.StreamEvents(cctx)
			if err != nil {
				t.Fatalf("StreamEvents failed: %v", err)
			}
			got := false
			for ev := range ch {
				if ev.ProviderID != p.ID() {
					t.Errorf("event ProviderID=%q want %q", ev.ProviderID, p.ID())
				}
				got = true
				cancel()
			}
			if !got {
				t.Error("StreamEvents produced no events before cancel")
			}
		}
	})

	t.Run("CloseIsIdempotentEnough", func(t *testing.T) {
		p, done := f(t)
		defer done()
		if err := p.Close(); err != nil {
			t.Errorf("Close error: %v", err)
		}
	})
}

// gateList asserts: capable -> err nil; not capable -> ErrUnsupported.
func gateList(t *testing.T, name string, capable bool, call func() error) {
	t.Helper()
	err := call()
	if capable {
		if err != nil {
			t.Errorf("%s failed though capable: %v", name, err)
		}
	} else {
		if !errors.Is(err, vp.ErrUnsupported) {
			t.Errorf("%s without capability must return ErrUnsupported, got %v", name, err)
		}
	}
}

func pickVMs(ctx context.Context, p vp.HypervisorProvider) ([]vp.VM, error) {
	if !p.Capabilities().Has(vp.CapListVMs) {
		return nil, nil
	}
	return p.ListVMs(ctx, vp.ListOptions{})
}

func containsVM(vms []vp.VM, id string) bool {
	for _, v := range vms {
		if v.ID == id {
			return true
		}
	}
	return false
}
