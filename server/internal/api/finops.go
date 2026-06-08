package api

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/finops"
	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/store"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// mountFinOpsRoutes wires the UniHV unified cost & rightsizing surface. The cost
// summary + rightsizing recommendations are read-only analytics over the unified
// inventory (gated by finops.read, global scope). The rate card read reuses
// finops.read; editing it is admin-grade configuration (settings.update) and is
// audited like the other settings mutations (AuditWrap OUTERMOST -> RequireAAL ->
// RequirePermission -> handler), so a denied edit still records exactly one row.
func (s *Server) mountFinOpsRoutes(pr chi.Router) {
	az := s.authz
	g := authz.GlobalScope

	pr.With(az.RequirePermission("finops.read", g)).Get("/finops/summary", s.FinOpsSummary)
	pr.With(az.RequirePermission("finops.read", g)).Get("/finops/rightsizing", s.FinOpsRightsizing)
	pr.With(az.RequirePermission("finops.read", g)).Get("/finops/ratecard", s.GetRateCard)
	pr.With(az.AuditWrap("finops.ratecard.update"), az.RequireAAL, az.RequirePermission("settings.update", g)).
		Put("/finops/ratecard", s.UpdateRateCard)
}

// loadRateCard reads the persisted rate card, falling back to the engine default
// when the settings row is absent or malformed.
func (s *Server) loadRateCard(ctx context.Context) finops.RateCard {
	raw := s.store.GetSettingDefault(ctx, store.SettingFinOpsRateCard, "")
	rc, _ := finops.UnmarshalRateCard(raw)
	return rc.Normalize()
}

// loadRightsizeThresholds reads persisted rightsizing thresholds (engine defaults
// when absent/invalid).
func (s *Server) loadRightsizeThresholds(ctx context.Context) finops.Thresholds {
	raw := s.store.GetSettingDefault(ctx, store.SettingFinOpsThresholds, "")
	var th finops.Thresholds
	if raw != "" {
		// best-effort; Normalize() repairs any out-of-range/zero fields.
		_ = json.Unmarshal([]byte(raw), &th)
	}
	return th.Normalize()
}

// metricWindow is the look-back the rightsizing pass samples over.
const finopsMetricWindow = 24 * time.Hour

// collectUtilization fetches a metric series per RUNNING VM (concurrently,
// bounded) and reduces it to utilization keyed by the VM's provider+id. A
// provider without metrics support or a failing read simply yields no entry, so
// that VM is skipped by the rightsizing rules (no recommendation on no evidence).
func (s *Server) collectUtilization(ctx context.Context, vms []vprovider.VM) map[string]finops.Utilization {
	out := map[string]finops.Utilization{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 8) // cap concurrent provider calls

	now := time.Now().UTC()
	window := vprovider.MetricWindow{Since: now.Add(-finopsMetricWindow), Until: now, StepSecond: 300}

	for _, vm := range vms {
		if vm.State != vprovider.StateRunning {
			continue
		}
		p, ok := s.vreg.Get(vm.ProviderID)
		if !ok {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(vm vprovider.VM, p vprovider.HypervisorProvider) {
			defer wg.Done()
			defer func() { <-sem }()
			mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			series, err := p.GetMetrics(mctx, vm.ID, window)
			if err != nil || series == nil || len(series.Samples) == 0 {
				return
			}
			u := finops.ComputeUtilization(series.Samples, vm.VCPUs)
			mu.Lock()
			out[vm.ProviderID+"/"+vm.ID] = u
			mu.Unlock()
		}(vm, p)
	}
	wg.Wait()
	return out
}

// buildRightsizing prices the inventory, joins per-VM utilization, and runs the
// rightsizing rules. Returns the recommendations (sorted by savings).
func (s *Server) buildRightsizing(ctx context.Context, rc finops.RateCard, th finops.Thresholds, u inventory.Unified) []finops.Recommendation {
	costs := finops.PriceInventory(rc, u)
	util := s.collectUtilization(ctx, u.VMs)

	inputs := make([]finops.RightsizeInput, 0, len(costs))
	for _, c := range costs {
		if c.Domain != finops.DomainVM {
			continue // containers have no per-entity metric series in this pass
		}
		uu, ok := util[c.Provider+"/"+c.ID]
		if !ok {
			continue
		}
		inputs = append(inputs, finops.RightsizeInput{Cost: c, Util: uu})
	}
	return finops.Recommend(rc, th, inputs)
}

// FinOpsSummary serves GET /finops/summary — the cost overview (totals,
// breakdowns, top spenders) plus the headline savings number.
func (s *Server) FinOpsSummary(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	rc := s.loadRateCard(ctx)
	th := s.loadRightsizeThresholds(ctx)
	u := s.agg.All(ctx, time.Now().UTC())

	costs := finops.PriceInventory(rc, u)
	recs := s.buildRightsizing(ctx, rc, th, u)
	summary := finops.Summarize(rc, costs, 10, finops.TotalSavings(recs), len(recs))
	ok(w, summary)
}

// FinOpsRightsizing serves GET /finops/rightsizing — the recommendation list.
func (s *Server) FinOpsRightsizing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 20*time.Second)
	defer cancel()

	rc := s.loadRateCard(ctx)
	th := s.loadRightsizeThresholds(ctx)
	u := s.agg.All(ctx, time.Now().UTC())
	recs := s.buildRightsizing(ctx, rc, th, u)
	if recs == nil {
		recs = []finops.Recommendation{}
	}
	ok(w, map[string]any{
		"recommendations":         recs,
		"potentialMonthlySavings": finops.TotalSavings(recs),
		"currency":                rc.Currency,
	})
}

// GetRateCard serves GET /finops/ratecard — the current (normalized) rate card.
func (s *Server) GetRateCard(w http.ResponseWriter, r *http.Request) {
	ok(w, s.loadRateCard(r.Context()))
}

// UpdateRateCard serves PUT /finops/ratecard — persists a new rate card. Gated by
// settings.update (admin-grade). The card is normalized (negatives clamped,
// container rates folded) before persisting so the stored value is always sane.
func (s *Server) UpdateRateCard(w http.ResponseWriter, r *http.Request) {
	var rc finops.RateCard
	if err := decodeJSON(w, r, &rc); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rc = rc.Normalize()
	if err := s.store.SetSetting(r.Context(), store.SettingFinOpsRateCard, finops.MarshalRateCard(rc)); err != nil {
		writeMapped(w, r, err)
		return
	}
	ok(w, rc)
}
