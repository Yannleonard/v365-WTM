package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
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
// secretKey opens the sealed SMTP password so the engine can authenticate on
// dispatch (it is NEVER exposed beyond the engine notifier).
type alarmStoreAdapter struct {
	st        *store.Store
	secretKey []byte
}

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
		out = append(out, a.chToEngine(c))
	}
	return out, nil
}

func (a alarmStoreAdapter) GetChannel(ctx context.Context, id string) (alarms.Channel, bool) {
	c, err := a.st.GetAlarmChannel(ctx, id)
	if err != nil {
		return alarms.Channel{}, false
	}
	return a.chToEngine(c), true
}

// chToEngine maps a store channel row to the engine Channel, opening the sealed
// SMTP password into Channel.Secret so the notifier can authenticate. A failure to
// open is logged WITHOUT the secret and yields an empty password (the SMTP send
// then surfaces a clear auth error rather than silently using a wrong secret).
func (a alarmStoreAdapter) chToEngine(c *store.AlarmChannel) alarms.Channel {
	ch := alarms.Channel{ID: c.ID, Name: c.Name, Type: alarms.ChannelType(c.Type), Config: c.Config}
	if len(c.ConfigSecret) > 0 && len(a.secretKey) == 32 {
		if pw, err := authz.OpenSecret(a.secretKey, c.ConfigSecret); err == nil {
			ch.Secret = string(pw)
		} else {
			log.Printf("alarms: failed to open sealed secret for channel %s: %v", c.ID, err)
		}
	}
	return ch
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

// smtpChannelInput is the structured SMTP config sent by the UI when type=smtp.
// The password is accepted here, sealed at rest (authz.SealSecret) and NEVER
// stored/returned/logged in plaintext.
type smtpChannelInput struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	From     string `json:"from"`
	To       string `json:"to"`
	UseTLS   bool   `json:"useTLS"`
	StartTLS bool   `json:"startTLS"`
}

type alarmChannelInput struct {
	Name string `json:"name"`
	Type string `json:"type"`
	// Config is the raw destination for webhook (URL) / email-stub (address).
	Config string `json:"config"`
	// SMTP carries the structured config when Type == smtp.
	SMTP *smtpChannelInput `json:"smtp"`
}

func (in alarmChannelInput) validate() error {
	if strings.TrimSpace(in.Name) == "" {
		return authz.Errorf(authz.ErrValidation, "name is required")
	}
	if !alarms.ChannelType(in.Type).Valid() {
		return authz.Errorf(authz.ErrValidation, "invalid type (webhook|email-stub|smtp)")
	}
	switch alarms.ChannelType(in.Type) {
	case alarms.ChannelWebhook:
		if strings.TrimSpace(in.Config) == "" {
			return authz.Errorf(authz.ErrValidation, "config (webhook URL) is required for a webhook channel")
		}
	case alarms.ChannelEmail:
		if strings.TrimSpace(in.Config) == "" {
			return authz.Errorf(authz.ErrValidation, "config (email address) is required for an email-stub channel")
		}
	case alarms.ChannelSMTP:
		if in.SMTP == nil {
			return authz.Errorf(authz.ErrValidation, "smtp config is required for an smtp channel")
		}
		if strings.TrimSpace(in.SMTP.Host) == "" {
			return authz.Errorf(authz.ErrValidation, "smtp host is required")
		}
		if strings.TrimSpace(in.SMTP.From) == "" {
			return authz.Errorf(authz.ErrValidation, "smtp from address is required")
		}
		if strings.TrimSpace(in.SMTP.To) == "" {
			return authz.Errorf(authz.ErrValidation, "smtp to address is required")
		}
	}
	return nil
}

// buildChannelConfig turns an input into the persisted (config string, sealed
// password BLOB). For smtp it serialises the non-secret SMTPConfig as JSON and
// seals the password separately; for other types the password is empty.
func (s *Server) buildChannelConfig(in alarmChannelInput) (cfg string, sealed []byte, err error) {
	if alarms.ChannelType(in.Type) != alarms.ChannelSMTP {
		return strings.TrimSpace(in.Config), nil, nil
	}
	sc := alarms.SMTPConfig{
		Host:     strings.TrimSpace(in.SMTP.Host),
		Port:     in.SMTP.Port,
		Username: strings.TrimSpace(in.SMTP.Username),
		From:     strings.TrimSpace(in.SMTP.From),
		To:       strings.TrimSpace(in.SMTP.To),
		UseTLS:   in.SMTP.UseTLS,
		StartTLS: in.SMTP.StartTLS,
	}
	if sc.Port <= 0 {
		sc.Port = 587
	}
	b, err := json.Marshal(sc)
	if err != nil {
		return "", nil, err
	}
	if pw := in.SMTP.Password; pw != "" {
		sealed, err = authz.SealSecret(s.cfg.SecretKey, []byte(pw))
		if err != nil {
			return "", nil, err
		}
	}
	return string(b), sealed, nil
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
	cfg, sealed, err := s.buildChannelConfig(in)
	if err != nil {
		authz.WriteError(w, r, err)
		return
	}
	rec := &store.AlarmChannel{
		ID: store.NewUUID(), Name: in.Name, Type: in.Type, Config: cfg, ConfigSecret: sealed,
	}
	if err := s.store.CreateAlarmChannel(r.Context(), rec); err != nil {
		authz.WriteError(w, r, err)
		return
	}
	// Never echo the sealed secret back; HasSecret is the safe flag.
	rec.ConfigSecret = nil
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
	adapter := alarmStoreAdapter{st: s.store, secretKey: s.cfg.SecretKey}
	ch := adapter.chToEngine(c)
	n := alarms.NewHTTPNotifier(s.cfg)
	// Real delivery: a webhook POST / SMTP send / logged stub. Surface a CLEAR
	// error to the operator on failure — no false-success (travaux.md §7.4).
	if err := n.SendTest(r.Context(), ch); err != nil {
		authz.WriteError(w, r, authz.Errorf(authz.ErrValidation, fmt.Sprintf("test failed: %v", err)))
		return
	}
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
