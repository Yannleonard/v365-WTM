package alarms

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gtek-it/castor/server/internal/inventory"
	"github.com/gtek-it/castor/server/internal/vprovider"
)

// readings helper: one definition's readings keyed by def id.
func rmap(defID string, rs ...reading) map[string][]reading {
	return map[string][]reading{defID: rs}
}

func numReading(id, name string, v float64) reading {
	return reading{objectID: id, objectName: name, objectType: TargetVM, value: v, hasData: true}
}

// TestStateMachine_BreachDurationRaiseHealClear walks the full lifecycle:
// breach -> (under duration: pending, no raise) -> (past duration: ACTIVE raised)
// -> still breaching: stays active -> heal: CLEARED.
func TestStateMachine_BreachDurationRaiseHealClear(t *testing.T) {
	def := Definition{
		ID: "d1", Name: "CPU>90", Target: TargetVM, Metric: MetricCPU,
		Comparator: CmpGT, Threshold: 90, DurationSec: 300,
		Severity: SeverityCritical, Enabled: true,
	}
	defs := []Definition{def}
	t0 := time.Date(2026, 6, 8, 12, 0, 0, 0, time.UTC)

	// Tick 1: breaching but duration not elapsed -> pending, no raise.
	next, raised, cleared := Evaluate(defs, rmap("d1", numReading("vm1", "web", 95)), nil, t0)
	if len(raised) != 0 || len(cleared) != 0 {
		t.Fatalf("tick1: expected no transitions, got raised=%d cleared=%d", len(raised), len(cleared))
	}
	inst, ok := next["d1:vm1"]
	if !ok || inst.State != StateCleared || inst.breachSince != t0 {
		t.Fatalf("tick1: expected pending instance tracking breachSince=%v, got %+v", t0, inst)
	}

	// Tick 2: still breaching, 4m later (< 5m) -> still pending.
	t1 := t0.Add(4 * time.Minute)
	next, raised, _ = Evaluate(defs, rmap("d1", numReading("vm1", "web", 96)), next, t1)
	if len(raised) != 0 {
		t.Fatalf("tick2: expected no raise before duration, got %d", len(raised))
	}

	// Tick 3: still breaching, now 5m+ since breachSince -> RAISE active.
	t2 := t0.Add(5 * time.Minute)
	next, raised, _ = Evaluate(defs, rmap("d1", numReading("vm1", "web", 97)), next, t2)
	if len(raised) != 1 {
		t.Fatalf("tick3: expected 1 raise, got %d", len(raised))
	}
	if raised[0].State != StateActive || raised[0].Severity != SeverityCritical || raised[0].Value != 97 {
		t.Fatalf("tick3: bad raised instance %+v", raised[0])
	}
	if next["d1:vm1"].RaisedAt != t2 {
		t.Fatalf("tick3: expected RaisedAt=%v, got %v", t2, next["d1:vm1"].RaisedAt)
	}

	// Tick 4: still breaching -> stays active, no new raise.
	t3 := t2.Add(1 * time.Minute)
	next, raised, cleared = Evaluate(defs, rmap("d1", numReading("vm1", "web", 99)), next, t3)
	if len(raised) != 0 || len(cleared) != 0 {
		t.Fatalf("tick4: expected stable active, got raised=%d cleared=%d", len(raised), len(cleared))
	}
	if next["d1:vm1"].State != StateActive {
		t.Fatalf("tick4: expected still active")
	}

	// Tick 5: healed (value below threshold) -> CLEAR, instance dropped.
	t4 := t3.Add(1 * time.Minute)
	next, _, cleared = Evaluate(defs, rmap("d1", numReading("vm1", "web", 10)), next, t4)
	if len(cleared) != 1 {
		t.Fatalf("tick5: expected 1 clear, got %d", len(cleared))
	}
	if cleared[0].State != StateCleared || cleared[0].ClearedAt != t4 {
		t.Fatalf("tick5: bad cleared instance %+v", cleared[0])
	}
	if _, still := next["d1:vm1"]; still {
		t.Fatalf("tick5: cleared instance should be dropped from next set")
	}
}

