// Package alarms is UniHV's vSphere-style ALARM engine: threshold-driven,
// STATEFUL rules over the unified inventory + metrics. Where the insights package
// emits stateless best-practice findings on every scan, alarms are user-defined
// rules ("VM CPU > 90% for 5m", "datastore > 90% full", "VM state == error") that
// RAISE a stateful instance with a severity when an object breaches past a
// duration, keep it ACTIVE while it keeps breaching, and CLEAR it when the object
// returns to health — exactly like vCenter alarms across every hypervisor.
//
// The evaluator runs on a ticker. Each tick it reads the unified inventory and
// (for metric rules) the per-VM/host metric series, evaluates every enabled
// definition against every matching object, and drives a per-(definition,object)
// state machine:
//
//	healthy --breach--> pending(breachSince=t0) --t-t0>=duration--> ACTIVE (raise + notify)
//	ACTIVE  --heal-->    CLEARED (clear + notify)   ACTIVE --breach--> stays ACTIVE
//
// On raise/clear it fires the definition's notification channels: a real HTTP POST
// for webhooks, a log line for the email stub. The pure evaluation core is a
// function of (inventory, metrics, defs, now, prior-state) so the state machine is
// deterministic and unit-testable with crafted fixtures.
package alarms

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/smtp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gtek-it/castor/server/internal/config"
	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// Severity is the alarm severity (UI badge + ordering: critical first).
type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityWarning  Severity = "warning"
	SeverityCritical Severity = "critical"
)

func (s Severity) Valid() bool {
	switch s {
	case SeverityInfo, SeverityWarning, SeverityCritical:
		return true
	}
	return false
}

// TargetType is the kind of object a definition watches.
type TargetType string

const (
	TargetVM        TargetType = "vm"
	TargetHost      TargetType = "host"
	TargetDatastore TargetType = "datastore"
)

func (t TargetType) Valid() bool {
	switch t {
	case TargetVM, TargetHost, TargetDatastore:
		return true
	}
	return false
}

// Metric is the measured quantity. cpu/memory are percent (0..100); storage_pct
// is used-percent of a datastore; disk is per-VM disk I/O bytes/s from the metric
// series; state is the object lifecycle state compared as a string (use comparator
// eq with threshold encoded via StateValue).
type Metric string

const (
	MetricCPU        Metric = "cpu"         // CPU percent (vm/host)
	MetricMemory     Metric = "memory"      // memory used percent (vm/host)
	MetricDisk       Metric = "disk"        // disk I/O bytes/sec (vm)
	MetricStoragePct Metric = "storage_pct" // datastore used percent
	MetricState      Metric = "state"       // lifecycle state (eq comparison)
)

func (m Metric) Valid() bool {
	switch m {
	case MetricCPU, MetricMemory, MetricDisk, MetricStoragePct, MetricState:
		return true
	}
	return false
}

// Comparator is how the measured value is compared to the threshold.
type Comparator string

const (
	CmpGT Comparator = "gt"
	CmpLT Comparator = "lt"
	CmpEQ Comparator = "eq"
)

func (c Comparator) Valid() bool {
	switch c {
	case CmpGT, CmpLT, CmpEQ:
		return true
	}
	return false
}

// ChannelType is a notification transport.
type ChannelType string

const (
	ChannelWebhook ChannelType = "webhook"    // real HTTP POST of the payload JSON
	ChannelEmail   ChannelType = "email-stub" // logged stub (CI/offline, no SMTP)
	ChannelSMTP    ChannelType = "smtp"       // real SMTP delivery via net/smtp
)

func (c ChannelType) Valid() bool {
	switch c {
	case ChannelWebhook, ChannelEmail, ChannelSMTP:
		return true
	}
	return false
}

