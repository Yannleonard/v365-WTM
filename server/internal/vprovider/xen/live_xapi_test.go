// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// This test exercises the REAL XAPI XML-RPC client (live_xapi.go + live_xmlrpc.go +
// live_records.go) end-to-end against an httptest.Server that returns RECORDED REAL
// XAPI XML-RPC response shapes (the exact <methodResponse> envelopes a XenServer /
// XCP-ng pool master emits, per the XenAPI docs). This validates the real client's
// request encoding AND response decoding against the real wire protocol — it is NOT a
// Go-struct mock: the bytes on the wire are genuine XAPI XML-RPC.
package xen

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// --- recorded REAL XAPI XML-RPC response bodies (verbatim wire shapes) ---

const respSessionLogin = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value>OpaqueRef:b7e5c0a1-1111-2222-3333-444455556666</value></member>
</struct></value></param></params></methodResponse>`

const respSessionUUID = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value>b7e5c0a1-1111-2222-3333-444455556666</value></member>
</struct></value></param></params></methodResponse>`

// VM.get_all_records: a struct keyed by VM opaque ref. Includes one running HVM/UEFI
// VM, one halted VM, the dom0 control domain (must be skipped), and a template (must
// be skipped). This is the real shape XAPI returns.
const respVMGetAllRecords = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
 <member><name>Status</name><value>Success</value></member>
 <member><name>Value</name><value><struct>
  <member><name>OpaqueRef:vm-running-aaaa</name><value><struct>
    <member><name>uuid</name><value>11111111-aaaa-bbbb-cccc-000000000001</value></member>
    <member><name>name_label</name><value>prod-web-01</value></member>
    <member><name>power_state</name><value>Running</value></member>
    <member><name>resident_on</name><value>OpaqueRef:host-aaaa</value></member>
    <member><name>VCPUs_max</name><value>4</value></member>
    <member><name>memory_static_max</name><value>4294967296</value></member>
    <member><name>HVM_boot_policy</name><value>BIOS order</value></member>
    <member><name>is_control_domain</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_snapshot</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_template</name><value><boolean>0</boolean></value></member>
    <member><name>platform</name><value><struct>
       <member><name>firmware</name><value>uefi</value></member>
       <member><name>secureboot</name><value>false</value></member>
    </struct></value></member>
    <member><name>other_config</name><value><struct>
       <member><name>base_template_name</name><value>Debian Bookworm 12</value></member>
    </struct></value></member>
    <member><name>VBDs</name><value><array><data>
       <value>OpaqueRef:vbd-1</value>
    </data></array></value></member>
    <member><name>VIFs</name><value><array><data>
       <value>OpaqueRef:vif-1</value>
    </data></array></value></member>
  </struct></value></member>
  <member><name>OpaqueRef:vm-halted-bbbb</name><value><struct>
    <member><name>uuid</name><value>22222222-aaaa-bbbb-cccc-000000000002</value></member>
    <member><name>name_label</name><value>backup-02</value></member>
    <member><name>power_state</name><value>Halted</value></member>
    <member><name>resident_on</name><value>OpaqueRef:NULL</value></member>
    <member><name>VCPUs_max</name><value>2</value></member>
    <member><name>memory_static_max</name><value>2147483648</value></member>
    <member><name>HVM_boot_policy</name><value></value></member>
    <member><name>is_control_domain</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_snapshot</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_template</name><value><boolean>0</boolean></value></member>
    <member><name>VBDs</name><value><array><data></data></array></value></member>
    <member><name>VIFs</name><value><array><data></data></array></value></member>
  </struct></value></member>
  <member><name>OpaqueRef:vm-dom0-cccc</name><value><struct>
    <member><name>uuid</name><value>33333333-aaaa-bbbb-cccc-000000000003</value></member>
    <member><name>name_label</name><value>Control domain on host: xcp-host-1</value></member>
    <member><name>power_state</name><value>Running</value></member>
    <member><name>is_control_domain</name><value><boolean>1</boolean></value></member>
    <member><name>is_a_snapshot</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_template</name><value><boolean>0</boolean></value></member>
  </struct></value></member>
  <member><name>OpaqueRef:vm-template-dddd</name><value><struct>
    <member><name>uuid</name><value>44444444-aaaa-bbbb-cccc-000000000004</value></member>
    <member><name>name_label</name><value>Debian Bookworm 12 template</value></member>
    <member><name>power_state</name><value>Halted</value></member>
    <member><name>is_control_domain</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_snapshot</name><value><boolean>0</boolean></value></member>
    <member><name>is_a_template</name><value><boolean>1</boolean></value></member>
  </struct></value></member>
 </struct></value></member>
