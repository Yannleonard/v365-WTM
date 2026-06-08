// Package insights is UniHV's cross-domain best-practice / drift / health rules
// engine. It scans the UNIFIED inventory (VMs across every hypervisor AND
// containers across every orchestrator) and emits an actionable feed of findings
// — risky configuration, reclaim candidates, snapshot sprawl, single points of
// failure — each with a severity and a concrete suggested action.
//
// No competitor offers a single insights feed spanning hypervisors AND
// orchestrators: vCenter has scattered alarms, Prism has its own analysis,
// Proxmox has none, Portainer/Rancher none for VMs. UniHV computes it all in one
// pure pass over the aggregator output.
//
// Every rule is a pure function of (inventory, thresholds, now) so the whole
// engine is deterministic and unit-testable with crafted fixtures, and imports
// no hypervisor/orchestrator SDK.
package insights

import (
	"fmt"
	"sort"
	"time"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// Severity orders findings for the UI (critical first).
type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityWarn     Severity = "warn"
	SeverityInfo     Severity = "info"
)

func sevRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarn:
		return 1
	default:
		return 2
	}
}

// Category groups findings in the UI.
type Category string

const (
	CatResilience  Category = "resilience"  // HA / SPOF / no backup
	CatReclaim     Category = "reclaim"     // wasted / unused resources
	CatHousekeeping Category = "housekeeping" // snapshot sprawl, orphans
	CatHealth      Category = "health"      // degraded / error state
)

// Insight is one finding in the feed.
type Insight struct {
	ID         string   `json:"id"`         // stable rule+entity id (idempotent across scans)
	Rule       string   `json:"rule"`       // rule key (e.g. "vm.no_snapshot")
	Severity   Severity `json:"severity"`
	Category   Category `json:"category"`
	Title      string   `json:"title"`
	Detail     string   `json:"detail"`
	Suggestion string   `json:"suggestion"`

	// Affected entity (best-effort; some cluster-level rules have no single entity).
	EntityID   string `json:"entityId,omitempty"`
	EntityName string `json:"entityName,omitempty"`
	EntityType string `json:"entityType,omitempty"` // vm|container|cluster|host
	Provider   string `json:"providerId,omitempty"`
	Kind       string `json:"kind,omitempty"`
}

// Thresholds tune the time/size-based rules. Sensible defaults ship enabled.
type Thresholds struct {
	// StaleSnapshotDays: snapshots older than this are flagged (sprawl / space).
	StaleSnapshotDays int `json:"staleSnapshotDays"`
	// PoweredOffDays: a VM stopped longer than this is a reclaim candidate.
	PoweredOffDays int `json:"poweredOffDays"`
	// SnapshotCountWarn: a VM carrying at least this many snapshots is flagged.
	SnapshotCountWarn int `json:"snapshotCountWarn"`
}

// DefaultThresholds are conservative, production-friendly defaults.
func DefaultThresholds() Thresholds {
	return Thresholds{
		StaleSnapshotDays: 14,
		PoweredOffDays:    30,
		SnapshotCountWarn: 5,
	}
}

// Normalize replaces non-positive values with defaults.
func (t Thresholds) Normalize() Thresholds {
	d := DefaultThresholds()
	if t.StaleSnapshotDays <= 0 {
		t.StaleSnapshotDays = d.StaleSnapshotDays
	}
	if t.PoweredOffDays <= 0 {
		t.PoweredOffDays = d.PoweredOffDays
	}
	if t.SnapshotCountWarn <= 0 {
		t.SnapshotCountWarn = d.SnapshotCountWarn
	}
	return t
}

// Feed is the analyzed output: the findings plus a severity histogram for the UI
// header.
type Feed struct {
	Insights    []Insight        `json:"insights"`
	Counts      map[Severity]int `json:"counts"`
	GeneratedAt time.Time        `json:"generatedAt"`
}

// hasBackupLabel reports whether a VM's labels mark it as backed up / replicated.
// We treat any of these conventional keys (truthy) as "protected".
func hasBackupLabel(labels map[string]string) bool {
	keys := []string{"backup", "io.castor.backup", "replication", "io.castor.replication", "veeam.backup"}
	for _, k := range keys {
		if v, ok := labels[k]; ok && v != "" && v != "false" && v != "0" {
			return true
		}
	}
	return false
}

// Analyze runs every rule over the unified snapshot and returns a sorted feed.
// Pure: deterministic for a given (inventory, thresholds, now).
func Analyze(u inventory.Unified, th Thresholds, now time.Time) Feed {
	th = th.Normalize()
	var out []Insight

	out = append(out, vmRules(u, th, now)...)
	out = append(out, clusterRules(u)...)
	out = append(out, containerRules(u)...)
	out = append(out, degradedRules(u)...)

	// Stable, severity-first ordering.
	sort.SliceStable(out, func(i, j int) bool {
		if sevRank(out[i].Severity) != sevRank(out[j].Severity) {
			return sevRank(out[i].Severity) < sevRank(out[j].Severity)
		}
		if out[i].Rule != out[j].Rule {
			return out[i].Rule < out[j].Rule
		}
		return out[i].ID < out[j].ID
	})

	counts := map[Severity]int{SeverityCritical: 0, SeverityWarn: 0, SeverityInfo: 0}
	for _, in := range out {
		counts[in.Severity]++
	}
	return Feed{Insights: out, Counts: counts, GeneratedAt: now}
}

