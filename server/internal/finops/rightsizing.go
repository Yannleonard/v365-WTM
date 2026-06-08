package finops

import (
	"math"
	"sort"

	"github.com/gtek-it/castor/server/internal/vprovider"
)

// RecKind classifies a rightsizing recommendation.
type RecKind string

const (
	// RecIdle: the VM is running but barely used — a candidate to power off or
	// reclaim entirely.
	RecIdle RecKind = "idle"
	// RecOversized: the VM is used, but its peak utilization sits well below its
	// allocation — a candidate to downsize vCPU/RAM.
	RecOversized RecKind = "oversized"
)

// Utilization summarizes a metric series into the averages + peaks the
// rightsizing rules consume. Computed from vprovider.MetricSample windows.
type Utilization struct {
	Samples       int     `json:"samples"`
	AvgCPUPercent float64 `json:"avgCpuPercent"` // 0..(100*nCPU)
	PeakCPUPercent float64 `json:"peakCpuPercent"`
	AvgMemPercent float64 `json:"avgMemPercent"` // 0..100 of allocation
	PeakMemPercent float64 `json:"peakMemPercent"`
}

// ComputeUtilization reduces a metric series to averages + peaks, normalizing CPU
// against the VM's vCPU count (provider CPUPercent is 0..100*nCPU) and memory
// against the sample's reported limit. It is pure and tolerates empty/partial
// series (returns Samples==0 so callers can skip a "no data" entity rather than
// flag it idle on no evidence).
func ComputeUtilization(samples []vprovider.MetricSample, vcpus int) Utilization {
	var u Utilization
	if len(samples) == 0 {
		return u
	}
	if vcpus < 1 {
		vcpus = 1
	}
	var sumCPU, sumMem float64
	var memSamples int
	for _, s := range samples {
		// Normalize CPU to 0..100 of the VM's total cores.
		cpu := s.CPUPercent / float64(vcpus)
		if cpu < 0 {
			cpu = 0
		}
		sumCPU += cpu
		if cpu > u.PeakCPUPercent {
			u.PeakCPUPercent = cpu
		}
		if s.MemLimitBytes > 0 {
			mem := float64(s.MemUsageBytes) / float64(s.MemLimitBytes) * 100
			if mem < 0 {
				mem = 0
			}
			sumMem += mem
			memSamples++
			if mem > u.PeakMemPercent {
				u.PeakMemPercent = mem
			}
		}
	}
	u.Samples = len(samples)
	u.AvgCPUPercent = sumCPU / float64(len(samples))
	if memSamples > 0 {
		u.AvgMemPercent = sumMem / float64(memSamples)
	}
	return u
}

// Thresholds tune the rightsizing rules. All percentages are 0..100 of the
// entity's own allocation. Sensible defaults beat every competitor's "no
// rightsizing across hypervisors" baseline out of the box.
type Thresholds struct {
	// IdleCPUPercent / IdleMemPercent: at or below BOTH peaks => idle (reclaim).
	IdleCPUPercent float64 `json:"idleCpuPercent"`
	IdleMemPercent float64 `json:"idleMemPercent"`
	// OversizedCPUPercent / OversizedMemPercent: peak utilization at or below these
	// => oversized (downsize). Applied to whichever dimension(s) are slack.
	OversizedCPUPercent float64 `json:"oversizedCpuPercent"`
	OversizedMemPercent float64 `json:"oversizedMemPercent"`
	// MinSamples: require at least this many samples before making a call (avoids
	// flagging an entity on a single noisy reading).
	MinSamples int `json:"minSamples"`
}

// DefaultThresholds are conservative enough to avoid false positives while still
// surfacing real waste.
func DefaultThresholds() Thresholds {
	return Thresholds{
		IdleCPUPercent:      5,
		IdleMemPercent:      10,
		OversizedCPUPercent: 30,
		OversizedMemPercent: 40,
		MinSamples:          3,
	}
}

// Normalize clamps thresholds to sane ranges.
func (t Thresholds) Normalize() Thresholds {
	clampPct := func(v, def float64) float64 {
		if v <= 0 || v > 100 {
			return def
		}
		return v
	}
	d := DefaultThresholds()
	out := Thresholds{
		IdleCPUPercent:      clampPct(t.IdleCPUPercent, d.IdleCPUPercent),
		IdleMemPercent:      clampPct(t.IdleMemPercent, d.IdleMemPercent),
		OversizedCPUPercent: clampPct(t.OversizedCPUPercent, d.OversizedCPUPercent),
		OversizedMemPercent: clampPct(t.OversizedMemPercent, d.OversizedMemPercent),
		MinSamples:          t.MinSamples,
	}
	if out.MinSamples < 1 {
		out.MinSamples = d.MinSamples
	}
	return out
}

// Recommendation is one actionable rightsizing finding with projected savings.
type Recommendation struct {
	EntityID   string  `json:"entityId"`
	Name       string  `json:"name"`
	Domain     Domain  `json:"domain"`
	Kind       string  `json:"kind"`
	Provider   string  `json:"providerId"`
	Host       string  `json:"hostId,omitempty"`
	Cluster    string  `json:"clusterId,omitempty"`
	RecKind    RecKind `json:"recommendation"` // idle|oversized
	Severity   string  `json:"severity"`       // info|warn|critical

	// Current vs suggested allocation.
	CurrentVCPUs   int     `json:"currentVcpus"`
	CurrentMemGB   float64 `json:"currentMemGb"`
	SuggestedVCPUs int     `json:"suggestedVcpus"`
	SuggestedMemGB float64 `json:"suggestedMemGb"`

	Utilization Utilization `json:"utilization"`

	CurrentMonthly   float64 `json:"currentMonthly"`
	ProjectedMonthly float64 `json:"projectedMonthly"`
	MonthlySavings   float64 `json:"monthlySavings"`

	Rationale string `json:"rationale"`
}

