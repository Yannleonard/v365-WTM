package finops

import (
	"sort"
	"strings"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/provider"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// Domain distinguishes a priced entity's world.
type Domain string

const (
	DomainVM        Domain = "vm"
	DomainContainer Domain = "container"
)

// CostBreakdown splits an entity's hourly cost into its priced dimensions so the
// UI can show WHY something is expensive (and which lever to pull).
type CostBreakdown struct {
	CPUHour     float64 `json:"cpuHour"`
	RAMHour     float64 `json:"ramHour"`
	StorageHour float64 `json:"storageHour"`
}

// Total is the summed hourly cost of all dimensions.
func (c CostBreakdown) Total() float64 { return c.CPUHour + c.RAMHour + c.StorageHour }

// EntityCost is the estimated cost of one priced entity (a VM or a container).
type EntityCost struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Domain   Domain `json:"domain"`
	Kind     string `json:"kind"`               // hypervisor/orchestrator family
	Provider string `json:"providerId"`         // owning provider instance
	Host     string `json:"hostId,omitempty"`   // placement host
	Cluster  string `json:"clusterId,omitempty"`
	Running  bool   `json:"running"`

	// Allocation that drives the price.
	VCPUs      int     `json:"vcpus"`
	MemoryGB   float64 `json:"memoryGb"`
	StorageGB  float64 `json:"storageGb"`

	Breakdown   CostBreakdown `json:"breakdown"`
	HourlyCost  float64       `json:"hourlyCost"`
	MonthlyCost float64       `json:"monthlyCost"`
}

// CostEntity is the minimal, domain-agnostic input the cost function prices. The
// adapters below build these from inventory VMs and workloads so the math is a
// single pure function regardless of source domain.
type CostEntity struct {
	ID        string
	Name      string
	Domain    Domain
	Kind      string
	Provider  string
	Host      string
	Cluster   string
	Running   bool
	VCPUs     int
	MemoryGB  float64
	StorageGB float64
}

// EstimateCost prices one entity against a (normalized) rate card. Stopped
// entities still incur STORAGE cost (their disks remain provisioned) but no
// compute cost — matching how real clouds bill a powered-off instance. This is a
// pure function: same inputs always yield the same cost.
func EstimateCost(rc RateCard, e CostEntity) EntityCost {
	rc = rc.Normalize()
	cpuRate, ramRate := rc.VCPUHour, rc.GBRAMHour
	if e.Domain == DomainContainer {
		cpuRate, ramRate = rc.ContainerVCPUHour, rc.ContainerGBRAMHour
	}

	var bd CostBreakdown
	if e.Running {
		bd.CPUHour = float64(e.VCPUs) * cpuRate
		bd.RAMHour = e.MemoryGB * ramRate
	}
	bd.StorageHour = e.StorageGB * rc.storageHour()

	hourly := bd.Total()
	return EntityCost{
		ID:          e.ID,
		Name:        e.Name,
		Domain:      e.Domain,
		Kind:        e.Kind,
		Provider:    e.Provider,
		Host:        e.Host,
		Cluster:     e.Cluster,
		Running:     e.Running,
		VCPUs:       e.VCPUs,
		MemoryGB:    e.MemoryGB,
		StorageGB:   e.StorageGB,
		Breakdown:   bd,
		HourlyCost:  hourly,
		MonthlyCost: hourly * HoursPerMonth,
	}
}

// vmStorageGB sums a VM's disk capacities.
func vmStorageGB(vm vprovider.VM) float64 {
	var gb float64
	for _, d := range vm.Disks {
		gb += d.CapacityGB
	}
	return gb
}

// VMToCostEntity adapts a normalized inventory VM into a priceable entity.
func VMToCostEntity(vm vprovider.VM) CostEntity {
	return CostEntity{
		ID:        vm.ID,
		Name:      vm.Name,
		Domain:    DomainVM,
		Kind:      string(vm.Kind),
		Provider:  vm.ProviderID,
		Host:      vm.HostID,
		Cluster:   vm.ClusterID,
		Running:   vm.State == vprovider.StateRunning,
		VCPUs:     vm.VCPUs,
		MemoryGB:  float64(vm.MemoryMB) / 1024.0,
		StorageGB: vmStorageGB(vm),
	}
}

// WorkloadToCostEntity adapts a unified container workload into a priceable
// entity. Containers rarely declare CPU/RAM allocation in the normalized header,
// so the caller may supply observed allocation via the alloc hook (see
// SummarizeUnified, which uses sensible per-container defaults). The unit here is
// allocation, not live usage; live usage drives the rightsizing pass.
func WorkloadToCostEntity(w inventory.UnifiedWorkload, vcpus int, memGB float64) CostEntity {
	return CostEntity{
		ID:        w.ID,
		Name:      w.Name,
		Domain:    DomainContainer,
		Kind:      string(w.Kind),
		Provider:  w.ProviderID,
		Host:      w.HostID,
		Running:   w.State == provider.StateRunning,
		VCPUs:     vcpus,
		MemoryGB:  memGB,
	}
}

// GroupCost is a rolled-up cost for an arbitrary grouping key (hypervisor kind,
// cluster, host, or domain).
type GroupCost struct {
	Key         string  `json:"key"`
	Label       string  `json:"label,omitempty"`
	Count       int     `json:"count"`
	HourlyCost  float64 `json:"hourlyCost"`
	MonthlyCost float64 `json:"monthlyCost"`
}