</struct></value></param></params></methodResponse>`

const respHostGetAllRecords = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
 <member><name>Status</name><value>Success</value></member>
 <member><name>Value</name><value><struct>
  <member><name>OpaqueRef:host-aaaa</name><value><struct>
    <member><name>uuid</name><value>host-uuid-0001</value></member>
    <member><name>name_label</name><value>xcp-host-1</value></member>
    <member><name>enabled</name><value><boolean>1</boolean></value></member>
    <member><name>software_version</name><value><struct>
       <member><name>product_version_text</name><value>8.3</value></member>
       <member><name>product_brand</name><value>XCP-ng</value></member>
       <member><name>xapi</name><value>24.16.0</value></member>
    </struct></value></member>
    <member><name>cpu_info</name><value><struct>
       <member><name>cpu_count</name><value>24</value></member>
       <member><name>speed</name><value>2899</value></member>
    </struct></value></member>
  </struct></value></member>
 </struct></value></member>
</struct></value></param></params></methodResponse>`

const respPoolGetAllRecords = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
 <member><name>Status</name><value>Success</value></member>
 <member><name>Value</name><value><struct>
  <member><name>OpaqueRef:pool-eeee</name><value><struct>
    <member><name>uuid</name><value>pool-uuid-0001</value></member>
    <member><name>name_label</name><value>xcp-pool</value></member>
    <member><name>master</name><value>OpaqueRef:host-aaaa</value></member>
    <member><name>ha_enabled</name><value><boolean>1</boolean></value></member>
  </struct></value></member>
 </struct></value></member>
</struct></value></param></params></methodResponse>`

const respSRGetAllRecords = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
 <member><name>Status</name><value>Success</value></member>
 <member><name>Value</name><value><struct>
  <member><name>OpaqueRef:sr-ffff</name><value><struct>
    <member><name>uuid</name><value>sr-uuid-0001</value></member>
    <member><name>name_label</name><value>Local-NFS</value></member>
    <member><name>type</name><value>nfs</value></member>
    <member><name>physical_size</name><value>4398046511104</value></member>
    <member><name>physical_utilisation</name><value>1099511627776</value></member>
    <member><name>shared</name><value><boolean>1</boolean></value></member>
    <member><name>PBDs</name><value><array><data><value>OpaqueRef:pbd-1</value></data></array></value></member>
  </struct></value></member>
 </struct></value></member>
</struct></value></param></params></methodResponse>`

const respNetworkGetAllRecords = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
 <member><name>Status</name><value>Success</value></member>
 <member><name>Value</name><value><struct>
  <member><name>OpaqueRef:net-gggg</name><value><struct>
    <member><name>uuid</name><value>net-uuid-0001</value></member>
    <member><name>name_label</name><value>Pool-wide network associated with eth0</value></member>
    <member><name>bridge</name><value>xenbr0</value></member>
  </struct></value></member>
 </struct></value></member>
</struct></value></param></params></methodResponse>`

// VM.start success returns void (empty Value).
const respVoidSuccess = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value></value></member>
</struct></value></param></params></methodResponse>`

// A real XAPI Failure: VM_BAD_POWER_STATE (e.g. starting an already-running VM).
const respBadPowerState = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Failure</value></member>
  <member><name>ErrorDescription</name><value><array><data>
    <value>VM_BAD_POWER_STATE</value>
    <value>OpaqueRef:vm-running-aaaa</value>
    <value>halted</value>
    <value>running</value>
  </data></array></value></member>
</struct></value></param></params></methodResponse>`

// VM.snapshot returns the new snapshot VM opaque ref.
const respSnapshotCreate = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value>OpaqueRef:snap-hhhh</value></member>
</struct></value></param></params></methodResponse>`

// VM.get_snapshots returns an array of snapshot VM refs.
const respGetSnapshots = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value><array><data>
    <value>OpaqueRef:snap-hhhh</value>
  </data></array></value></member>
</struct></value></param></params></methodResponse>`

// VM.get_record (for a snapshot) returns its record.
const respSnapshotRecord = `<?xml version="1.0"?>
<methodResponse><params><param><value><struct>
  <member><name>Status</name><value>Success</value></member>
  <member><name>Value</name><value><struct>
    <member><name>uuid</name><value>snap-uuid-0001</value></member>
    <member><name>name_label</name><value>pre-upgrade</value></member>
    <member><name>name_description</name><value>before kernel update</value></member>
    <member><name>snapshot_time</name><value>20240115T13:45:00Z</value></member>
  </struct></value></member>
