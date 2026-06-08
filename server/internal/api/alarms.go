package api

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/gtek-it/castor/server/internal/alarms"
	"github.com/gtek-it/castor/server/internal/authz"
	"github.com/gtek-it/castor/server/internal/store"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// --- adapters bridging the alarms engine to the store + hypervisor registry ---

// alarmStoreAdapter maps the durable store rows to the alarms engine's interfaces.
type alarmStoreAdapter struct{ st *store.Store }

func (a alarmStoreAdapter) ListDefinitions(ctx context.Context) ([]alarms.Definition, error) {
	rows, err := a.st.ListAlarmDefinitions(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]alarms.Definition, 0, len(rows))
	for _, d := range rows {
		out = append(out, defToEngine(d))
	}
	return out, nil
}

func (a alarmStoreAdapter) ListChannels(ctx context.Context) ([]alarms.Channel, error) {
	rows, err := a.st.ListAlarmChannels(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]alarms.Channel, 0, len(rows))
	for _, c := range rows {
		out = append(out, chToEngine(c))
	}
	return out, nil
}

func (a alarmStoreAdapter) GetChannel(ctx context.Context, id string) (alarms.Channel, bool) {
	c, err := a.st.GetAlarmChannel(ctx, id)
	if err != nil {
		return alarms.Channel{}, false
	}
	return chToEngine(c), true
}

func (a alarmStoreAdapter) SaveInstances(ctx context.Context, insts []alarms.Instance) error {
	rows := make([]*store.AlarmInstance, 0, len(insts))
	for _, in := range insts {
		rows = append(rows, &store.AlarmInstance{
			ID: in.ID, DefinitionID: in.DefinitionID, DefinitionName: in.DefinitionName,
			ObjectID: in.ObjectID, ObjectName: in.ObjectName, ObjectType: string(in.ObjectType),
			Severity: string(in.Severity), Metric: string(in.Metric), Value: in.Value,
			StateRaw: in.StateRaw, RaisedAt: in.RaisedAt.Unix(),
			LastNotifiedAt: unixOrZero(in.LastNotifiedAt),
		})
	}
	return a.st.ReplaceAlarmInstances(ctx, rows)
}

func (a alarmStoreAdapter) LoadInstances(ctx context.Context) ([]alarms.Instance, error) {
	rows, err := a.st.LoadAlarmInstances(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]alarms.Instance, 0, len(rows))
	for _, in := range rows {
		out = append(out, alarms.Instance{
			ID: in.ID, DefinitionID: in.DefinitionID, DefinitionName: in.DefinitionName,
			ObjectID: in.ObjectID, ObjectName: in.ObjectName, ObjectType: alarms.TargetType(in.ObjectType),
			Severity: alarms.Severity(in.Severity), Metric: alarms.Metric(in.Metric), Value: in.Value,
			StateRaw: in.StateRaw, State: alarms.StateActive,
			RaisedAt: time.Unix(in.RaisedAt, 0).UTC(),
		})
	}
	return out, nil
}