// Definition is a user-defined alarm rule. Threshold is the numeric bound for
// numeric metrics; StateValue is the compared state for the state metric.
type Definition struct {
	ID               string     `json:"id"`
	Name             string     `json:"name"`
	Target           TargetType `json:"target"`
	Metric           Metric     `json:"metric"`
	Comparator       Comparator `json:"comparator"`
	Threshold        float64    `json:"threshold"`
	StateValue       string     `json:"stateValue,omitempty"` // for metric=state (e.g. "error")
	DurationSec      int        `json:"durationSec"`
	Severity         Severity   `json:"severity"`
	Enabled          bool       `json:"enabled"`
	NotifyChannelIDs []string   `json:"notifyChannelIds,omitempty"`
}

// Channel is a notification destination.
//
// For webhook channels Config is the target URL; for email-stub it is the
// recipient address (logged only). For smtp channels Config is a JSON-encoded
// SMTPConfig (host/port/username/from/to/tls — NEVER the password) and Secret
// carries the SMTP password in plaintext, opened from the sealed store BLOB by
// the API layer at dispatch/test time. Secret is intentionally json:"-" so it is
// never serialised back to a client.
type Channel struct {
	ID     string      `json:"id"`
	Name   string      `json:"name"`
	Type   ChannelType `json:"type"`
	Config string      `json:"config"` // webhook URL, stub address, or smtp JSON config
	Secret string      `json:"-"`      // smtp password (opened from sealed store BLOB)
}

// SMTPConfig is the non-secret SMTP channel configuration carried in Channel.Config
// as JSON. The password is NEVER part of this struct — it is sealed at rest and
// passed separately via Channel.Secret.
type SMTPConfig struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username,omitempty"`
	From     string `json:"from"`
	To       string `json:"to"`                 // comma-separated recipients
	UseTLS   bool   `json:"useTLS,omitempty"`   // implicit TLS (SMTPS, typically port 465)
	StartTLS bool   `json:"startTLS,omitempty"` // STARTTLS upgrade (typically port 587)
}

// ParseSMTPConfig decodes a smtp channel's Config JSON and validates the required
// fields. Returns a clear error so the /test endpoint can surface a bad config.
func ParseSMTPConfig(raw string) (SMTPConfig, error) {
	var c SMTPConfig
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return c, fmt.Errorf("smtp channel config is empty")
	}
	if err := json.Unmarshal([]byte(raw), &c); err != nil {
		return c, fmt.Errorf("smtp channel config is not valid JSON: %w", err)
	}
	if strings.TrimSpace(c.Host) == "" {
		return c, fmt.Errorf("smtp host is required")
	}
	if strings.TrimSpace(c.From) == "" {
		return c, fmt.Errorf("smtp from address is required")
	}
	if strings.TrimSpace(c.To) == "" {
		return c, fmt.Errorf("smtp to address is required")
	}
	if c.Port <= 0 {
		c.Port = 587
	}
	return c, nil
}

// InstanceState is the lifecycle of an alarm instance.
type InstanceState string

const (
	StateActive  InstanceState = "active"
	StateCleared InstanceState = "cleared"
)

// Instance is a stateful alarm instance for one (definition, object) pair. The
// engine keeps active instances in memory (and persists them via the StateStore so
// they survive restart). breachSince tracks the pending window before a raise.
type Instance struct {
	ID             string        `json:"id"` // stable: definitionId + ":" + objectId
	DefinitionID   string        `json:"definitionId"`
	DefinitionName string        `json:"definitionName"`
	ObjectID       string        `json:"objectId"`
	ObjectName     string        `json:"objectName"`
	ObjectType     TargetType    `json:"objectType"`
	Severity       Severity      `json:"severity"`
	Metric         Metric        `json:"metric"`
	State          InstanceState `json:"state"`
	Value          float64       `json:"value"`              // last measured value (numeric metrics)
	StateRaw       string        `json:"stateRaw,omitempty"` // last measured state (state metric)
	RaisedAt       time.Time     `json:"raisedAt"`
	ClearedAt      time.Time     `json:"clearedAt,omitempty"`
	LastNotifiedAt time.Time     `json:"lastNotifiedAt,omitempty"`

	// breachSince is the first tick this object was found breaching; used to apply
	// the duration before promoting pending -> active. Zero when not breaching.
	breachSince time.Time
}

