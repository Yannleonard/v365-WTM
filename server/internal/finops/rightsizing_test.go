package finops

import (
	"testing"
	"time"

	"github.com/gtek-it/castor/server/internal/vprovider"
)

func mkSamples(n int, cpuPct float64, memUsage, memLimit uint64) []vprovider.MetricSample {
	out := make([]vprovider.MetricSample, n)
	base := time.Now()
	for i := range out {
		out[i] = vprovider.MetricSample{
			Timestamp:     base.Add(time.Duration(i) * time.Minute),
			CPUPercent:    cpuPct,
			MemUsageBytes: memUsage,
			MemLimitBytes: memLimit,
		}
	}
	return out
}

func TestComputeUtilizationNormalizesCPU(t *testing.T) {
	// 4 vCPUs, raw CPUPercent 200 (0..400 scale) => 50% of total cores.
	u := ComputeUtilization(mkSamples(5, 200, 2<<30, 4<<30), 4)
	if u.Samples != 5 {
		t.Fatalf("samples = %d", u.Samples)
	}
	if !approx(u.AvgCPUPercent, 50) || !approx(u.PeakCPUPercent, 50) {
		t.Errorf("cpu normalize wrong: avg=%v peak=%v", u.AvgCPUPercent, u.PeakCPUPercent)
	}
	if !approx(u.AvgMemPercent, 50) {
		t.Errorf("mem pct = %v want 50", u.AvgMemPercent)
	}
}

func TestComputeUtilizationEmpty(t *testing.T) {
	u := ComputeUtilization(nil, 2)
	if u.Samples != 0 {
		t.Errorf("empty series should yield 0 samples")
	}
}

func TestRecommendIdle(t *testing.T) {
	rc := DefaultRateCard()
	th := DefaultThresholds()
	cost := EstimateCost(rc, CostEntity{ID: "vm-idle", Domain: DomainVM, Running: true, VCPUs: 8, MemoryGB: 16, StorageGB: 100})
	// 8 vCPUs, raw 8 => 1% of cores; memory 1% of limit. Idle.
	memLimit := uint64(1) << 34
	util := ComputeUtilization(mkSamples(10, 8, memLimit/100, memLimit), 8)
	recs := Recommend(rc, th, []RightsizeInput{{Cost: cost, Util: util}})
	if len(recs) != 1 {
		t.Fatalf("expected 1 idle rec, got %d", len(recs))
	}
	r := recs[0]
	if r.RecKind != RecIdle {
		t.Errorf("kind = %v want idle", r.RecKind)
	}
	if r.MonthlySavings <= 0 {
		t.Errorf("idle savings should be positive, got %v", r.MonthlySavings)
	}
	// Reclaiming keeps only storage cost.
	if !approx(r.ProjectedMonthly, cost.Breakdown.StorageHour*HoursPerMonth) {
		t.Errorf("projected = %v want storage-only %v", r.ProjectedMonthly, cost.Breakdown.StorageHour*HoursPerMonth)
	}
}

func TestRecommendOversized(t *testing.T) {
	rc := DefaultRateCard()
	th := DefaultThresholds()
	cost := EstimateCost(rc, CostEntity{ID: "vm-big", Domain: DomainVM, Running: true, VCPUs: 16, MemoryGB: 32, StorageGB: 200})
	// 16 vCPUs raw 320 => 20% of cores (<=30 oversized). Mem 20% of limit (<=40).
	memLimit := uint64(1) << 35
	util := ComputeUtilization(mkSamples(8, 320, memLimit/5, memLimit), 16)
	recs := Recommend(rc, th, []RightsizeInput{{Cost: cost, Util: util}})
	if len(recs) != 1 {
		t.Fatalf("expected 1 oversized rec, got %d", len(recs))
	}
	r := recs[0]
	if r.RecKind != RecOversized {
		t.Errorf("kind = %v want oversized", r.RecKind)
	}
	if r.SuggestedVCPUs >= cost.VCPUs {
		t.Errorf("should suggest fewer vCPUs, got %d (was %d)", r.SuggestedVCPUs, cost.VCPUs)
	}
	if r.MonthlySavings <= 0 {
		t.Errorf("oversized savings should be positive")
	}
}

func TestRecommendSkipsBusyAndInsufficientSamples(t *testing.T) {
	rc := DefaultRateCard()
	th := DefaultThresholds()
	busy := EstimateCost(rc, CostEntity{ID: "busy", Domain: DomainVM, Running: true, VCPUs: 4, MemoryGB: 8})
	// 90% of cores, 90% mem => not idle, not oversized.
	busyLimit := uint64(1) << 33
	utilBusy := ComputeUtilization(mkSamples(6, 360, busyLimit/10*9, busyLimit), 4)

	// Idle but too few samples.
	thin := EstimateCost(rc, CostEntity{ID: "thin", Domain: DomainVM, Running: true, VCPUs: 4, MemoryGB: 8})
	utilThin := ComputeUtilization(mkSamples(1, 1, 1<<20, 1<<33), 4)

	// Stopped entity is never rightsized here.
	stopped := EstimateCost(rc, CostEntity{ID: "stopped", Domain: DomainVM, Running: false, VCPUs: 4, MemoryGB: 8})

	recs := Recommend(rc, th, []RightsizeInput{
		{Cost: busy, Util: utilBusy},
		{Cost: thin, Util: utilThin},
		{Cost: stopped, Util: ComputeUtilization(mkSamples(10, 1, 1, 1<<33), 4)},
	})
	if len(recs) != 0 {
		t.Fatalf("expected no recs, got %d: %+v", len(recs), recs)
	}
}

func TestTotalSavings(t *testing.T) {
	recs := []Recommendation{{MonthlySavings: 10}, {MonthlySavings: 5.5}}
	if !approx(TotalSavings(recs), 15.5) {
		t.Errorf("total savings = %v", TotalSavings(recs))
	}
}