func unixOrZero(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

// alarmMetricsAdapter resolves the latest metric sample for a VM/host via the
// hypervisor registry's GetMetrics (a short recent window, last sample).
type alarmMetricsAdapter struct{ vreg *vprovider.Registry }

func (a alarmMetricsAdapter) LatestSample(ctx context.Context, providerID, entityID string) (*vprovider.MetricSample, bool) {
	if a.vreg == nil {
		return nil, false
	}
	p, ok := a.vreg.Get(providerID)
	if !ok {
		return nil, false
	}
	if !p.Capabilities().Has(vprovider.CapMetrics) {
		return nil, false
	}
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	now := time.Now()
	series, err := p.GetMetrics(cctx, entityID, vprovider.MetricWindow{
		Since: now.Add(-5 * time.Minute), Until: now, StepSecond: 10,
	})
	if err != nil || series == nil || len(series.Samples) == 0 {
		return nil, false
	}
	last := series.Samples[len(series.Samples)-1]
	return &last, true
}

// --- mappers between store rows and engine types ---

func defToEngine(d *store.AlarmDefinition) alarms.Definition {
	return alarms.Definition{
		ID: d.ID, Name: d.Name,
		Target: alarms.TargetType(d.Target), Metric: alarms.Metric(d.Metric),
		Comparator: alarms.Comparator(d.Comparator), Threshold: d.Threshold,
		StateValue: d.StateValue, DurationSec: d.DurationSec,
		Severity: alarms.Severity(d.Severity), Enabled: d.Enabled,
		NotifyChannelIDs: d.NotifyChannelIDs,
	}
}

func chToEngine(c *store.AlarmChannel) alarms.Channel {
	return alarms.Channel{ID: c.ID, Name: c.Name, Type: alarms.ChannelType(c.Type), Config: c.Config}
}

// --- request bodies ---

type alarmDefInput struct {
	Name             string   `json:"name"`
	Target           string   `json:"target"`
	Metric           string   `json:"metric"`
	Comparator       string   `json:"comparator"`
	Threshold        float64  `json:"threshold"`
	StateValue       string   `json:"stateValue"`
	DurationSec      int      `json:"durationSec"`
	Severity         string   `json:"severity"`
	Enabled          bool     `json:"enabled"`
	NotifyChannelIDs []string `json:"notifyChannelIds"`
}

func (in alarmDefInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if !alarms.TargetType(in.Target).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid target (vm|host|datastore)")
	}
	if !alarms.Metric(in.Metric).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid metric (cpu|memory|disk|storage_pct|state)")
	}
	if !alarms.Severity(in.Severity).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid severity (info|warning|critical)")
	}
	if alarms.Metric(in.Metric) == alarms.MetricState {
		if strings.TrimSpace(in.StateValue) == "" {
			return authz.Errorf(authz.ErrValidation, "stateValue is required for metric=state")
		}
	} else if !alarms.Comparator(in.Comparator).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid comparator (gt|lt|eq)")
	}
	if in.DurationSec < 0 {
		return authz.Errorf(authz.ErrValidation, "durationSec must be non-negative")
	}
	return nil
}

type alarmChannelInput struct {
	Name   string `json:"name"`
	Type   string `json:"type"`
	Config string `json:"config"`
}

func (in alarmChannelInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if !alarms.ChannelType(in.Type).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid type (webhook|email-stub)")
	}
	if alarms.ChannelType(in.Type) == alarms.ChannelWebhook && strings.TrimSpace(in.Config) == "" {
		return authz.Errorf(authz.ErrValidation, "config (webhook URL) is required for a webhook channel")
	}
	return nil
}

// --- definition handlers ---

// ListAlarmDefinitions returns all alarm definitions.
func (s *Server) ListAlarmDefinitions(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListAlarmDefinitions(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]*store.AlarmDefinition, 0, len(rows))
	out = append(out, rows...)
	ok(w, out)
}