// MetricsSource resolves a metric series for a VM/host entity (satisfied by the
// vprovider registry via a thin adapter in the api layer). The evaluator pulls the
// LAST sample of the series for the breach test. Returning (nil, err) is treated as
// "no data" — a missing sample never raises a metric alarm.
type MetricsSource interface {
	LatestSample(ctx context.Context, providerID, entityID string) (*vprovider.MetricSample, bool)
}

// StateStore persists definitions, channels and active instances so the engine
// resumes after a restart. All methods are best-effort from the engine's side.
type StateStore interface {
	ListDefinitions(ctx context.Context) ([]Definition, error)
	ListChannels(ctx context.Context) ([]Channel, error)
	GetChannel(ctx context.Context, id string) (Channel, bool)
	SaveInstances(ctx context.Context, insts []Instance) error
	LoadInstances(ctx context.Context) ([]Instance, error)
}

// InventorySource returns the current unified inventory snapshot.
type InventorySource interface {
	All(ctx context.Context, now time.Time) inventory.Unified
}

// Notifier sends one notification for an instance transition.
type Notifier interface {
	Notify(ctx context.Context, ch Channel, ev NotifyEvent)
}

// NotifyEvent is the JSON payload POSTed to a webhook (and logged for email-stub).
// Stable shape: this is the public contract for downstream integrations.
type NotifyEvent struct {
	Event      string    `json:"event"` // "raised" | "cleared"
	AlarmID    string    `json:"alarmId"`
	Definition string    `json:"definition"`
	Severity   Severity  `json:"severity"`
	ObjectID   string    `json:"objectId"`
	ObjectName string    `json:"objectName"`
	ObjectType string    `json:"objectType"`
	Metric     string    `json:"metric"`
	Value      float64   `json:"value"`
	StateRaw   string    `json:"stateRaw,omitempty"`
	Message    string    `json:"message"`
	Timestamp  time.Time `json:"timestamp"`
}

// reading is the value of one object for one metric on a tick.
type reading struct {
	objectID   string
	objectName string
	objectType TargetType
	value      float64 // numeric metrics
	stateRaw   string  // state metric
	hasData    bool    // false => skip (no metric sample); never raises
}

// breaches reports whether a reading violates the definition's comparator/threshold.
func (d Definition) breaches(r reading) bool {
	if !r.hasData {
		return false
	}
	if d.Metric == MetricState {
		// State metric only supports equality against StateValue.
		return strings.EqualFold(r.stateRaw, d.StateValue)
	}
	switch d.Comparator {
	case CmpGT:
		return r.value > d.Threshold
	case CmpLT:
		return r.value < d.Threshold
	case CmpEQ:
		return r.value == d.Threshold
	}
	return false
}

