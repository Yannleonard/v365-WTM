package finops

import (
	"math"
	"testing"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

func approx(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

func TestEstimateCostRunningVM(t *testing.T) {
	rc := RateCard{Currency: "USD", VCPUHour: 0.04, GBRAMHour: 0.005, GBStorageMonth: 0.10}
	e := CostEntity{Domain: DomainVM, Running: true, VCPUs: 4, MemoryGB: 8, StorageGB: 100}
	got := EstimateCost(rc, e)

	wantCPU := 4 * 0.04
	wantRAM := 8 * 0.005
	wantStorage := 100 * (0.10 / HoursPerMonth)
	if !approx(got.Breakdown.CPUHour, wantCPU) {
		t.Errorf("cpu hour = %v want %v", got.Breakdown.CPUHour, wantCPU)
	}
	if !approx(got.Breakdown.RAMHour, wantRAM) {
		t.Errorf("ram hour = %v want %v", got.Breakdown.RAMHour, wantRAM)
	}
	if !approx(got.Breakdown.StorageHour, wantStorage) {
		t.Errorf("storage hour = %v want %v", got.Breakdown.StorageHour, wantStorage)
	}
	if !approx(got.HourlyCost, wantCPU+wantRAM+wantStorage) {
		t.Errorf("hourly = %v", got.HourlyCost)
	}
	if !approx(got.MonthlyCost, got.HourlyCost*HoursPerMonth) {
		t.Errorf("monthly mismatch")
	}
}

func TestEstimateCostStoppedVMNoComputeCharge(t *testing.T) {
	rc := DefaultRateCard()
	e := CostEntity{Domain: DomainVM, Running: false, VCPUs: 8, MemoryGB: 16, StorageGB: 50}
	got := EstimateCost(rc, e)
	if got.Breakdown.CPUHour != 0 || got.Breakdown.RAMHour != 0 {
		t.Errorf("stopped VM should incur no compute cost, got cpu=%v ram=%v", got.Breakdown.CPUHour, got.Breakdown.RAMHour)
	}
	if got.Breakdown.StorageHour <= 0 {
		t.Errorf("stopped VM should still incur storage cost")
	}
}

func TestContainerUsesContainerRates(t *testing.T) {
	rc := RateCard{VCPUHour: 0.10, GBRAMHour: 0.10, ContainerVCPUHour: 0.01, ContainerGBRAMHour: 0.01}
	c := EstimateCost(rc, CostEntity{Domain: DomainContainer, Running: true, VCPUs: 2, MemoryGB: 2})
	want := 2*0.01 + 2*0.01
	if !approx(c.HourlyCost, want) {
		t.Errorf("container hourly = %v want %v", c.HourlyCost, want)
	}
}

func TestNormalizeFoldsContainerRates(t *testing.T) {
	rc := RateCard{VCPUHour: 0.05, GBRAMHour: 0.01}.Normalize()
	if rc.ContainerVCPUHour != 0.05 || rc.ContainerGBRAMHour != 0.01 {
		t.Errorf("container rates should fold onto VM rates, got %+v", rc)
	}
	if rc.Currency != "USD" {
		t.Errorf("empty currency should default to USD")
	}
}

func TestPriceAndSummarizeUnified(t *testing.T) {
	rc := DefaultRateCard()
	u := inventory.Unified{
		VMs: []vprovider.VM{
			{ID: "vm1", Name: "big", Kind: vprovider.KindVMware, ProviderID: "p1", ClusterID: "c1",
				State: vprovider.StateRunning, VCPUs: 16, MemoryMB: 65536,
				Disks: []vprovider.Disk{{CapacityGB: 500}}},
			{ID: "vm2", Name: "small", Kind: vprovider.KindKVM, ProviderID: "p2",
				State: vprovider.StateRunning, VCPUs: 1, MemoryMB: 1024,
				Disks: []vprovider.Disk{{CapacityGB: 20}}},
		},
		Workloads: []inventory.UnifiedWorkload{
			{HostID: "h1", Workload: provider.Workload{ID: "ct1", Name: "web", Kind: provider.KindDocker, State: provider.StateRunning}},
		},
	}
	costs := PriceInventory(rc, u)
	if len(costs) != 3 {
		t.Fatalf("expected 3 priced entities, got %d", len(costs))
	}
	sum := Summarize(rc, costs, 10, 0, 0)
	if sum.Entities != 3 || sum.RunningEntities != 3 {
		t.Errorf("counts wrong: %+v", sum)
	}
	if sum.TotalHourly <= 0 {
		t.Errorf("total hourly should be positive")
	}
	// big VM must be the #1 spender.
	if len(sum.TopSpenders) == 0 || sum.TopSpenders[0].ID != "vm1" {
		t.Errorf("expected vm1 as top spender, got %+v", sum.TopSpenders)
	}
	// byDomain should split vm vs container.
	if len(sum.ByDomain) != 2 {
		t.Errorf("expected 2 domains, got %d", len(sum.ByDomain))
	}
	if sum.VMHourly <= sum.ContainerHourly {
		// not strictly required but with these fixtures VMs dominate
		t.Logf("vmHourly=%v containerHourly=%v", sum.VMHourly, sum.ContainerHourly)
	}
}

func TestRateCardRoundTrip(t *testing.T) {
	rc := RateCard{Currency: "EUR", VCPUHour: 0.03, GBRAMHour: 0.004, GBStorageMonth: 0.08}
	s := MarshalRateCard(rc)
	back, ok := UnmarshalRateCard(s)
	if !ok {
		t.Fatal("round trip should succeed")
	}
	if back.Currency != "EUR" || !approx(back.VCPUHour, 0.03) {
		t.Errorf("round trip mismatch: %+v", back)
	}
	// Empty / invalid input falls back to default.
	if _, ok := UnmarshalRateCard(""); ok {
		t.Errorf("empty input should report ok=false")
	}
	if _, ok := UnmarshalRateCard("{not json"); ok {
		t.Errorf("malformed input should report ok=false")
	}
}