// TestStateMachine_ImmediateRaiseZeroDuration: durationSec=0 raises on first breach.
func TestStateMachine_ImmediateRaiseZeroDuration(t *testing.T) {
	def := Definition{ID: "d", Name: "any", Target: TargetVM, Metric: MetricCPU,
		Comparator: CmpGT, Threshold: 0, DurationSec: 0, Severity: SeverityInfo, Enabled: true}
	_, raised, _ := Evaluate([]Definition{def}, rmap("d", numReading("vm1", "x", 1)), nil, time.Now())
	if len(raised) != 1 {
		t.Fatalf("expected immediate raise with zero duration, got %d", len(raised))
	}
}

// TestStateMachine_StateMetricEquality raises on state==error and clears otherwise.
func TestStateMachine_StateMetricEquality(t *testing.T) {
	def := Definition{ID: "d", Name: "vm-error", Target: TargetVM, Metric: MetricState,
		StateValue: "error", DurationSec: 0, Severity: SeverityCritical, Enabled: true}
	now := time.Now()
	r := reading{objectID: "vm1", objectName: "bad", objectType: TargetVM, stateRaw: "error", hasData: true}
	next, raised, _ := Evaluate([]Definition{def}, rmap("d", r), nil, now)
	if len(raised) != 1 {
		t.Fatalf("expected raise on state==error, got %d", len(raised))
	}
	r2 := reading{objectID: "vm1", objectName: "bad", objectType: TargetVM, stateRaw: "running", hasData: true}
	_, _, cleared := Evaluate([]Definition{def}, rmap("d", r2), next, now.Add(time.Minute))
	if len(cleared) != 1 {
		t.Fatalf("expected clear when state leaves error, got %d", len(cleared))
	}
}

// TestStateMachine_NoDataNeverRaises: a missing metric sample must not raise.
func TestStateMachine_NoDataNeverRaises(t *testing.T) {
	def := Definition{ID: "d", Name: "cpu", Target: TargetVM, Metric: MetricCPU,
		Comparator: CmpGT, Threshold: 0, DurationSec: 0, Severity: SeverityInfo, Enabled: true}
	r := reading{objectID: "vm1", objectName: "x", objectType: TargetVM, hasData: false}
	_, raised, _ := Evaluate([]Definition{def}, rmap("d", r), nil, time.Now())
	if len(raised) != 0 {
		t.Fatalf("expected no raise without data, got %d", len(raised))
	}
}

// TestStateMachine_DisabledDefinitionIgnored.
func TestStateMachine_DisabledDefinitionIgnored(t *testing.T) {
	def := Definition{ID: "d", Name: "cpu", Target: TargetVM, Metric: MetricCPU,
		Comparator: CmpGT, Threshold: 0, DurationSec: 0, Severity: SeverityInfo, Enabled: false}
	next, raised, _ := Evaluate([]Definition{def}, rmap("d", numReading("vm1", "x", 50)), nil, time.Now())
	if len(raised) != 0 || len(next) != 0 {
		t.Fatalf("disabled definition must be ignored")
	}
}

// TestWebhookPayloadShape verifies the real HTTP POST body shape on a raise.
func TestWebhookPayloadShape(t *testing.T) {
	var got NotifyEvent
	var gotCT string
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotCT = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		close(done)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &HTTPNotifier{Client: srv.Client()}
	ev := NotifyEvent{
		Event: "raised", AlarmID: "d1:vm1", Definition: "CPU>90",
		Severity: SeverityCritical, ObjectID: "vm1", ObjectName: "web", ObjectType: "vm",
		Metric: "cpu", Value: 97, Message: "boom", Timestamp: time.Now().UTC(),
	}
	n.Notify(context.Background(), Channel{ID: "c1", Name: "hook", Type: ChannelWebhook, Config: srv.URL}, ev)

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("webhook was not POSTed")
	}
	if gotCT != "application/json" {
		t.Fatalf("expected application/json content-type, got %q", gotCT)
	}
	if got.Event != "raised" || got.Severity != SeverityCritical || got.ObjectName != "web" || got.Value != 97 {
		t.Fatalf("unexpected webhook payload: %+v", got)
	}
}