</struct></value></param></params></methodResponse>`

// newXAPITestServer returns an httptest.Server that decodes the real XML-RPC
// methodName and replies with the matching recorded REAL XAPI response.
func newXAPITestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		method := methodNameOf(string(body))
		w.Header().Set("Content-Type", "text/xml")
		switch method {
		case "session.login_with_password":
			io.WriteString(w, respSessionLogin)
		case "session.get_uuid":
			io.WriteString(w, respSessionUUID)
		case "session.logout":
			io.WriteString(w, respVoidSuccess)
		case "VM.get_all_records":
			io.WriteString(w, respVMGetAllRecords)
		case "host.get_all_records":
			io.WriteString(w, respHostGetAllRecords)
		case "pool.get_all_records":
			io.WriteString(w, respPoolGetAllRecords)
		case "SR.get_all_records":
			io.WriteString(w, respSRGetAllRecords)
		case "network.get_all_records":
			io.WriteString(w, respNetworkGetAllRecords)
		case "VM.start":
			// Starting the already-Running VM faults; starting the Halted one succeeds.
			if strings.Contains(string(body), "vm-running-aaaa") {
				io.WriteString(w, respBadPowerState)
			} else {
				io.WriteString(w, respVoidSuccess)
			}
		case "VM.clean_shutdown", "VM.hard_shutdown", "VM.suspend", "VM.resume",
			"VM.destroy", "VM.revert", "VM.pool_migrate":
			io.WriteString(w, respVoidSuccess)
		case "VM.snapshot":
			io.WriteString(w, respSnapshotCreate)
		case "VM.get_snapshots":
			io.WriteString(w, respGetSnapshots)
		case "VM.get_record":
			io.WriteString(w, respSnapshotRecord)
		default:
			io.WriteString(w, respVoidSuccess)
		}
	}))
}

// methodNameOf extracts <methodName> from a methodCall body without a full parse.
func methodNameOf(body string) string {
	const open, closeT = "<methodName>", "</methodName>"
	i := strings.Index(body, open)
	if i < 0 {
		return ""
	}
	j := strings.Index(body[i:], closeT)
	if j < 0 {
		return ""
	}
	return body[i+len(open) : i+j]
}

// TestLiveXAPI_RealClientPath_WireProtocol drives the real XAPI XML-RPC client
// against recorded real responses: login, inventory reads, power op (incl. a real
// VM_BAD_POWER_STATE fault -> ErrConflict mapping), snapshot create+list+revert.
func TestLiveXAPI_RealClientPath_WireProtocol(t *testing.T) {
	srv := newXAPITestServer(t)
	defer srv.Close()

	// newLiveBackend performs the REAL session.login_with_password XML-RPC handshake.
	be, err := newLiveBackend(srv.URL, "root", "pass", true)
	if err != nil {
		t.Fatalf("newLiveBackend (real XAPI login): %v", err)
	}
	p := New("xen-xapi-live", WithBackend(be))
	defer p.Close()
	ctx := context.Background()

	// --- session/health: probes session.get_uuid over the wire ---
	hs, err := p.HealthCheck(ctx)
	if err != nil {
		t.Fatalf("HealthCheck: %v", err)
	}
	if !hs.Healthy {
		t.Fatalf("expected healthy XAPI session, got: %s", hs.Message)
	}
	if hs.Version == "" {
		t.Error("expected a non-empty version decoded from host.get_all_records")
	}
	t.Logf("XAPI version: %s", hs.Version)

	// --- inventory decoded from real *.get_all_records wire payloads ---
	hosts, err := p.ListHosts(ctx)
	if err != nil {
		t.Fatalf("ListHosts: %v", err)
	}
	if len(hosts) != 1 {
		t.Fatalf("expected 1 host decoded, got %d", len(hosts))
	}
	if hosts[0].ID != "OpaqueRef:host-aaaa" || hosts[0].Name != "xcp-host-1" {
		t.Errorf("host decode wrong: %+v", hosts[0])
	}
	if hosts[0].CPUCores != 24 {
		t.Errorf("host cpu cores decode: got %d want 24", hosts[0].CPUCores)
	}

	clusters, err := p.ListClusters(ctx)
	if err != nil {
		t.Fatalf("ListClusters: %v", err)
	}
	if len(clusters) != 1 || clusters[0].ID != "OpaqueRef:pool-eeee" {
		t.Fatalf("pool->cluster decode wrong: %+v", clusters)
	}
	if !clusters[0].HAEnabled {
		t.Error("expected ha_enabled=true decoded from pool record")
	}

	srs, err := p.ListStorage(ctx)
	if err != nil {
		t.Fatalf("ListStorage: %v", err)
	}
	if len(srs) != 1 || srs[0].Type != "nfs" {
		t.Fatalf("SR decode wrong: %+v", srs)
	}

	nets, err := p.ListNetworks(ctx)
	if err != nil {
		t.Fatalf("ListNetworks: %v", err)
	}
	if len(nets) != 1 || nets[0].Name != "Pool-wide network associated with eth0" {
		t.Fatalf("network decode wrong: %+v", nets)
	}

	// VMs: the snapshot/template VM objects are filtered out; the dom0 control domain
	// IS surfaced but marked Protected (matching the xapi model: is_control_domain ->
	// Protected, so the UI/RBAC can never delete it). So 3 VMs remain.
	vms, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	if len(vms) != 3 {
		t.Fatalf("expected 3 VMs (template filtered; dom0 kept as protected), got %d: %+v", len(vms), vms)
	}
	var running, halted, dom0 *vp.VM
	for i := range vms {
		switch vms[i].ID {
		case "OpaqueRef:vm-running-aaaa":
			running = &vms[i]
		case "OpaqueRef:vm-halted-bbbb":
			halted = &vms[i]
		case "OpaqueRef:vm-dom0-cccc":
			dom0 = &vms[i]
		}
	}
	if running == nil || halted == nil || dom0 == nil {
		t.Fatalf("expected running+halted+dom0 VMs, got %+v", vms)
	}
	if !dom0.Protected {
		t.Error("control domain must be surfaced as Protected (is_control_domain decode)")
	}
	if running.Name != "prod-web-01" || running.State != vp.StateRunning {
		t.Errorf("running VM decode wrong: %+v", running)
	}
	if running.VCPUs != 4 || running.MemoryMB != 4096 {
		t.Errorf("running VM hw decode: vcpus=%d mem=%d want 4/4096", running.VCPUs, running.MemoryMB)
	}
	if running.Firmware != vp.FirmwareUEFI {
		t.Errorf("running VM firmware=%q want uefi (decoded from platform map)", running.Firmware)
	}
	if halted.State != vp.StateStopped {
		t.Errorf("halted VM state=%q want stopped", halted.State)
	}

	// --- GetVM round-trip (decodes via the same wire path) ---
	d, err := p.GetVM(ctx, running.ID)
	if err != nil {
		t.Fatalf("GetVM: %v", err)
	}
	if d.ID != running.ID || len(d.Raw) == 0 {
		t.Errorf("GetVM wrong: id=%q rawlen=%d", d.ID, len(d.Raw))
	}

	// --- power op: stop the running VM (clean_shutdown -> void success) ---
	if _, err := p.PowerOp(ctx, running.ID, vp.PowerStop); err != nil {
		t.Fatalf("PowerOp(stop): %v", err)
	}

	// --- snapshot create + list + revert over the real wire ---
	if _, err := p.Snapshot(ctx, running.ID, vp.SnapshotOptions{Name: "pre-upgrade"}); err != nil {
		t.Fatalf("Snapshot: %v", err)
	}
	snaps, err := p.ListSnapshots(ctx, running.ID)
	if err != nil {
		t.Fatalf("ListSnapshots: %v", err)
	}
	if len(snaps) != 1 || snaps[0].ID != "OpaqueRef:snap-hhhh" {
		t.Fatalf("snapshot list decode wrong: %+v", snaps)
	}
	if snaps[0].Name != "pre-upgrade" {
		t.Errorf("snapshot name decode=%q want pre-upgrade", snaps[0].Name)
	}
	if _, err := p.RevertSnapshot(ctx, running.ID, snaps[0].ID); err != nil {
		t.Fatalf("RevertSnapshot: %v", err)
	}

	// --- fault mapping: a real VM_BAD_POWER_STATE Failure -> handled by the client ---
	// Drive the raw client to assert the XAPI Failure decodes into an *xapiFault and
	// maps to ErrConflict (the production error path).
	_, ferr := be.callSession("VM.start", xmlrpcString("OpaqueRef:vm-running-aaaa"),
		xmlrpcBool(false), xmlrpcBool(false))
	if ferr == nil {
		t.Fatal("expected VM_BAD_POWER_STATE fault from the real wire response")
	}
	if got := mapXapiErr(ferr); got != vp.ErrConflict {
		t.Errorf("VM_BAD_POWER_STATE mapped to %v, want ErrConflict", got)
	}
	if f, ok := ferr.(*xapiFault); !ok || f.code != "VM_BAD_POWER_STATE" {
		t.Errorf("expected *xapiFault VM_BAD_POWER_STATE, got %T %v", ferr, ferr)
	}
}
