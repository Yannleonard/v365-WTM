package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gtek-it/castor/server/internal/alarms"
	"github.com/gtek-it/castor/server/internal/store"
)

// TestAlarmDefinitionCRUD exercises create/list/update(enable-disable)/delete over
// the real HTTP surface, gated by alarms.read / alarms.write (admin "*").
func TestAlarmDefinitionCRUD(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminSession(t, e)

	// Create a definition.
	rec := e.do(t, http.MethodPost, "/api/v1/alarms/definitions", map[string]any{
		"name": "cpu-hot", "target": "vm", "metric": "cpu",
		"comparator": "gt", "threshold": 90, "durationSec": 60,
		"severity": "warning", "enabled": true, "notifyChannelIds": []string{},
	}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create def = %d (%s)", rec.Code, rec.Body.String())
	}
	var def store.AlarmDefinition
	if err := json.Unmarshal(rec.Body.Bytes(), &def); err != nil {
		t.Fatalf("decode def: %v", err)
	}
	if def.ID == "" || !def.Enabled || def.Metric != "cpu" {
		t.Fatalf("bad created def: %+v", def)
	}

	// List.
	rec = e.do(t, http.MethodGet, "/api/v1/alarms/definitions", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list def = %d", rec.Code)
	}
	var list []store.AlarmDefinition
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 def, got %d", len(list))
	}

	// Disable via update.
	rec = e.do(t, http.MethodPut, "/api/v1/alarms/definitions/"+def.ID, map[string]any{
		"name": "cpu-hot", "target": "vm", "metric": "cpu",
		"comparator": "gt", "threshold": 90, "durationSec": 60,
		"severity": "warning", "enabled": false, "notifyChannelIds": []string{},
	}, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("update def = %d (%s)", rec.Code, rec.Body.String())
	}
	got, err := e.st.GetAlarmDefinition(context.Background(), def.ID)
	if err != nil || got.Enabled {
		t.Fatalf("expected disabled def, got %+v err=%v", got, err)
	}

	// Delete.
	rec = e.do(t, http.MethodDelete, "/api/v1/alarms/definitions/"+def.ID, nil, cookies, csrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete def = %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAlarmInvalidDefinitionRejected proves validation rejects bad bodies.
func TestAlarmInvalidDefinitionRejected(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminSession(t, e)
	rec := e.do(t, http.MethodPost, "/api/v1/alarms/definitions", map[string]any{
		"name": "bad", "target": "nope", "metric": "cpu",
		"comparator": "gt", "threshold": 1, "severity": "warning", "notifyChannelIds": []string{},
	}, cookies, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid target should be 422, got %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestAlarmFiringFlow is the LIVE-equivalent end-to-end: create a webhook channel,
// create a definition that WILL fire over the sim inventory (VM state == stopped,
// duration 0), tick the engine, and assert GET /alarms/active returns the firing
// alarm AND the webhook received the raised payload.
func TestAlarmFiringFlow(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminSession(t, e)

	// Webhook sink.
	var mu sync.Mutex
	var got []alarms.NotifyEvent
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var ev alarms.NotifyEvent
		_ = json.NewDecoder(r.Body).Decode(&ev)
		mu.Lock()
		got = append(got, ev)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer sink.Close()

	// Create the channel.
	rec := e.do(t, http.MethodPost, "/api/v1/alarms/channels", map[string]any{
		"name": "hook", "type": "webhook", "config": sink.URL,
	}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create channel = %d (%s)", rec.Code, rec.Body.String())
	}
	var ch store.AlarmChannel
	_ = json.Unmarshal(rec.Body.Bytes(), &ch)

	// Test-notification endpoint must succeed (real POST / logged).
	rec = e.do(t, http.MethodPost, "/api/v1/alarms/channels/"+ch.ID+"/test", nil, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("channel test = %d (%s)", rec.Code, rec.Body.String())
	}

	// Definition that fires on a stopped VM (the sim has a stopped VM), duration 0.
	rec = e.do(t, http.MethodPost, "/api/v1/alarms/definitions", map[string]any{
		"name": "vm-stopped", "target": "vm", "metric": "state",
		"stateValue": "stopped", "durationSec": 0, "severity": "critical",
		"enabled": true, "notifyChannelIds": []string{ch.ID},
	}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create firing def = %d (%s)", rec.Code, rec.Body.String())
	}

	// Drive an evaluation tick directly (the engine ticker also runs in prod).
	e.srv.alarmEng.Tick(context.Background(), time.Now().UTC())

	// GET /alarms/active must now return the firing alarm.
	rec = e.do(t, http.MethodGet, "/api/v1/alarms/active", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("active = %d (%s)", rec.Code, rec.Body.String())
	}
	var active []alarms.Instance
	if err := json.Unmarshal(rec.Body.Bytes(), &active); err != nil {
		t.Fatalf("decode active: %v", err)
	}
	if len(active) == 0 {
		t.Fatalf("expected at least one firing alarm, got none")
	}
	found := false
	for _, a := range active {
		if a.DefinitionName == "vm-stopped" && a.State == alarms.StateActive && a.Severity == alarms.SeverityCritical {
			found = true
		}
	}
	if !found {
		t.Fatalf("firing alarm 'vm-stopped' not present: %+v", active)
	}

	// The webhook must have received a "raised" payload (test + raise => >=1).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	var sawRaise bool
	for _, ev := range got {
		if ev.Event == "raised" {
			sawRaise = true
		}
	}
	if !sawRaise {
		t.Fatalf("webhook never received a 'raised' event; got %+v", got)
	}
}