// Evaluate is the PURE state-machine core: given the current readings per
// definition, the prior instances and now, it returns the next instance set plus
// the transitions (raised/cleared) to notify. Deterministic and unit-testable.
//
// readings maps definitionID -> []reading (already filtered to matching objects).
func Evaluate(defs []Definition, readings map[string][]reading, prior map[string]Instance, now time.Time) (next map[string]Instance, raised, cleared []Instance) {
	next = make(map[string]Instance)
	enabled := make(map[string]Definition, len(defs))
	for _, d := range defs {
		if d.Enabled {
			enabled[d.ID] = d
		}
	}

	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		for _, r := range readings[d.ID] {
			key := d.ID + ":" + r.objectID
			cur, had := prior[key]
			breaching := d.breaches(r)

			inst := Instance{
				ID:             key,
				DefinitionID:   d.ID,
				DefinitionName: d.Name,
				ObjectID:       r.objectID,
				ObjectName:     r.objectName,
				ObjectType:     r.objectType,
				Severity:       d.Severity,
				Metric:         d.Metric,
				Value:          r.value,
				StateRaw:       r.stateRaw,
			}
			if had {
				inst.RaisedAt = cur.RaisedAt
				inst.LastNotifiedAt = cur.LastNotifiedAt
				inst.breachSince = cur.breachSince
			}

			switch {
			case breaching && (!had || cur.State == StateCleared):
				// Not currently active. Track the breach window.
				if inst.breachSince.IsZero() {
					inst.breachSince = now
				}
				if now.Sub(inst.breachSince) >= time.Duration(d.DurationSec)*time.Second {
					inst.State = StateActive
					inst.RaisedAt = now
					inst.LastNotifiedAt = now
					raised = append(raised, inst)
				} else {
					// Pending: keep tracking but not yet a visible active alarm.
					inst.State = StateCleared
				}
				next[key] = inst

			case breaching && had && cur.State == StateActive:
				// Still breaching: stays active, refresh value.
				inst.State = StateActive
				next[key] = inst

			case !breaching && had && cur.State == StateActive:
				// Healed: clear it (and notify once).
				inst.State = StateCleared
				inst.ClearedAt = now
				inst.LastNotifiedAt = now
				inst.breachSince = time.Time{}
				cleared = append(cleared, inst)
				// Do not carry a cleared instance forward.

			default:
				// Healthy and not active: drop (no instance retained).
			}
		}
	}
	return next, raised, cleared
}

// Engine drives the ticker, holds the live instance map, and fires notifications.
type Engine struct {
	inv      InventorySource
	metrics  MetricsSource
	store    StateStore
	notifier Notifier
	interval time.Duration

	mu        sync.RWMutex
	instances map[string]Instance

	stopCh chan struct{}
	doneCh chan struct{}
	once   sync.Once
}

// New builds the engine. interval<=0 defaults to 30s.
func New(inv InventorySource, metrics MetricsSource, st StateStore, notifier Notifier, interval time.Duration) *Engine {
	if interval <= 0 {
		interval = 30 * time.Second
	}
	if notifier == nil {
		notifier = &HTTPNotifier{Client: http.DefaultClient}
	}
	return &Engine{
		inv:       inv,
		metrics:   metrics,
		store:     st,
		notifier:  notifier,
		interval:  interval,
		instances: make(map[string]Instance),
	}
}

// Resume loads persisted active instances at startup so an in-flight alarm is not
// lost across restarts. Best-effort.
func (e *Engine) Resume(ctx context.Context) {
	if e.store == nil {
		return
	}
	insts, err := e.store.LoadInstances(ctx)
	if err != nil {
		return
	}
	e.mu.Lock()
	for _, in := range insts {
		e.instances[in.ID] = in
	}
	e.mu.Unlock()
}

// Start launches the evaluation ticker. Idempotent.
func (e *Engine) Start(ctx context.Context) {
	e.once.Do(func() {
		e.stopCh = make(chan struct{})
		e.doneCh = make(chan struct{})
		go e.loop(ctx)
	})
}

// Stop halts the ticker and waits for the loop to exit.
func (e *Engine) Stop() {
	if e.stopCh == nil {
		return
	}
	close(e.stopCh)
	<-e.doneCh
}

func (e *Engine) loop(ctx context.Context) {
	defer close(e.doneCh)
	t := time.NewTicker(e.interval)
	defer t.Stop()
	// Evaluate once immediately so a just-created firing alarm shows up fast.
	e.Tick(ctx, time.Now().UTC())
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.Tick(ctx, time.Now().UTC())
		}
	}
}

