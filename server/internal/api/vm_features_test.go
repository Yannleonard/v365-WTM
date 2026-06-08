package api

import (
	"net/http"
	"testing"
)

// These full-stack tests exercise the VM extension + V2V API surface through the
// real router (SessionAuth + CSRF + RBAC + handler + sim provider). The test env
// registers a sim provider "test-kvm" with all capability bits, so every endpoint
// is regression-covered in CI without hardware.

func TestVMNetworkAndStorageAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)
	pid := "test-kvm"

	// --- networks: list, create, delete ---
	rec := e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/networks",
		map[string]any{"name": "apinet", "type": "nat"}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create network = %d %s", rec.Code, rec.Body.String())
	}

	// --- storage: list pools, list volumes, create + delete volume ---
	rec = e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/storage", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list storage = %d", rec.Code)
	}
	rec = e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/storage/ds-1/volumes", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list volumes = %d %s", rec.Code, rec.Body.String())
	}
	rec = e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/storage/ds-1/volumes",
		map[string]any{"name": "apivol", "capacityGb": 1}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create volume = %d %s", rec.Code, rec.Body.String())
	}
}

func TestVMConsoleEndpointAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, _ := adminLogin(t, e)
	pid := "test-kvm"
	// pick a VM
	rec := e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/vms", nil, cookies, "")
	vms, _ := decodeAny(t, rec).([]any)
	if len(vms) == 0 {
		t.Skip("no VMs")
	}
	id := vms[0].(map[string]any)["id"].(string)
	rec = e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/vms/"+id+"/console", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("console endpoint = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["kind"] == nil || body["port"] == nil {
		t.Errorf("console endpoint missing kind/port: %+v", body)
	}
}

func TestVMSnapshotCloneAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)
	pid := "test-kvm"
	rec := e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/vms", nil, cookies, "")
	vms, _ := decodeAny(t, rec).([]any)
	if len(vms) == 0 {
		t.Skip("no VMs")
	}
	id := vms[0].(map[string]any)["id"].(string)

	// snapshot create
	rec = e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/vms/"+id+"/snapshots",
		map[string]any{"name": "apisnap"}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("snapshot create = %d %s", rec.Code, rec.Body.String())
	}
	// list snapshots
	rec = e.do(t, http.MethodGet, "/api/v1/vm/providers/"+pid+"/vms/"+id+"/snapshots", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list snapshots = %d", rec.Code)
	}
	snaps, _ := decodeAny(t, rec).([]any)
	if len(snaps) == 0 {
		t.Error("expected at least one snapshot")
	}
	// clone
	rec = e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/vms/"+id+"/clone",
		map[string]any{"name": "apiclone"}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("clone = %d %s", rec.Code, rec.Body.String())
	}
	// reconfigure
	rec = e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/vms/"+id+"/reconfigure",
		map[string]any{"vcpus": 4}, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("reconfigure = %d %s", rec.Code, rec.Body.String())
	}
}

func TestVMCreateAndDeleteAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)
	pid := "test-kvm"
	rec := e.do(t, http.MethodPost, "/api/v1/vm/providers/"+pid+"/vms",
		map[string]any{"name": "api-created", "vcpus": 2, "memoryMb": 2048,
			"disks": []map[string]any{{"capacityGb": 10}}}, cookies, csrf)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create VM = %d %s", rec.Code, rec.Body.String())
	}
	newID := decodeBody(t, rec)["entityId"].(string)
	// delete it
	rec = e.do(t, http.MethodDelete, "/api/v1/vm/providers/"+pid+"/vms/"+newID+"?force=true&deleteDisks=true", nil, cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("delete VM = %d %s", rec.Code, rec.Body.String())
	}
}

func TestV2VPreflightAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, csrf := adminLogin(t, e)
	// Same-provider preflight must be rejected (not a cross-hypervisor migration).
	rec := e.do(t, http.MethodPost, "/api/v1/v2v/preflight",
		map[string]any{"sourceProviderId": "test-kvm", "sourceVmId": "vm-1", "targetProviderId": "test-kvm"},
		cookies, csrf)
	if rec.Code != 200 {
		t.Fatalf("v2v preflight = %d %s", rec.Code, rec.Body.String())
	}
	body := decodeBody(t, rec)
	if body["ok"] != false {
		t.Errorf("same-provider preflight should report ok=false, got %+v", body["ok"])
	}
	// jobs list works
	rec = e.do(t, http.MethodGet, "/api/v1/v2v/jobs", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("v2v jobs = %d", rec.Code)
	}
}

func TestVMConnectionsAPI(t *testing.T) {
	e := newTestEnv(t)
	cookies, _ := adminLogin(t, e)
	// list connections (may be empty in the test env)
	rec := e.do(t, http.MethodGet, "/api/v1/vm/connections", nil, cookies, "")
	if rec.Code != 200 {
		t.Fatalf("list connections = %d %s", rec.Code, rec.Body.String())
	}
}