// RightsizeInput pairs a priced entity with its observed utilization.
type RightsizeInput struct {
	Cost EntityCost
	Util Utilization
}

// Recommend evaluates the rightsizing rules over priced entities + their
// utilization and returns recommendations sorted by projected monthly savings
// (descending). Only RUNNING entities with enough samples are considered — a
// stopped entity is handled by the insights "reclaim candidate" rule instead.
//
// Pure function: deterministic for a given (rate card, thresholds, inputs).
func Recommend(rc RateCard, th Thresholds, inputs []RightsizeInput) []Recommendation {
	rc = rc.Normalize()
	th = th.Normalize()
	var recs []Recommendation

	for _, in := range inputs {
		c := in.Cost
		u := in.Util
		if !c.Running || u.Samples < th.MinSamples {
			continue
		}

		// IDLE: both CPU and memory peaks sit at/below the idle thresholds.
		if u.PeakCPUPercent <= th.IdleCPUPercent && (u.PeakMemPercent == 0 || u.PeakMemPercent <= th.IdleMemPercent) {
			// Reclaiming an idle entity stops its compute charge (storage stays).
			projected := c.Breakdown.StorageHour * HoursPerMonth
			recs = append(recs, Recommendation{
				EntityID: c.ID, Name: c.Name, Domain: c.Domain, Kind: c.Kind,
				Provider: c.Provider, Host: c.Host, Cluster: c.Cluster,
				RecKind: RecIdle, Severity: "warn",
				CurrentVCPUs: c.VCPUs, CurrentMemGB: c.MemoryGB,
				SuggestedVCPUs: 0, SuggestedMemGB: 0,
				Utilization:      u,
				CurrentMonthly:   c.MonthlyCost,
				ProjectedMonthly: projected,
				MonthlySavings:   c.MonthlyCost - projected,
				Rationale:        "Running but idle: peak CPU and memory are below the idle threshold. Candidate to power off or decommission.",
			})
			continue
		}

		// OVERSIZED: slack in CPU and/or RAM. Halve the slack dimension(s) down to
		// the headroom the peak actually needs (peak * 2, rounded), but never below 1
		// vCPU / 0.5 GiB.
		newVCPU, newMem := c.VCPUs, c.MemoryGB
		cpuSlack := u.PeakCPUPercent <= th.OversizedCPUPercent
		memSlack := u.PeakMemPercent > 0 && u.PeakMemPercent <= th.OversizedMemPercent
		if !cpuSlack && !memSlack {
			continue
		}
		if cpuSlack && c.VCPUs > 1 {
			// Target ~2x peak headroom: needed cores = ceil(peakFraction * cores * 2).
			needed := int(math.Ceil(u.PeakCPUPercent / 100.0 * float64(c.VCPUs) * 2))
			if needed < 1 {
				needed = 1
			}
			if needed < newVCPU {
				newVCPU = needed
			}
		}
		if memSlack && c.MemoryGB > 0.5 {
			needed := u.PeakMemPercent / 100.0 * c.MemoryGB * 2
			if needed < 0.5 {
				needed = 0.5
			}
			// round to nearest 0.5 GiB
			needed = math.Ceil(needed*2) / 2
			if needed < newMem {
				newMem = needed
			}
		}
		// Nothing actually shrank (already minimal) => no recommendation.
		if newVCPU >= c.VCPUs && newMem >= c.MemoryGB {
			continue
		}

		proj := EstimateCost(rc, CostEntity{
			Domain: c.Domain, Running: true,
			VCPUs: newVCPU, MemoryGB: newMem, StorageGB: c.StorageGB,
		})
		recs = append(recs, Recommendation{
			EntityID: c.ID, Name: c.Name, Domain: c.Domain, Kind: c.Kind,
			Provider: c.Provider, Host: c.Host, Cluster: c.Cluster,
			RecKind: RecOversized, Severity: "info",
			CurrentVCPUs: c.VCPUs, CurrentMemGB: c.MemoryGB,
			SuggestedVCPUs: newVCPU, SuggestedMemGB: newMem,
			Utilization:      u,
			CurrentMonthly:   c.MonthlyCost,
			ProjectedMonthly: proj.MonthlyCost,
			MonthlySavings:   c.MonthlyCost - proj.MonthlyCost,
			Rationale:        "Over-provisioned: peak utilization leaves significant slack. Downsizing keeps 2x headroom over observed peak.",
		})
	}

	sort.SliceStable(recs, func(i, j int) bool {
		if recs[i].MonthlySavings != recs[j].MonthlySavings {
			return recs[i].MonthlySavings > recs[j].MonthlySavings
		}
		return recs[i].EntityID < recs[j].EntityID
	})
	return recs
}

// TotalSavings sums the projected monthly savings across recommendations.
func TotalSavings(recs []Recommendation) float64 {
	var t float64
	for _, r := range recs {
		t += r.MonthlySavings
	}
	return t
}