// Tick performs one evaluation pass: read inventory + metrics, run the pure state
// machine, persist + notify transitions. Exported so it can be driven from tests.
func (e *Engine) Tick(ctx context.Context, now time.Time) {
	if e.store == nil || e.inv == nil {
		return
	}
	defs, err := e.store.ListDefinitions(ctx)
	if err != nil {
		return
	}
	u := e.inv.All(ctx, now)
	readings := e.collect(ctx, defs, u)

	e.mu.Lock()
	prior := make(map[string]Instance, len(e.instances))
	for k, v := range e.instances {
		prior[k] = v
	}
	next, raised, cleared := Evaluate(defs, readings, prior, now)
	e.instances = next
	snapshot := make([]Instance, 0, len(next))
	for _, in := range next {
		snapshot = append(snapshot, in)
	}
	e.mu.Unlock()

	// Persist the live set (best-effort).
	_ = e.store.SaveInstances(ctx, activeOnly(snapshot))

	// Fire notifications for transitions.
	for _, in := range raised {
		e.fire(ctx, in, "raised")
	}
	for _, in := range cleared {
		e.fire(ctx, in, "cleared")
	}
}

func activeOnly(in []Instance) []Instance {
	out := make([]Instance, 0, len(in))
	for _, x := range in {
		if x.State == StateActive {
			out = append(out, x)
		}
	}
	return out
}

// collect builds the per-definition readings over the inventory + metrics.
func (e *Engine) collect(ctx context.Context, defs []Definition, u inventory.Unified) map[string][]reading {
	out := make(map[string][]reading)
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		switch d.Target {
		case TargetVM:
			for _, vm := range u.VMs {
				out[d.ID] = append(out[d.ID], e.vmReading(ctx, d, vm))
			}
		case TargetHost:
			for _, h := range u.Hosts {
				out[d.ID] = append(out[d.ID], e.hostReading(ctx, d, h))
			}
		case TargetDatastore:
			for _, sp := range u.Storage {
				out[d.ID] = append(out[d.ID], dsReading(d, sp))
			}
		}
	}
	return out
}

func (e *Engine) vmReading(ctx context.Context, d Definition, vm vprovider.VM) reading {
	r := reading{objectID: vm.ID, objectName: vm.Name, objectType: TargetVM}
	if d.Metric == MetricState {
		r.stateRaw = string(vm.State)
		r.hasData = true
		return r
	}
	if e.metrics == nil {
		return r
	}
	s, ok := e.metrics.LatestSample(ctx, vm.ProviderID, vm.ID)
	if !ok || s == nil {
		return r
	}
	r.value, r.hasData = metricValue(d.Metric, s), true
	return r
}

func (e *Engine) hostReading(ctx context.Context, d Definition, h vprovider.Host) reading {
	r := reading{objectID: h.ID, objectName: h.Name, objectType: TargetHost}
	if d.Metric == MetricState {
		r.stateRaw = string(h.State)
		r.hasData = true
		return r
	}
	// Host memory % can be derived directly from the inventory row.
	if d.Metric == MetricMemory && h.MemoryMB > 0 {
		r.value = float64(h.MemUsedMB) / float64(h.MemoryMB) * 100
		r.hasData = true
		return r
	}
	if e.metrics == nil {
		return r
	}
	s, ok := e.metrics.LatestSample(ctx, h.ProviderID, h.ID)
	if !ok || s == nil {
		return r
	}
	r.value, r.hasData = metricValue(d.Metric, s), true
	return r
}

func dsReading(d Definition, sp vprovider.StoragePool) reading {
	r := reading{objectID: sp.ID, objectName: sp.Name, objectType: TargetDatastore}
	if d.Metric == MetricStoragePct && sp.CapacityGB > 0 {
		used := sp.CapacityGB - sp.FreeGB
		if used < 0 {
			used = 0
		}
		r.value = used / sp.CapacityGB * 100
		r.hasData = true
	}
	return r
}

