package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// doBearer issues a request authenticated by an Authorization: Bearer header
// (NO session cookie), exercising the bearer-or-session middleware path.
func (e *testEnv) doBearer(t *testing.T, method, path string, body any, bearer string) *httptest.ResponseRecorder {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://example.test")
	req.Host = "example.test"
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	e.mux.ServeHTTP(rec, req)
	return rec
}

// createToken creates a scoped API token via the API and returns its raw value.
func createToken(t *testing.T, e *testEnv, cookies []*http.Cookie, csrf string, scopes []string) string {
	t.Helper()
	rec := e.do(t, http.MethodPost, "/api/v1/tokens", map[string]any{
		"name": "ci", "scopes": scopes,
	}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("token create = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	raw, _ := body["token"].(string)
	if raw == "" {
		t.Fatalf("create did not return a raw token: %s", rec.Body.String())
	}
	return raw
}

func TestAPITokenCreateListRevokeAndBearerAuth(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)

	raw := createToken(t, e, cookies, csrf, []string{"vm.read", "vm.power"})

	// The raw token authenticates WITHOUT a session cookie (bearer path).
	rec := e.doBearer(t, http.MethodGet, "/api/v1/vm/providers", nil, raw)
	if rec.Code != 200 {
		t.Fatalf("bearer /vm/providers = %d %s", rec.Code, rec.Body.String())
	}

	// List never returns the raw value.
	rec = e.do(t, http.MethodGet, "/api/v1/tokens", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list tokens = %d %s", rec.Code, rec.Body.String())
	}
	if bytes.Contains(rec.Body.Bytes(), []byte(raw)) {
		t.Fatalf("token list leaked the raw token")
	}
	var list []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Fatalf("expected 1 token, got %d", len(list))
	}
	id, _ := list[0]["id"].(string)

	// A garbage bearer is rejected (401).
	rec = e.doBearer(t, http.MethodGet, "/api/v1/vm/providers", nil, "uhv_not-a-real-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("garbage bearer = %d, want 401", rec.Code)
	}

	// Revoke, then the bearer no longer authenticates.
	rec = e.do(t, http.MethodDelete, "/api/v1/tokens/"+id, nil, cookies, csrf)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.doBearer(t, http.MethodGet, "/api/v1/vm/providers", nil, raw)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked bearer = %d, want 401", rec.Code)
	}
}

func TestAPITokenScopeSubsetEnforced(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)

	// A token scoped to vm.read only must NOT be able to run a bulk power op
	// (which needs vm.power) — the scoped permission set excludes it.
	raw := createToken(t, e, cookies, csrf, []string{"vm.read"})

	vms := listSimVMIDs(t, e, cookies)
	if len(vms) == 0 {
		t.Skip("no sim VMs")
	}
	rec := e.doBearer(t, http.MethodPost, "/api/v1/vm/bulk", map[string]any{
		"action": "power", "op": "start",
		"targets": []map[string]string{{"providerId": "test-kvm", "vmId": vms[0]}},
	}, raw)
	if rec.Code != 200 {
		t.Fatalf("bulk = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	// All targets should be DENIED (per-target forbidden), 0 succeeded.
	if int(body["succeeded"].(float64)) != 0 {
		t.Fatalf("vm.read-only token should not power VMs: %s", rec.Body.String())
	}
}

// listSimVMIDs returns the sim provider's VM ids via the API.
func listSimVMIDs(t *testing.T, e *testEnv, cookies []*http.Cookie) []string {
	t.Helper()
	rec := e.do(t, http.MethodGet, "/api/v1/vm/providers/test-kvm/vms", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list vms = %d %s", rec.Code, rec.Body.String())
	}
	var vms []map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &vms)
	out := make([]string, 0, len(vms))
	for _, v := range vms {
		if id, ok := v["id"].(string); ok {
			out = append(out, id)
		}
	}
	return out
}

func TestBulkPowerFanOut(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)

	vms := listSimVMIDs(t, e, cookies)
	if len(vms) < 2 {
		t.Skip("need >=2 sim VMs")
	}
	targets := []map[string]string{
		{"providerId": "test-kvm", "vmId": vms[0]},
		{"providerId": "test-kvm", "vmId": vms[1]},
		{"providerId": "nope", "vmId": "ghost"}, // unknown provider -> per-target failure
	}
	rec := e.do(t, http.MethodPost, "/api/v1/vm/bulk", map[string]any{
		"action": "power", "op": "start", "targets": targets,
	}, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("bulk = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if int(body["succeeded"].(float64)) != 2 {
		t.Fatalf("expected 2 succeeded, got %v (%s)", body["succeeded"], rec.Body.String())
	}
	if int(body["failed"].(float64)) != 1 {
		t.Fatalf("expected 1 failed (unknown provider), got %v", body["failed"])
	}
	results, _ := body["results"].([]any)
	if len(results) != 3 {
		t.Fatalf("expected 3 per-target results, got %d", len(results))
	}
}

func TestBulkUnknownActionRejected(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)
	rec := e.do(t, http.MethodPost, "/api/v1/vm/bulk", map[string]any{
		"action":  "frobnicate",
		"targets": []map[string]string{{"providerId": "test-kvm", "vmId": "x"}},
	}, cookies, csrf)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown action = %d, want 422", rec.Code)
	}
}
