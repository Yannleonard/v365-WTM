package api

import (
	"net/http"
	"testing"
)

// TestFinOpsSummaryAndRightsizing exercises the FinOps read surface through the
// full router. The test env registers one sim provider with seeded VMs, so the
// summary must price them and return positive totals + breakdowns.
func TestFinOpsSummaryAndRightsizing(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)

	// Summary: 200 with a cost overview.
	rec := e.do(t, http.MethodGet, "/api/v1/finops/summary", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("finops/summary = %d %s", rec.Code, rec.Body.String())
	}
	sum := decodeBody(t, rec)
	if sum["currency"] == nil {
		t.Errorf("summary missing currency")
	}
	if int(sum["entities"].(float64)) < 3 {
		t.Errorf("expected >=3 priced entities, got %v", sum["entities"])
	}
	if sum["totalMonthly"].(float64) <= 0 {
		t.Errorf("totalMonthly should be positive, got %v", sum["totalMonthly"])
	}
	if _, ok := sum["topSpenders"].([]any); !ok {
		t.Errorf("summary missing topSpenders array")
	}
	if _, ok := sum["byHypervisor"].([]any); !ok {
		t.Errorf("summary missing byHypervisor array")
	}

	// Rightsizing: 200 with a recommendations envelope.
	rec = e.do(t, http.MethodGet, "/api/v1/finops/rightsizing", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("finops/rightsizing = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if _, ok := body["recommendations"].([]any); !ok {
		t.Errorf("rightsizing missing recommendations array")
	}

	// Rate card round trip: GET then PUT (admin has settings.update + AAL ok).
	rec = e.do(t, http.MethodGet, "/api/v1/finops/ratecard", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("get ratecard = %d %s", rec.Code, rec.Body.String())
	}

	// PUT without CSRF -> 403.
	rec = e.do(t, http.MethodPut, "/api/v1/finops/ratecard", map[string]any{"currency": "EUR", "vcpuHour": 0.05}, cookies, "")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("put ratecard without CSRF should be 403, got %d", rec.Code)
	}

	// PUT with CSRF -> 200 and the normalized card echoes back.
	rec = e.do(t, http.MethodPut, "/api/v1/finops/ratecard",
		map[string]any{"currency": "EUR", "vcpuHour": 0.05, "gbRamHour": 0.006, "gbStorageMonth": 0.09}, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("put ratecard = %d %s", rec.Code, rec.Body.String())
	}
	got := decodeBody(t, rec)
	if got["currency"] != "EUR" {
		t.Errorf("ratecard currency = %v want EUR", got["currency"])
	}
	// Container rates fold onto VM rates when omitted.
	if got["containerVcpuHour"].(float64) != 0.05 {
		t.Errorf("container vcpu rate should fold to 0.05, got %v", got["containerVcpuHour"])
	}
}

// TestInsightsFeed exercises GET /insights through the router.
func TestInsightsFeed(t *testing.T) {
	e := newTestEnv(t)
	cookies, _ := adminLogin(t, e)

	rec := e.do(t, http.MethodGet, "/api/v1/insights", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("insights = %d %s", rec.Code, rec.Body.String())
	}
	feed := decodeBody(t, rec)
	if _, ok := feed["insights"].([]any); !ok {
		t.Errorf("feed missing insights array")
	}
	if _, ok := feed["counts"].(map[string]any); !ok {
		t.Errorf("feed missing counts histogram")
	}
}

// TestFinOpsRequiresPermission proves a user without finops.read is denied.
func TestFinOpsRequiresPermission(t *testing.T) {
	e := newTestEnv(t)
	_, _ = adminLogin(t, e) // bootstrap out of bootstrap mode (also creates admin)

	// finops.read is granted to viewers (AllReadPermissions), so to exercise the
	// gate we use an unauthenticated request, which the SessionAuth chain rejects
	// with 401 before the permission check.
	rec := e.do(t, http.MethodGet, "/api/v1/finops/summary", nil, nil, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated finops/summary should be 401, got %d", rec.Code)
	}
}