// metricValue projects a metric sample onto the requested metric (percent for
// cpu/memory; bytes/s proxy for disk).
func metricValue(m Metric, s *vprovider.MetricSample) float64 {
	switch m {
	case MetricCPU:
		return s.CPUPercent
	case MetricMemory:
		if s.MemLimitBytes > 0 {
			return float64(s.MemUsageBytes) / float64(s.MemLimitBytes) * 100
		}
		return 0
	case MetricDisk:
		return float64(s.DiskReadBytes + s.DiskWriteBytes)
	}
	return 0
}

// fire dispatches a transition to every configured channel.
func (e *Engine) fire(ctx context.Context, in Instance, event string) {
	ev := NotifyEvent{
		Event:      event,
		AlarmID:    in.ID,
		Definition: in.DefinitionName,
		Severity:   in.Severity,
		ObjectID:   in.ObjectID,
		ObjectName: in.ObjectName,
		ObjectType: string(in.ObjectType),
		Metric:     string(in.Metric),
		Value:      in.Value,
		StateRaw:   in.StateRaw,
		Message:    buildMessage(in, event),
		Timestamp:  time.Now().UTC(),
	}
	defs, _ := e.store.ListDefinitions(ctx)
	var chIDs []string
	for _, d := range defs {
		if d.ID == in.DefinitionID {
			chIDs = d.NotifyChannelIDs
			break
		}
	}
	for _, id := range chIDs {
		ch, ok := e.store.GetChannel(ctx, id)
		if !ok {
			continue
		}
		e.notifier.Notify(ctx, ch, ev)
	}
}

// buildMessage renders a human-friendly notification message.
func buildMessage(in Instance, event string) string {
	verb := "RAISED"
	if event == "cleared" {
		verb = "CLEARED"
	}
	var b strings.Builder
	b.WriteString("[")
	b.WriteString(strings.ToUpper(string(in.Severity)))
	b.WriteString("] alarm ")
	b.WriteString(verb)
	b.WriteString(": ")
	b.WriteString(in.DefinitionName)
	b.WriteString(" on ")
	b.WriteString(string(in.ObjectType))
	b.WriteString(" ")
	b.WriteString(in.ObjectName)
	if in.Metric == MetricState {
		b.WriteString(" (state=")
		b.WriteString(in.StateRaw)
		b.WriteString(")")
	}
	return b.String()
}

// Active returns the current ACTIVE alarm instances, severity-first then name.
func (e *Engine) Active() []Instance {
	e.mu.RLock()
	defer e.mu.RUnlock()
	out := make([]Instance, 0, len(e.instances))
	for _, in := range e.instances {
		if in.State == StateActive {
			out = append(out, in)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if sevRank(out[i].Severity) != sevRank(out[j].Severity) {
			return sevRank(out[i].Severity) < sevRank(out[j].Severity)
		}
		if out[i].ObjectName != out[j].ObjectName {
			return out[i].ObjectName < out[j].ObjectName
		}
		return out[i].ID < out[j].ID
	})
	return out
}

func sevRank(s Severity) int {
	switch s {
	case SeverityCritical:
		return 0
	case SeverityWarning:
		return 1
	default:
		return 2
	}
}

// HTTPNotifier is the real notifier: POSTs the JSON payload to webhook channels,
// sends REAL email via net/smtp for smtp channels, and logs email-stub channels
// (the explicit CI/offline mode — no SMTP dependency). Pure stdlib, CGO-free.
type HTTPNotifier struct {
	Client *http.Client
	// now is overridable in tests for a deterministic Date header.
	now func() time.Time
}

// Notify implements Notifier: fire-and-forget dispatch for the engine's raise/
// clear transitions. Errors are logged (never the password); use Send for the
// error-returning path consumed by the channel "test" endpoint.
func (n *HTTPNotifier) Notify(ctx context.Context, ch Channel, ev NotifyEvent) {
	if err := n.Send(ctx, ch, ev); err != nil {
		log.Printf("alarms: channel %q (%s) delivery failed: %v", ch.Name, ch.Type, err)
	}
}

