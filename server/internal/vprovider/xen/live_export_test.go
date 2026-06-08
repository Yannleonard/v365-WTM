// modeled on server/internal/vprovider/xen/live_xapi_test.go (see CASTOR-REUSE.md)
//
// This test proves the REAL Xen XVA export path (live_xapi.go exportStream +
// xen.go ExportVM live branch) against a simulated XAPI WIRE: an httptest.Server that
// (a) answers the real XML-RPC session.login_with_password + VM.get_all_records so the
// live backend can establish a session and resolve the VM uuid, and (b) serves the
// XAPI HTTP /export handler returning a fake-but-real XVA byte stream for a valid
// session+uuid, and 401/403/404 otherwise. It asserts that ExportVM streams the REAL
// bytes from the HTTP body (NOT the XENEXPORT sim placeholder), and that a refusal
// yields a CLEAR error. The real XCP-ng/XenServer host proves the same path tomorrow.
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

// fakeXVA is a stand-in for the real XVA tar stream a XenServer /export handler emits.
// It is deliberately NOT the sim's "XENEXPORT" placeholder so the test can distinguish
// "real HTTP body streamed" from "sim placeholder fabricated".
const fakeXVA = "ustar-XVA\x00ova.xml\x00<value name='vm'/>\x00Ref:vm-running-aaaa-disk0.vhd-bytes"

// validExportSession is the session ref the recorded login response (respSessionLogin)
// hands back; the export handler accepts only this session.
const validExportSession = "OpaqueRef:b7e5c0a1-1111-2222-3333-444455556666"

// runningVMUUID is the uuid of OpaqueRef:vm-running-aaaa in respVMGetAllRecords.
const runningVMUUID = "11111111-aaaa-bbbb-cccc-000000000001"

// newXAPIExportTestServer wraps the XML-RPC handler with the XAPI HTTP /export handler.
func newXAPIExportTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/export" {
			q := r.URL.Query()
			sid := q.Get("session_id")
			uuid := q.Get("uuid")
			switch {
			case sid != validExportSession:
				// Session rejected: real XAPI returns 403 from the export handler.
				w.WriteHeader(http.StatusForbidden)
				io.WriteString(w, "Authentication failed")
				return
			case uuid != runningVMUUID:
				w.WriteHeader(http.StatusNotFound)
				io.WriteString(w, "VM not found")
				return
			default:
				// Real XVA stream: octet-stream body (Content-Length set here; a real
				// XAPI export is usually chunked/unknown-length, exercised separately).
				w.Header().Set("Content-Type", "application/octet-stream")
				w.WriteHeader(http.StatusOK)
				io.WriteString(w, fakeXVA)
				return
			}
		}
		// Fall through to the XML-RPC handler (login, VM.get_all_records, ...).
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
		default:
			io.WriteString(w, respVoidSuccess)
		}
	}))
}

// TestLiveXAPI_ExportVM_StreamsRealXVA proves the live ExportVM streams the real HTTP
// /export body (not the sim placeholder) for a valid session+uuid.
func TestLiveXAPI_ExportVM_StreamsRealXVA(t *testing.T) {
	srv := newXAPIExportTestServer(t)
	defer srv.Close()

	be, err := newLiveBackend(srv.URL, "root", "pass", true)
	if err != nil {
		t.Fatalf("newLiveBackend (real XAPI login): %v", err)
	}
	p := New("xen-xapi-live", WithBackend(be))
	defer p.Close()
	ctx := context.Background()

	// Resolve the running VM (decoded from the real VM.get_all_records wire payload).
	vms, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		t.Fatalf("ListVMs: %v", err)
	}
	var running *vp.VM
	for i := range vms {
		if vms[i].ID == "OpaqueRef:vm-running-aaaa" {
			running = &vms[i]
		}
	}
	if running == nil {
		t.Fatalf("running VM not found in %+v", vms)
	}

	rc, info, err := p.ExportVM(ctx, running.ID, vp.DiskVHD)
	if err != nil {
		t.Fatalf("ExportVM (live XVA): %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read export stream: %v", err)
	}

	// PROOF 1: the bytes are the REAL HTTP /export body, not the sim placeholder.
	if string(got) != fakeXVA {
		t.Fatalf("export body mismatch:\n got=%q\nwant=%q", string(got), fakeXVA)
	}
	if strings.Contains(string(got), "XENEXPORT") {
		t.Fatalf("live export returned the SIM placeholder, not the real HTTP stream: %q", got)
	}

	// PROOF 2: ExportInfo reflects the real VM + stream.
	if info == nil {
		t.Fatal("nil ExportInfo")
	}
	if info.SourceVMID != running.ID {
		t.Errorf("SourceVMID=%q want %q", info.SourceVMID, running.ID)
	}
	if info.Format != vp.DiskVHD {
		t.Errorf("Format=%q want vhd (requested format echoed for XVA container)", info.Format)
	}
	if info.DiskCount != 1 {
		t.Errorf("DiskCount=%d want 1 (one VBD on the running VM)", info.DiskCount)
	}
	if info.SizeBytes != int64(len(fakeXVA)) {
		t.Errorf("SizeBytes=%d want %d (HTTP Content-Length)", info.SizeBytes, len(fakeXVA))
	}
	t.Logf("streamed %d real XVA bytes from the XAPI /export HTTP handler", len(got))
}

// TestLiveXAPI_ExportVM_RefusalIsClearError proves a refused export (bad session ->
// 403) surfaces a CLEAR error, never a placeholder / false success.
func TestLiveXAPI_ExportVM_RefusalIsClearError(t *testing.T) {
	srv := newXAPIExportTestServer(t)
	defer srv.Close()

	be, err := newLiveBackend(srv.URL, "root", "pass", true)
	if err != nil {
		t.Fatalf("newLiveBackend: %v", err)
	}
	// Corrupt the cached session so the export handler returns 403.
	be.mu.Lock()
	be.session = "OpaqueRef:bogus-session"
	be.mu.Unlock()

	rc, _, err := be.exportStream(context.Background(), runningVMUUID)
	if err == nil {
		if rc != nil {
			rc.Close()
		}
		t.Fatal("expected a clear refusal error for a rejected session, got nil")
	}
	if !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "refused") {
		t.Fatalf("refusal error not clear about HTTP status: %v", err)
	}
	t.Logf("refusal surfaced clearly: %v", err)
}

// TestLiveXAPI_ExportVM_UnknownVMIsClearError proves an unknown uuid (404) is a clear
// error, not a fabricated stream.
func TestLiveXAPI_ExportVM_UnknownVMIsClearError(t *testing.T) {
	srv := newXAPIExportTestServer(t)
	defer srv.Close()

	be, err := newLiveBackend(srv.URL, "root", "pass", true)
	if err != nil {
		t.Fatalf("newLiveBackend: %v", err)
	}
	rc, _, err := be.exportStream(context.Background(), "00000000-dead-beef-0000-000000000000")
	if err == nil {
		if rc != nil {
			rc.Close()
		}
		t.Fatal("expected a clear not-found error for an unknown uuid, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Fatalf("unknown-VM error not clear about HTTP status: %v", err)
	}
	t.Logf("unknown VM surfaced clearly: %v", err)
}