// CreateAlarmDefinition persists a new alarm definition.
func (s *Server) CreateAlarmDefinition(w http.ResponseWriter, r *http.Request) {
	var in alarmDefInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.AlarmDefinition{
		ID: store.NewUUID(), Name: in.Name, Target: in.Target, Metric: in.Metric,
		Comparator: in.Comparator, Threshold: in.Threshold, StateValue: in.StateValue,
		DurationSec: in.DurationSec, Severity: in.Severity, Enabled: in.Enabled,
		NotifyChannelIDs: in.NotifyChannelIDs,
	}
	if rec.Comparator == "" {
		rec.Comparator = string(alarms.CmpGT)
	}
	if err := s.store.CreateAlarmDefinition(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	s.kickAlarmEval()
	created(w, rec)
}

// UpdateAlarmDefinition replaces a definition (used for enable/disable + edits).
func (s *Server) UpdateAlarmDefinition(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := s.store.GetAlarmDefinition(r.Context(), id); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	var in alarmDefInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.AlarmDefinition{
		ID: id, Name: in.Name, Target: in.Target, Metric: in.Metric,
		Comparator: in.Comparator, Threshold: in.Threshold, StateValue: in.StateValue,
		DurationSec: in.DurationSec, Severity: in.Severity, Enabled: in.Enabled,
		NotifyChannelIDs: in.NotifyChannelIDs,
	}
	if rec.Comparator == "" {
		rec.Comparator = string(alarms.CmpGT)
	}
	if err := s.store.UpdateAlarmDefinition(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	s.kickAlarmEval()
	ok(w, rec)
}

// DeleteAlarmDefinition removes a definition + its active instances.
func (s *Server) DeleteAlarmDefinition(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAlarmDefinition(r.Context(), chi.URLParam(r, "id")); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	s.kickAlarmEval()
	noContent(w)
}

// ActiveAlarms returns the current firing alarm instances (engine live state).
func (s *Server) ActiveAlarms(w http.ResponseWriter, r *http.Request) {
	out := s.alarmEng.Active()
	if out == nil {
		out = []alarms.Instance{}
	}
	ok(w, out)
}

// --- channel handlers ---

// ListAlarmChannels returns all notification channels.
func (s *Server) ListAlarmChannels(w http.ResponseWriter, r *http.Request) {
	rows, err := s.store.ListAlarmChannels(r.Context())
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	out := make([]*store.AlarmChannel, 0, len(rows))
	out = append(out, rows...)
	ok(w, out)
}

// CreateAlarmChannel persists a new notification channel.
func (s *Server) CreateAlarmChannel(w http.ResponseWriter, r *http.Request) {
	var in alarmChannelInput
	if err := decodeJSON(w, r, &in); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	if err := in.validate(); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.AlarmChannel{ID: store.NewUUID(), Name: in.Name, Type: in.Type, Config: in.Config}
	if err := s.store.CreateAlarmChannel(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	created(w, rec)
}

// DeleteAlarmChannel removes a notification channel.
func (s *Server) DeleteAlarmChannel(w http.ResponseWriter, r *http.Request) {
	if err := s.store.DeleteAlarmChannel(r.Context(), chi.URLParam(r, "id")); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	noContent(w)
}

// TestAlarmChannel sends a synthetic test notification to a channel (real webhook
// POST / logged email-stub) so an operator can verify delivery before relying on it.
func (s *Server) TestAlarmChannel(w http.ResponseWriter, r *http.Request) {
	c, err := s.store.GetAlarmChannel(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	n := &alarms.HTTPNotifier{}
	n.SendTest(context.Background(), chToEngine(c))
	ok(w, ActionResult{OK: true})
}

// kickAlarmEval triggers an immediate evaluation pass so a just-created firing
// alarm shows up without waiting for the next tick. Best-effort, off the request.
func (s *Server) kickAlarmEval() {
	go s.alarmEng.Tick(context.Background(), time.Now().UTC())
}

// StartAlarmEngine resumes persisted instances and starts the evaluation ticker.
// Called once at startup (after providers are registered so metrics resolve).
func (s *Server) StartAlarmEngine(ctx context.Context) {
	s.alarmEng.Resume(ctx)
	s.alarmEng.Start(ctx)
}

// mountAlarmRoutes wires the vSphere-style alarms surface. Reads use alarms.read;
// definition/channel writes + channel test are admin-grade (alarms.write).
// Mutations follow AuditWrap -> RequireAAL -> RequirePermission. Global-scoped:
// an alarm definition spans every object across every hypervisor.
func (s *Server) mountAlarmRoutes(pr chi.Router) {
	az := s.authz
	pr.With(az.RequirePermission("alarms.read", nil)).
		Get("/alarms/definitions", s.ListAlarmDefinitions)
	pr.With(az.RequirePermission("alarms.read", nil)).
		Get("/alarms/active", s.ActiveAlarms)
	pr.With(az.RequirePermission("alarms.read", nil)).
		Get("/alarms/channels", s.ListAlarmChannels)

	pr.With(az.AuditWrap("alarms.definition.create"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Post("/alarms/definitions", s.CreateAlarmDefinition)
	pr.With(az.AuditWrap("alarms.definition.update"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Put("/alarms/definitions/{id}", s.UpdateAlarmDefinition)
	pr.With(az.AuditWrap("alarms.definition.delete"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Delete("/alarms/definitions/{id}", s.DeleteAlarmDefinition)

	pr.With(az.AuditWrap("alarms.channel.create"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Post("/alarms/channels", s.CreateAlarmChannel)
	pr.With(az.AuditWrap("alarms.channel.delete"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Delete("/alarms/channels/{id}", s.DeleteAlarmChannel)
	pr.With(az.AuditWrap("alarms.channel.test"), az.RequireAAL, az.RequirePermission("alarms.write", nil)).
		Post("/alarms/channels/{id}/test", s.TestAlarmChannel)
}