// Send delivers one event to one channel and RETURNS a clear error on failure
// (no mock/false-success — a real send or a real error, per travaux.md §7.4).
// The smtp password is never included in any returned/logged error.
func (n *HTTPNotifier) Send(ctx context.Context, ch Channel, ev NotifyEvent) error {
	switch ch.Type {
	case ChannelWebhook:
		return n.postWebhook(ctx, ch, ev)
	case ChannelEmail:
		// Explicit CI/offline stub: log only, always succeeds.
		log.Printf("alarms: [email-stub %s -> %s] %s", ch.Name, ch.Config, ev.Message)
		return nil
	case ChannelSMTP:
		return n.sendSMTP(ctx, ch, ev)
	default:
		return fmt.Errorf("unknown channel type %q", ch.Type)
	}
}

func (n *HTTPNotifier) nowUTC() time.Time {
	if n.now != nil {
		return n.now()
	}
	return time.Now().UTC()
}

// sendSMTP delivers a real email over SMTP using only the Go stdlib. It supports
// implicit TLS (UseTLS), STARTTLS upgrade (StartTLS) and plain (e.g. a local
// MailHog test server). PLAIN auth is used only when a username+password are set.
func (n *HTTPNotifier) sendSMTP(ctx context.Context, ch Channel, ev NotifyEvent) error {
	cfg, err := ParseSMTPConfig(ch.Config)
	if err != nil {
		return fmt.Errorf("smtp channel %q: %w", ch.Name, err)
	}
	recipients := splitAddrs(cfg.To)
	if len(recipients) == 0 {
		return fmt.Errorf("smtp channel %q: no recipients", ch.Name)
	}
	subject := "UniHV alarm: " + ev.Definition
	if ev.Event == "test" {
		subject = "UniHV alarms test: " + ch.Name
	}
	msg := BuildEmailMessage(cfg.From, cfg.To, subject, ev, n.nowUTC())
	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)

	var auth smtp.Auth
	if cfg.Username != "" && ch.Secret != "" {
		auth = smtp.PlainAuth("", cfg.Username, ch.Secret, cfg.Host)
	}

	if cfg.UseTLS {
		if err := sendMailTLS(addr, cfg.Host, auth, cfg.From, recipients, []byte(msg)); err != nil {
			return fmt.Errorf("smtp channel %q implicit-TLS send to %s failed: %w", ch.Name, addr, err)
		}
		return nil
	}
	if cfg.StartTLS {
		if err := sendMailStartTLS(addr, cfg.Host, auth, cfg.From, recipients, []byte(msg)); err != nil {
			return fmt.Errorf("smtp channel %q STARTTLS send to %s failed: %w", ch.Name, addr, err)
		}
		return nil
	}
	// Plain SMTP (no TLS) — e.g. a local MailHog/relay. smtp.SendMail still
	// performs STARTTLS automatically if the server advertises it.
	if err := smtp.SendMail(addr, auth, cfg.From, recipients, []byte(msg)); err != nil {
		return fmt.Errorf("smtp channel %q send to %s failed: %w", ch.Name, addr, err)
	}
	return nil
}

// sendMailStartTLS opens a plain connection, issues STARTTLS, then authenticates
// and sends. Mirrors smtp.SendMail but forces the STARTTLS upgrade.
func sendMailStartTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	c, err := smtp.Dial(addr)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("unihv"); err != nil {
		return err
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		return fmt.Errorf("server does not advertise STARTTLS")
	}
	if err := c.StartTLS(&tls.Config{ServerName: host}); err != nil {
		return err
	}
	return finishSMTP(c, auth, from, to, msg)
}

// sendMailTLS opens an implicit-TLS (SMTPS) connection, then authenticates + sends.
func sendMailTLS(addr, host string, auth smtp.Auth, from string, to []string, msg []byte) error {
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: host})
	if err != nil {
		return err
	}
	c, err := smtp.NewClient(conn, host)
	if err != nil {
		_ = conn.Close()
		return err
	}
	defer func() { _ = c.Close() }()
	if err := c.Hello("unihv"); err != nil {
		return err
	}
	return finishSMTP(c, auth, from, to, msg)
}