func vmRules(u inventory.Unified, th Thresholds, now time.Time) []Insight {
	var out []Insight
	for _, vm := range u.VMs {
		base := Insight{
			EntityID: vm.ID, EntityName: vm.Name, EntityType: "vm",
			Provider: vm.ProviderID, Kind: string(vm.Kind),
		}

		// No backup/replication label on a running VM => data-loss risk.
		if vm.State == vprovider.StateRunning && !hasBackupLabel(vm.Labels) {
			in := base
			in.ID = "vm.no_backup:" + vm.ProviderID + ":" + vm.ID
			in.Rule = "vm.no_backup"
			in.Severity = SeverityWarn
			in.Category = CatResilience
			in.Title = "VM has no backup or replication"
			in.Detail = fmt.Sprintf("%q is running with no backup/replication label set — an unprotected workload.", vm.Name)
			in.Suggestion = "Configure a backup or replication policy and tag the VM (e.g. label backup=true)."
			out = append(out, in)
		}

		// Snapshot sprawl: many snapshots eat space + degrade performance.
		if vm.SnapshotCount >= th.SnapshotCountWarn {
			in := base
			in.ID = "vm.snapshot_sprawl:" + vm.ProviderID + ":" + vm.ID
			in.Rule = "vm.snapshot_sprawl"
			in.Severity = SeverityWarn
			in.Category = CatHousekeeping
			in.Title = "Snapshot sprawl"
			in.Detail = fmt.Sprintf("%q carries %d snapshots; long snapshot chains consume storage and slow I/O.", vm.Name, vm.SnapshotCount)
			in.Suggestion = "Consolidate or delete old snapshots."
			out = append(out, in)
		}

		// Powered-off-for-long => reclaim candidate. CreatedAt is the best proxy we
		// have in the normalized header; if it's old AND the VM is stopped we flag it
		// (no per-state-change timestamp exists in the contract).
		if vm.State == vprovider.StateStopped && !vm.CreatedAt.IsZero() {
			age := now.Sub(vm.CreatedAt)
			if age >= time.Duration(th.PoweredOffDays)*24*time.Hour {
				in := base
				in.ID = "vm.reclaim_candidate:" + vm.ProviderID + ":" + vm.ID
				in.Rule = "vm.reclaim_candidate"
				in.Severity = SeverityInfo
				in.Category = CatReclaim
				in.Title = "Stopped VM — reclaim candidate"
				in.Detail = fmt.Sprintf("%q has been stopped and exists for %d+ days; its disks still consume storage.", vm.Name, th.PoweredOffDays)
				in.Suggestion = "Confirm it is no longer needed, then delete it (with its disks) to reclaim storage."
				out = append(out, in)
			}
		}
	}
	return out
}

// clusterRules flags resilience problems at the cluster level.
func clusterRules(u inventory.Unified) []Insight {
	var out []Insight

	// Single-host clusters = no failover domain (SPOF).
	for _, cl := range u.Clusters {
		if len(cl.HostIDs) <= 1 {
			out = append(out, Insight{
				ID: "cluster.single_host:" + cl.ProviderID + ":" + cl.ID, Rule: "cluster.single_host",
				Severity: SeverityCritical, Category: CatResilience,
				Title:      "Single-host cluster (no failover)",
				Detail:     fmt.Sprintf("Cluster %q has %d host(s); a single-host cluster has no HA failover domain.", cl.Name, len(cl.HostIDs)),
				Suggestion: "Add at least one more host so HA can restart VMs on a survivor.",
				EntityID:   cl.ID, EntityName: cl.Name, EntityType: "cluster",
				Provider: cl.ProviderID, Kind: string(cl.Kind),
			})
			continue
		}
		// Multi-host cluster with HA disabled = a configured failover domain left off.
		if !cl.HAEnabled {
			out = append(out, Insight{
				ID: "cluster.ha_disabled:" + cl.ProviderID + ":" + cl.ID, Rule: "cluster.ha_disabled",
				Severity: SeverityWarn, Category: CatResilience,
				Title:      "HA disabled on a multi-host cluster",
				Detail:     fmt.Sprintf("Cluster %q has %d hosts but HA is disabled; a host failure will not auto-restart its VMs.", cl.Name, len(cl.HostIDs)),
				Suggestion: "Enable HA so VMs restart automatically on a host failure.",
				EntityID:   cl.ID, EntityName: cl.Name, EntityType: "cluster",
				Provider: cl.ProviderID, Kind: string(cl.Kind),
			})
		}
	}
	return out
}

// containerRules flags container-domain best-practice issues.
func containerRules(u inventory.Unified) []Insight {
	var out []Insight
	for _, w := range u.Workloads {
		// Container stuck in a non-running, non-terminal state (restart loop / pending).
		if w.State == provider.StateRestarting || w.State == provider.StatePending {
			out = append(out, Insight{
				ID: "container.unstable:" + w.HostID + ":" + w.ID, Rule: "container.unstable",
				Severity: SeverityWarn, Category: CatHealth,
				Title:      "Container not stable",
				Detail:     fmt.Sprintf("%q on %s is %s — possible crash loop or scheduling problem.", w.Name, w.HostID, w.State),
				Suggestion: "Inspect logs and events; check resource limits and the image entrypoint.",
				EntityID:   w.ID, EntityName: w.Name, EntityType: "container",
				Provider: w.ProviderID, Kind: string(w.Kind),
			})
		}
	}
	return out
}

// degradedRules surfaces providers/hosts the aggregator marked degraded.
func degradedRules(u inventory.Unified) []Insight {
	var out []Insight
	for _, d := range u.Degraded {
		out = append(out, Insight{
			ID: "provider.degraded:" + d, Rule: "provider.degraded",
			Severity: SeverityCritical, Category: CatHealth,
			Title:      "Provider degraded",
			Detail:     fmt.Sprintf("%s failed an inventory read; its data may be stale or incomplete.", d),
			Suggestion: "Check connectivity and credentials for this provider/host.",
			EntityID:   d, EntityType: "host",
		})
	}
	return out
}
