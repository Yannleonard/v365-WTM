//go:build xen_live

// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// This file is compiled ONLY under `-tags xen_live`. It is the seam where a real
// XAPI transport would be wired into the existing pure-Go normalization core.
// XAPI is an XML-RPC (or JSON-RPC) API spoken over HTTP/HTTPS to a XenServer /
// XCP-ng pool master (default https://<master>/ , endpoint path "/"). A live
// backend would, using ONLY the Go standard library (net/http + encoding/xml or
// encoding/json — NO external XAPI SDK, so go.mod stays clean and CGO_ENABLED=0
// holds):
//
//  1. session.login_with_password(user, pass, "", "") -> session ref
//  2. VM.get_all_records / host.get_all_records / pool.get_all_records /
//     SR.get_all_records / network.get_all_records to populate the xapi* model
//     structs the core already normalizes (opaque refs, power_state tokens, bytes)
//  3. VM.start / VM.clean_shutdown / VM.hard_reboot / VM.suspend / VM.resume for
//     PowerOp; VM.destroy for delete; VM.snapshot / VM.checkpoint / VM.revert for
//     snapshots; VM.clone / VM.copy for clone; VM.pool_migrate for XenMotion;
//     the export HTTP handler (GET /export?ref=...) for ExportVM; host RRD
//     (http_get_rrd) for metrics; event.from / event.next long-poll for events
//  4. session.logout on close
//
// Per D-005 the default (CGO_ENABLED=0, distroless) build never compiles this file,
// so the in-memory fake (sim_backend.go) is what conformance runs against and CI
// stays hardware-free. It is intentionally a stub: a live XAPI session needs a
// reachable pool master, unavailable in CI.
//
// To make this real, replace the body of liveBackend's methods with stdlib
// net/http XML-RPC calls as sketched above. No change to xen.go / xapi.go is
// needed — only this file.
package xen

import (
	"errors"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// errLiveUnavailable is returned until a real XAPI transport is wired here.
var errLiveUnavailable = errors.New("xen: live XAPI backend not implemented in this build")

// liveBackend would hold the XAPI session (stdlib net/http client + session ref).
// Stubbed.
type liveBackend struct {
	url     string
	session string
}

// NewLive constructs a Provider backed by a live XAPI session at url (e.g.
// "https://xcp-master.example.com/"). Available only under xen_live.
func NewLive(id, url, user, pass string, opts ...Option) (*Provider, error) {
	be := &liveBackend{url: url}
	if !be.healthy() {
		return nil, errLiveUnavailable
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

func (l *liveBackend) version() string { return "" }
func (l *liveBackend) healthy() bool   { return false } // stub: never healthy
func (l *liveBackend) close() error    { return nil }

func (l *liveBackend) listHosts() []*xapiHost                  { return nil }
func (l *liveBackend) getHost(string) (*xapiHost, bool)        { return nil, false }
func (l *liveBackend) listVMs() []*xapiVM                      { return nil }
func (l *liveBackend) getVM(string) (*xapiVM, bool)            { return nil, false }
func (l *liveBackend) listSRs() []*xapiSR                      { return nil }
func (l *liveBackend) listNetworks() []*xapiNetwork            { return nil }

func (l *liveBackend) createVM(*xapiVM)                  {}
func (l *liveBackend) destroyVM(string)                  {}
func (l *liveBackend) setPowerState(string, xapiPowerState) {}
func (l *liveBackend) vmsOnHost(string) int              { return 0 }

func (l *liveBackend) listSnapshots(string) []vp.Snapshot     { return nil }
func (l *liveBackend) createSnapshot(string, vp.Snapshot)     {}
func (l *liveBackend) setCurrentSnapshot(string, string) bool { return false }

func (l *liveBackend) pool() *xapiPool { return nil }

var _ xapiBackend = (*liveBackend)(nil)