// finishSMTP runs AUTH (if any) + MAIL/RCPT/DATA on an established client.
func finishSMTP(c *smtp.Client, auth smtp.Auth, from string, to []string, msg []byte) error {
	if auth != nil {
		if ok, _ := c.Extension("AUTH"); ok {
			if err := c.Auth(auth); err != nil {
				return err
			}
		}
	}
	if err := c.Mail(from); err != nil {
		return err
	}
	for _, rcpt := range to {
		if err := c.Rcpt(rcpt); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(msg); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

func splitAddrs(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if t := strings.TrimSpace(p); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// BuildEmailMessage composes a proper RFC5322 message (From/To/Subject/Date/
// MIME headers + plain-text body) for an alarm raise/clear/test notification.
func BuildEmailMessage(from, to, subject string, ev NotifyEvent, now time.Time) string {
	var b strings.Builder
	b.WriteString("From: " + from + "\r\n")
	b.WriteString("To: " + to + "\r\n")
	b.WriteString("Subject: " + subject + "\r\n")
	b.WriteString("Date: " + now.Format(time.RFC1123Z) + "\r\n")
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\n")
	b.WriteString("\r\n")
	b.WriteString(ev.Message)
	b.WriteString("\r\n\r\n")
	b.WriteString("Event: " + ev.Event + "\r\n")
	if ev.AlarmID != "" {
		b.WriteString("Alarm ID: " + ev.AlarmID + "\r\n")
	}
	b.WriteString("Definition: " + ev.Definition + "\r\n")
	b.WriteString("Object: " + ev.ObjectType + " " + ev.ObjectName + "\r\n")
	b.WriteString("Severity: " + string(ev.Severity) + "\r\n")
	if ev.Metric != "" {
		b.WriteString("Metric: " + ev.Metric + "\r\n")
		b.WriteString(fmt.Sprintf("Value: %.2f\r\n", ev.Value))
	}
	if ev.StateRaw != "" {
		b.WriteString("State: " + ev.StateRaw + "\r\n")
	}
	b.WriteString("Timestamp: " + ev.Timestamp.Format(time.RFC3339) + "\r\n")
	return b.String()
}

// NewHTTPNotifier builds the real notifier. cfg is accepted for signature
// compatibility with the server wiring; SMTP settings are now PER-CHANNEL (the
// channel's Config + sealed Secret), so no global SMTP config is consulted.
func NewHTTPNotifier(cfg *config.Config) *HTTPNotifier {
	return &HTTPNotifier{Client: http.DefaultClient}
}

func (n *HTTPNotifier) postWebhook(ctx context.Context, ch Channel, ev NotifyEvent) error {
	url := strings.TrimSpace(ch.Config)
	if url == "" {
		return fmt.Errorf("webhook channel %q has no URL configured", ch.Name)
	}
	body, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("webhook %q build error: %w", ch.Name, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "UniHV-Alarms/1")
	client := n.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("webhook %q POST error: %w", ch.Name, err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook %q returned HTTP %d", ch.Name, resp.StatusCode)
	}
	return nil
}

// SendTest fires a synthetic test event to a single channel (used by the channel
// "test" API). It RETURNS the delivery error (real webhook POST / real SMTP send /
// logged stub) so the endpoint surfaces a clear success or failure — no
// false-success (travaux.md §7.4).
func (n *HTTPNotifier) SendTest(ctx context.Context, ch Channel) error {
	return n.Send(ctx, ch, NotifyEvent{
		Event:      "test",
		Definition: "Test notification",
		Severity:   SeverityInfo,
		ObjectType: "system",
		ObjectName: "unihv",
		Message:    "UniHV alarms test notification for channel " + ch.Name,
		Timestamp:  n.nowUTC(),
	})
}