// --- engine integration test with fakes ---

type fakeStore struct {
	mu    sync.Mutex
	defs  []Definition
	chans map[string]Channel
	saved []Instance
}

func (f *fakeStore) ListDefinitions(context.Context) ([]Definition, error) { return f.defs, nil }
func (f *fakeStore) ListChannels(context.Context) ([]Channel, error) {
	out := []Channel{}
	for _, c := range f.chans {
		out = append(out, c)
	}
	return out, nil
}
func (f *fakeStore) GetChannel(_ context.Context, id string) (Channel, bool) {
	c, ok := f.chans[id]
	return c, ok
}
func (f *fakeStore) SaveInstances(_ context.Context, insts []Instance) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saved = insts
	return nil
}
func (f *fakeStore) LoadInstances(context.Context) ([]Instance, error) { return nil, nil }

// fakeInv is a mutable InventorySource returning VMs with a chosen state.
type fakeInv struct {
	mu  sync.Mutex
	vms []vprovider.VM
}

func (f *fakeInv) set(vms []vprovider.VM) {
	f.mu.Lock()
	f.vms = vms
	f.mu.Unlock()
}

func (f *fakeInv) All(context.Context, time.Time) inventory.Unified {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := make([]vprovider.VM, len(f.vms))
	copy(cp, f.vms)
	return inventory.Unified{VMs: cp}
}

// TestEngineTick_FiresWebhookOnRaise drives a full engine Tick through fakes and
// asserts the webhook fired with the expected payload, then clears.
func TestEngineTick_FiresWebhookOnRaise(t *testing.T) {
	var mu sync.Mutex
	var events []NotifyEvent
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev NotifyEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		events = append(events, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	fs := &fakeStore{
		chans: map[string]Channel{"c1": {ID: "c1", Name: "hook", Type: ChannelWebhook, Config: srv.URL}},
		defs: []Definition{{
			ID: "d1", Name: "state==error", Target: TargetVM, Metric: MetricState,
			StateValue: "error", DurationSec: 0, Severity: SeverityCritical, Enabled: true,
			NotifyChannelIDs: []string{"c1"},
		}},
	}
	// inventory returning one VM in error state.
	inv := &fakeInv{vms: []vprovider.VM{{ID: "vm1", Name: "bad", State: vprovider.StateError}}}
	e := New(inv, nil, fs, &HTTPNotifier{Client: srv.Client()}, time.Hour)

	e.Tick(context.Background(), time.Now().UTC())
	if got := e.Active(); len(got) != 1 || got[0].ObjectName != "bad" {
		t.Fatalf("expected 1 active alarm, got %+v", got)
	}
	mu.Lock()
	if len(events) != 1 || events[0].Event != "raised" {
		mu.Unlock()
		t.Fatalf("expected 1 raised webhook, got %+v", events)
	}
	mu.Unlock()

	// Heal: VM back to running -> clear + webhook "cleared".
	inv.set([]vprovider.VM{{ID: "vm1", Name: "bad", State: vprovider.StateRunning}})
	e.Tick(context.Background(), time.Now().UTC())
	if got := e.Active(); len(got) != 0 {
		t.Fatalf("expected 0 active after heal, got %d", len(got))
	}
	mu.Lock()
	defer mu.Unlock()
	if len(events) != 2 || events[1].Event != "cleared" {
		t.Fatalf("expected a cleared webhook, got %+v", events)
	}
}
