package alarms

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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

func startFakeSMTPServer(t *testing.T) (host string, port int, emails <-chan string, close func()) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	emailsCh := make(chan string, 1)
	go func() {
		defer ln.Close()
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		w := func(s string) { _, _ = conn.Write([]byte(s)) }
		r := bufio.NewReader(conn)
		w("220 localhost ESMTP\r\n")
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			cmd := strings.TrimSpace(line)
			switch {
			case strings.HasPrefix(cmd, "EHLO"), strings.HasPrefix(cmd, "HELO"):
				w("250-localhost\r\n250 AUTH PLAIN LOGIN\r\n")
			case strings.HasPrefix(cmd, "MAIL FROM:"):
				w("250 OK\r\n")
			case strings.HasPrefix(cmd, "RCPT TO:"):
				w("250 OK\r\n")
			case strings.HasPrefix(cmd, "DATA"):
				w("354 End data with <CR><LF>.<CR><LF>\r\n")
				var email strings.Builder
				for {
					line, err := r.ReadString('\n')
					if err != nil {
						return
					}
					if line == ".\r\n" {
						break
					}
					email.WriteString(line)
				}
				emailsCh <- email.String()
				w("250 OK\r\n")
			case strings.HasPrefix(cmd, "QUIT"):
				w("221 Bye\r\n")
				return
			default:
				w("250 OK\r\n")
			}
		}
	}()
	addr := ln.Addr().String()
	host, portStr, _ := net.SplitHostPort(addr)
	port, _ = strconv.Atoi(portStr)
	return host, port, emailsCh, func() { _ = ln.Close() }
}

// smtpChannelJSON builds a smtp channel whose Config holds the JSON SMTPConfig
// for a (host, port) — mirroring how the API layer persists it.
func smtpChannelJSON(t *testing.T, host string, port int) Channel {
	t.Helper()
	b, err := json.Marshal(SMTPConfig{Host: host, Port: port, From: "noreply@example.com", To: "ops@example.com"})
	if err != nil {
		t.Fatal(err)
	}
	return Channel{ID: "c1", Name: "ops", Type: ChannelSMTP, Config: string(b)}
}

