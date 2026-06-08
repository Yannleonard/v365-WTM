package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/insights"
	"github.com/gtek-it/castor/server/internal/store"
)

// mountInsightsRoutes wires the UniHV cross-domain insights feed (read-only,
// gated by insights.read at global scope).
func (s *Server) mountInsightsRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("insights.read", authz.GlobalScope)).Get("/insights", s.Insights)
}

// Insights serves GET /insights — the actionable drift/health/best-practice feed
// computed over the unified VM + container inventory. Thresholds come from the
// settings table (engine defaults when absent/invalid).
func (s *Server) Insights(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := contextWithTimeout(r, 15*time.Second)
	defer cancel()

	var th insights.Thresholds
	if raw := s.store.GetSettingDefault(ctx, store.SettingInsightsThresholds, ""); raw != "" {
		_ = json.Unmarshal([]byte(raw), &th)
	}
	th = th.Normalize()

	now := time.Now().UTC()
	u := s.agg.All(ctx, now)
	feed := insights.Analyze(u, th, now)
	ok(w, feed)
}