// Summary is the FinOps overview the /finops/summary endpoint serves.
type Summary struct {
	Currency      string      `json:"currency"`
	TotalHourly   float64     `json:"totalHourly"`
	TotalMonthly  float64     `json:"totalMonthly"`
	VMHourly      float64     `json:"vmHourly"`
	ContainerHourly float64   `json:"containerHourly"`
	Entities      int         `json:"entities"`
	RunningEntities int       `json:"runningEntities"`

	ByDomain     []GroupCost `json:"byDomain"`
	ByHypervisor []GroupCost `json:"byHypervisor"`
	ByCluster    []GroupCost `json:"byCluster"`
	ByHost       []GroupCost `json:"byHost"`

	// TopSpenders is the costliest N entities (default 10), descending by monthly cost.
	TopSpenders []EntityCost `json:"topSpenders"`

	// PotentialMonthlySavings is the sum of projected savings from all rightsizing
	// recommendations, so the headline KPI is one number.
	PotentialMonthlySavings float64 `json:"potentialMonthlySavings"`
	Recommendations         int     `json:"recommendations"`
}

// defaultContainerVCPU / defaultContainerMemGB are the assumed allocation for a
// running container that does not declare resource requests in the normalized
// header. They keep the container domain visible in the cost view even when the
// orchestrator does not surface limits. Tunable constants, not magic in the math.
const (
	defaultContainerVCPU  = 1
	defaultContainerMemGB = 0.5
)

// PriceInventory prices every VM and container in a unified snapshot, returning
// the per-entity costs in a stable order (domain, then id).
func PriceInventory(rc RateCard, u inventory.Unified) []EntityCost {
	out := make([]EntityCost, 0, len(u.VMs)+len(u.Workloads))
	for _, vm := range u.VMs {
		out = append(out, EstimateCost(rc, VMToCostEntity(vm)))
	}
	for _, w := range u.Workloads {
		vcpu, mem := 0, 0.0
		if w.State == provider.StateRunning {
			vcpu, mem = defaultContainerVCPU, defaultContainerMemGB
		}
		out = append(out, EstimateCost(rc, WorkloadToCostEntity(w, vcpu, mem)))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Domain != out[j].Domain {
			return out[i].Domain < out[j].Domain
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Summarize rolls priced entities into the FinOps overview. topN bounds the
// top-spenders list (<=0 defaults to 10). savings is the precomputed total of
// rightsizing savings (pass 0 if not computed yet).
func Summarize(rc RateCard, costs []EntityCost, topN int, savings float64, recCount int) Summary {
	rc = rc.Normalize()
	if topN <= 0 {
		topN = 10
	}
	s := Summary{Currency: rc.Currency}

	byDomain := map[string]*GroupCost{}
	byHV := map[string]*GroupCost{}
	byCluster := map[string]*GroupCost{}
	byHost := map[string]*GroupCost{}
	add := func(m map[string]*GroupCost, key, label string, c EntityCost) {
		if key == "" {
			return
		}
		g := m[key]
		if g == nil {
			g = &GroupCost{Key: key, Label: label}
			m[key] = g
		}
		g.Count++
		g.HourlyCost += c.HourlyCost
		g.MonthlyCost += c.MonthlyCost
	}

	for _, c := range costs {
		s.Entities++
		s.TotalHourly += c.HourlyCost
		if c.Running {
			s.RunningEntities++
		}
		if c.Domain == DomainVM {
			s.VMHourly += c.HourlyCost
		} else {
			s.ContainerHourly += c.HourlyCost
		}
		add(byDomain, string(c.Domain), domainLabel(c.Domain), c)
		add(byHV, c.Kind, "", c)
		add(byCluster, c.Cluster, "", c)
		add(byHost, c.Host, "", c)
	}
	s.TotalMonthly = s.TotalHourly * HoursPerMonth

	s.ByDomain = sortGroups(byDomain)
	s.ByHypervisor = sortGroups(byHV)
	s.ByCluster = sortGroups(byCluster)
	s.ByHost = sortGroups(byHost)

	// Top spenders by monthly cost (descending), tie-broken by id for stability.
	top := append([]EntityCost(nil), costs...)
	sort.SliceStable(top, func(i, j int) bool {
		if top[i].MonthlyCost != top[j].MonthlyCost {
			return top[i].MonthlyCost > top[j].MonthlyCost
		}
		return top[i].ID < top[j].ID
	})
	if len(top) > topN {
		top = top[:topN]
	}
	s.TopSpenders = top

	s.PotentialMonthlySavings = savings
	s.Recommendations = recCount
	return s
}

func sortGroups(m map[string]*GroupCost) []GroupCost {
	out := make([]GroupCost, 0, len(m))
	for _, g := range m {
		out = append(out, *g)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].MonthlyCost != out[j].MonthlyCost {
			return out[i].MonthlyCost > out[j].MonthlyCost
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func domainLabel(d Domain) string {
	switch d {
	case DomainVM:
		return "Virtual machines"
	case DomainContainer:
		return "Containers"
	default:
		return strings.Title(string(d))
	}
}