// TestSMTPNotifierSendsEmail proves a REAL SMTP send over a plain connection,
// using per-channel config (host/port/from/to JSON). The fake SMTP server
// captures the wire message; we assert RFC5322 headers + the alarm body.
func TestSMTPNotifierSendsEmail(t *testing.T) {
	host, port, emailsCh, stopSMTP := startFakeSMTPServer(t)
	defer stopSMTP()
	msgCh := make(chan string, 1)
	go func() {
		select {
		case msg := <-emailsCh:
			msgCh <- msg
		case <-time.After(3 * time.Second):
			close(msgCh)
		}
	}()
	n := &HTTPNotifier{Client: http.DefaultClient}
	err := n.Send(context.Background(), smtpChannelJSON(t, host, port), NotifyEvent{
		Event: "raised", AlarmID: "d1:vm1", Definition: "CPU>90", Severity: SeverityCritical,
		ObjectID: "vm1", ObjectName: "web", ObjectType: "vm", Metric: "cpu", Value: 97,
		Message: "boom", Timestamp: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	msg, ok := <-msgCh
	if !ok {
		t.Fatal("SMTP server did not receive a message")
	}
	if !strings.Contains(msg, "Subject: UniHV alarm: CPU>90") {
		t.Fatalf("unexpected email subject payload: %q", msg)
	}
	if !strings.Contains(msg, "boom") {
		t.Fatalf("expected email body to contain the alarm message, got %q", msg)
	}
	if !strings.Contains(msg, "From: noreply@example.com") || !strings.Contains(msg, "To: ops@example.com") {
		t.Fatalf("missing From/To headers: %q", msg)
	}
	if !strings.Contains(msg, "Date: ") {
		t.Fatalf("missing Date header (RFC5322): %q", msg)
	}
}

// TestSMTPSendErrorOnUnreachableHost proves a REAL error is returned (no
// false-success) when the SMTP server is unreachable.
func TestSMTPSendErrorOnUnreachableHost(t *testing.T) {
	n := &HTTPNotifier{Client: http.DefaultClient}
	// 127.0.0.1:1 is reserved/closed — connection must fail fast.
	ch := smtpChannelJSON(t, "127.0.0.1", 1)
	err := n.SendTest(context.Background(), ch)
	if err == nil {
		t.Fatal("expected a clear SMTP error for an unreachable host, got nil")
	}
}

// TestBuildEmailMessageRFC5322 asserts the composed message has the required
// RFC5322 headers in order with CRLF line endings and a blank line before body.
func TestBuildEmailMessageRFC5322(t *testing.T) {
	now := time.Date(2026, 6, 8, 12, 30, 0, 0, time.UTC)
	ev := NotifyEvent{
		Event: "raised", AlarmID: "d1:vm1", Definition: "CPU>90", Severity: SeverityCritical,
		ObjectName: "web", ObjectType: "vm", Metric: "cpu", Value: 97.5,
		Message: "alarm body", Timestamp: now,
	}
	msg := BuildEmailMessage("from@x.test", "a@x.test, b@x.test", "Sub", ev, now)
	for _, h := range []string{
		"From: from@x.test\r\n", "To: a@x.test, b@x.test\r\n", "Subject: Sub\r\n",
		"Date: ", "MIME-Version: 1.0\r\n", "Content-Type: text/plain; charset=utf-8\r\n",
	} {
		if !strings.Contains(msg, h) {
			t.Fatalf("missing header %q in:\n%s", h, msg)
		}
	}
	headerEnd := strings.Index(msg, "\r\n\r\n")
	if headerEnd < 0 {
		t.Fatalf("no header/body separator (blank line) in:\n%s", msg)
	}
	if !strings.Contains(msg[headerEnd:], "alarm body") {
		t.Fatalf("body missing the message text:\n%s", msg)
	}
	if !strings.Contains(msg, "Value: 97.50\r\n") {
		t.Fatalf("expected formatted Value header:\n%s", msg)
	}
}

// TestParseSMTPConfigValidation covers the config validation paths.
func TestParseSMTPConfigValidation(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{"empty", "", true},
		{"bad-json", "{not json", true},
		{"no-host", `{"from":"a@x","to":"b@x"}`, true},
		{"no-from", `{"host":"h","to":"b@x"}`, true},
		{"no-to", `{"host":"h","from":"a@x"}`, true},
		{"ok", `{"host":"h","from":"a@x","to":"b@x"}`, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg, err := ParseSMTPConfig(c.raw)
			if c.wantErr && err == nil {
				t.Fatalf("expected error for %q", c.raw)
			}
			if !c.wantErr {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if cfg.Port != 587 {
					t.Fatalf("expected default port 587, got %d", cfg.Port)
				}
			}
		})
	}
}

// TestStubVsSMTPDispatch proves the explicit stub mode logs (never errors, never
// sends) while smtp performs a real send — the two email options are distinct.
func TestStubVsSMTPDispatch(t *testing.T) {
	n := &HTTPNotifier{Client: http.DefaultClient}
	// email-stub: always succeeds, no network, even with a nonsense config.
	if err := n.Send(context.Background(), Channel{Name: "ci", Type: ChannelEmail, Config: "ops@x.test"}, NotifyEvent{
		Event: "test", Message: "stub", Timestamp: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("email-stub must never error, got %v", err)
	}
	// smtp with empty config: must error (no false-success).
	if err := n.Send(context.Background(), Channel{Name: "real", Type: ChannelSMTP, Config: ""}, NotifyEvent{
		Event: "test", Message: "x", Timestamp: time.Now().UTC(),
	}); err == nil {
		t.Fatal("smtp with empty config must error")
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
