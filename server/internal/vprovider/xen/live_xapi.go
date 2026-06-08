// modeled on server/internal/vprovider/kvm/live_libvirt.go (see CASTOR-REUSE.md)
//
// live_xapi.go is the REAL Xen/XAPI backend. It speaks the OFFICIAL XenAPI (XAPI)
// over its XML-RPC wire protocol using ONLY the Go standard library
// (net/http + encoding/xml) — NO external XAPI SDK — so it is CGO-free, carries NO
// build tag, and keeps go.mod lean (D-005 / D-007). It satisfies the existing
// xapiBackend seam (xen.go) so the pure-Go normalization core in xen.go / xapi.go is
// reused verbatim against a real XenServer / XCP-ng pool master (or, in tests,
// against recorded REAL XAPI XML-RPC responses served over httptest — proving the
// real XML-RPC request-encode / response-decode client code, exercised against the
// real wire protocol).
//
// Official XAPI methods used (XML-RPC, POST to https://<master>/ , path "/"):
//   session  : session.login_with_password(user, pass, "", "") -> session ref;
//              session.logout
//   inventory: VM.get_all_records / host.get_all_records / pool.get_all_records /
//              SR.get_all_records / network.get_all_records
//   lifecycle: VM.start / VM.clean_shutdown / VM.hard_shutdown / VM.hard_reboot /
//              VM.suspend / VM.resume ; VM.destroy
//   snapshots: VM.snapshot / VM.revert (snapshots are themselves VM objects)
//   clone    : VM.clone (CoW) / VM.copy (full)
//   migrate  : VM.pool_migrate (XenMotion)
//   events   : event.next (long-poll; surfaced via the provider event stream)
//
// XAPI replies are XML-RPC structs with Status="Success"|"Failure"; a Failure
// carries an ErrorDescription string array whose first element is the error code
// (e.g. HANDLE_INVALID, VM_BAD_POWER_STATE). mapXapiErr maps those to the contract
// sentinels. The seam methods are synchronous and DO NOT return errors (they mirror
// an in-memory model), so the live backend performs the RPC eagerly, records the last
// transport error, and flips healthy()->false on a hard transport failure.
package xen

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// liveBackend is the real, pure-Go XAPI XML-RPC backend.
type liveBackend struct {
	url    string
	client *http.Client

	mu       sync.RWMutex
	session  string
	ver      string
	healthOK bool
	lastErr  error
}

// NewLive constructs a Provider backed by a REAL XAPI session. endpoint is the pool
// master URL ("https://xcp-master.example.com" or with a trailing "/"); user/pass are
// the XAPI credentials; insecure skips TLS verification (lab/self-signed certs).
func NewLive(id, endpoint, user, pass string, insecure bool, opts ...Option) (*Provider, error) {
	be, err := newLiveBackend(endpoint, user, pass, insecure)
	if err != nil {
		return nil, err
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

// newLiveBackend dials XAPI and performs the official session.login_with_password
// handshake, caching the session ref + pool master version.
func newLiveBackend(endpoint, user, pass string, insecure bool) (*liveBackend, error) {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" {
		return nil, fmt.Errorf("xen: empty XAPI endpoint")
	}
	tr := &http.Transport{}
	if insecure {
		tr.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
	}
	be := &liveBackend{
		url:    endpoint + "/",
		client: &http.Client{Timeout: 60 * time.Second, Transport: tr},
	}
	sess, err := be.login(user, pass)
	if err != nil {
		return nil, fmt.Errorf("xen: XAPI session.login_with_password: %w", err)
	}
	be.mu.Lock()
	be.session = sess
	be.healthOK = true
	be.mu.Unlock()
	// Best-effort: cache the pool master software version.
	if v := be.poolMasterVersion(); v != "" {
		be.mu.Lock()
		be.ver = v
		be.mu.Unlock()
	}
	return be, nil
}

// login performs session.login_with_password(user, pass, "", "") and returns the
// session opaque ref.
func (b *liveBackend) login(user, pass string) (string, error) {
	val, err := b.call("session.login_with_password",
		xmlrpcString(user), xmlrpcString(pass), xmlrpcString(""), xmlrpcString(""))
	if err != nil {
		return "", err
	}
	return val.text(), nil
}

// poolMasterVersion fetches host.get_all_records and returns the first host's
// product version (best-effort, used for HealthStatus.Version).
func (b *liveBackend) poolMasterVersion() string {
	for _, h := range b.listHosts() {
		if h.Version != "" {
			return h.Version
		}
	}
	return ""
}

// fail records a transport error; a hard transport failure marks the backend
// unhealthy. Logical XAPI failures (mapped via mapXapiErr) do not.
func (b *liveBackend) fail(err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	b.lastErr = err
	if isTransportError(err) {
		b.healthOK = false
	}
	b.mu.Unlock()
}

// isTransportError reports a connection/HTTP-level failure (not a logical XAPI
// Failure, which is an *xapiFault).
func isTransportError(err error) bool {
	if err == nil {
		return false
	}
	_, isFault := err.(*xapiFault)
	return !isFault
}

// --- connection / session ---

func (b *liveBackend) version() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ver
}

func (b *liveBackend) healthy() bool {
	b.mu.RLock()
	sess := b.session
	ok := b.healthOK
	b.mu.RUnlock()
	if !ok || sess == "" {
		return false
	}
	// Active probe: session.get_uuid round-trips and validates the session.
	if _, err := b.callSession("session.get_uuid", xmlrpcString(sess)); err != nil {
		b.fail(err)
		return false
	}
	return true
}

func (b *liveBackend) close() error {
	b.mu.Lock()
	sess := b.session
	b.session = ""
	b.healthOK = false
	b.mu.Unlock()
	if sess != "" {
		_, _ = b.call("session.logout", xmlrpcString(sess))
	}
	return nil
}

// callSession invokes an XAPI method whose FIRST argument is the session ref.
func (b *liveBackend) callSession(method string, rest ...xmlrpcValue) (*xmlrpcValue, error) {
	b.mu.RLock()
	sess := b.session
	b.mu.RUnlock()
	args := append([]xmlrpcValue{xmlrpcString(sess)}, rest...)
	return b.call(method, args...)
}

// --- inventory (*.get_all_records) ---

func (b *liveBackend) listHosts() []*xapiHost {
	val, err := b.callSession("host.get_all_records")
	if err != nil {
		b.fail(err)
		return nil
	}
	var out []*xapiHost
	for ref, rec := range val.structMap() {
		out = append(out, parseHostRecord(ref, rec))
	}
	return out
}

func (b *liveBackend) getHost(ref string) (*xapiHost, bool) {
	for _, h := range b.listHosts() {
		if h.Ref == ref {
			return h, true
		}
	}
	return nil, false
}

func (b *liveBackend) listVMs() []*xapiVM {
	val, err := b.callSession("VM.get_all_records")
	if err != nil {
		b.fail(err)
		return nil
	}
	var out []*xapiVM
	for ref, rec := range val.structMap() {
		v := parseVMRecord(ref, rec)
		if v == nil {
			continue // skip control domains/templates/snapshots per parse rules
		}
		out = append(out, v)
	}
	return out
}

func (b *liveBackend) getVM(ref string) (*xapiVM, bool) {
	for _, v := range b.listVMs() {
		if v.Ref == ref {
			return v, true
		}
	}
	return nil, false
}

func (b *liveBackend) listSRs() []*xapiSR {
	val, err := b.callSession("SR.get_all_records")
	if err != nil {
		b.fail(err)
		return nil
	}
	var out []*xapiSR
	for ref, rec := range val.structMap() {
		out = append(out, parseSRRecord(ref, rec))
	}
	return out
}

func (b *liveBackend) listNetworks() []*xapiNetwork {
	val, err := b.callSession("network.get_all_records")
	if err != nil {
		b.fail(err)
		return nil
	}
	var out []*xapiNetwork
	for ref, rec := range val.structMap() {
		out = append(out, parseNetworkRecord(ref, rec))
	}
	return out
}

func (b *liveBackend) pool() *xapiPool {
	val, err := b.callSession("pool.get_all_records")
	if err != nil {
		b.fail(err)
		return nil
	}
	for ref, rec := range val.structMap() {
		pl := parsePoolRecord(ref, rec)
		// The pool record references its master host; enrich HostRefs from hosts.
		if hosts := b.listHosts(); len(hosts) > 0 {
			pl.HostRefs = pl.HostRefs[:0]
			for _, h := range hosts {
				pl.HostRefs = append(pl.HostRefs, h.Ref)
			}
		}
		return pl
	}
	return nil
}

// --- lifecycle ---

func (b *liveBackend) createVM(v *xapiVM) {
	// XAPI VM.create requires a large record; real provisioning typically clones a
	// template. The frozen core builds an xapiVM and expects the backend to register
	// it. We issue VM.create with the essential fields; XAPI assigns the opaque ref.
	rec := xmlrpcStruct(map[string]xmlrpcValue{
		"name_label":            xmlrpcString(v.NameLabel),
		"name_description":      xmlrpcString(""),
		"user_version":          xmlrpcString("1"),
		"is_a_template":         xmlrpcBool(false),
		"memory_static_max":     xmlrpcString(strconv.FormatInt(v.MemoryB, 10)),
		"memory_dynamic_max":    xmlrpcString(strconv.FormatInt(v.MemoryB, 10)),
		"memory_dynamic_min":    xmlrpcString(strconv.FormatInt(v.MemoryB, 10)),
		"memory_static_min":     xmlrpcString(strconv.FormatInt(v.MemoryB, 10)),
		"VCPUs_max":             xmlrpcString(strconv.Itoa(v.VCPUsMax)),
		"VCPUs_at_startup":      xmlrpcString(strconv.Itoa(v.VCPUsMax)),
		"actions_after_shutdown": xmlrpcString("destroy"),
		"actions_after_reboot":  xmlrpcString("restart"),
		"actions_after_crash":   xmlrpcString("restart"),
		"HVM_boot_policy":       xmlrpcString(hvmPolicy(v)),
		"PV_bootloader":         xmlrpcString(""),
	})
	val, err := b.callSession("VM.create", rec)
	if err != nil {
		b.fail(err)
		return
	}
	if ref := val.text(); ref != "" {
		v.Ref = ref
	}
}

func hvmPolicy(v *xapiVM) string {
	if v.HVM {
		return "BIOS order"
	}
	return ""
}

func (b *liveBackend) destroyVM(ref string) {
	if _, err := b.callSession("VM.destroy", xmlrpcString(ref)); err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) setPowerState(ref string, s xapiPowerState) {
	var err error
	switch s {
	case psRunning:
		// Resume if suspended, else start. Try resume first; on a bad-power-state
		// fault fall back to start (Halted -> Running).
		_, rerr := b.callSession("VM.resume", xmlrpcString(ref), xmlrpcBool(false), xmlrpcBool(false))
		if rerr != nil {
			_, err = b.callSession("VM.start", xmlrpcString(ref), xmlrpcBool(false), xmlrpcBool(false))
		}
	case psHalted:
		// Graceful clean_shutdown; if that faults, hard_shutdown.
		_, serr := b.callSession("VM.clean_shutdown", xmlrpcString(ref))
		if serr != nil {
			_, err = b.callSession("VM.hard_shutdown", xmlrpcString(ref))
		}
	case psSuspended:
		_, err = b.callSession("VM.suspend", xmlrpcString(ref))
	}
	if err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) vmsOnHost(hostRef string) int {
	n := 0
	for _, v := range b.listVMs() {
		if v.ResidentOn == hostRef {
			n++
		}
	}
	return n
}

// --- snapshots ---

func (b *liveBackend) listSnapshots(ref string) []vp.Snapshot {
	// VM.get_snapshots returns the snapshot VM refs; fetch each record for detail.
	val, err := b.callSession("VM.get_snapshots", xmlrpcString(ref))
	if err != nil {
		b.fail(err)
		return nil
	}
	var out []vp.Snapshot
	for _, sv := range val.arrayValues() {
		snapRef := sv.text()
		if snapRef == "" || snapRef == "OpaqueRef:NULL" {
			continue
		}
		rec, rerr := b.callSession("VM.get_record", xmlrpcString(snapRef))
		if rerr != nil {
			continue
		}
		m := rec.structMapFlat()
		out = append(out, vp.Snapshot{
			ID:          snapRef,
			VMID:        ref,
			Name:        m["name_label"],
			Description: m["name_description"],
			ParentID:    ref,
			CreatedAt:   parseXapiTime(m["snapshot_time"]),
		})
	}
	return out
}

func (b *liveBackend) createSnapshot(ref string, snap vp.Snapshot) {
	if _, err := b.callSession("VM.snapshot", xmlrpcString(ref), xmlrpcString(snap.Name)); err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) setCurrentSnapshot(ref, snapID string) bool {
	// VM.revert(snapshot_ref) reverts the parent VM to the snapshot's state.
	if _, err := b.callSession("VM.revert", xmlrpcString(snapID)); err != nil {
		b.fail(err)
		return false
	}
	return true
}

// migrate performs a real XenMotion via VM.pool_migrate(vm, host, options). Exposed
// beyond the xapiBackend seam for completeness; the provider's MigrateVM updates the
// model and may call this.
func (b *liveBackend) migrate(ref, targetHost string) error {
	opts := xmlrpcStruct(map[string]xmlrpcValue{"live": xmlrpcString("true")})
	if _, err := b.callSession("VM.pool_migrate", xmlrpcString(ref), xmlrpcString(targetHost), opts); err != nil {
		return mapXapiErr(err)
	}
	return nil
}

// --- VM export (XVA over the XAPI HTTP handler) ---
//
// XenServer / XCP-ng do NOT export a VM via XML-RPC. Instead XAPI exposes a plain
// HTTP handler on the SAME host as the XML-RPC endpoint:
//
//	GET https://<master>/export?session_id=<sid>&uuid=<vm-uuid>
//	  (equivalently ...&ref=<vm-opaque-ref>)
//
// authenticated by an EXISTING XML-RPC session (the same session.login_with_password
// ref this backend already holds). The response body is the VM's XVA archive — a tar
// stream of the VM metadata + VHD disk images — streamed as it is produced. The XVA is
// what `xe vm-export` writes and what the import handler (/import) consumes, so this is
// the canonical V2V / backup artifact.
//
// exportStream builds that URL from the cached session + the VM uuid, issues the
// authenticated GET (honoring the same insecure-TLS http.Client as every other call),
// and returns the live response body as an io.ReadCloser the caller streams to disk —
// NEVER buffering it in memory. On any non-200 (401/403 = session refused, 404 =
// unknown VM, 500 = XAPI export error) it drains+closes the body and returns a CLEAR
// error carrying the HTTP status, never a placeholder. Content-Length (usually absent
// for a chunked XVA stream) is returned so the caller can show size when known.
func (b *liveBackend) exportStream(ctx context.Context, vmUUID string) (io.ReadCloser, int64, error) {
	b.mu.RLock()
	sess := b.session
	b.mu.RUnlock()
	if sess == "" {
		return nil, 0, fmt.Errorf("xen: export: no active XAPI session")
	}
	if strings.TrimSpace(vmUUID) == "" {
		return nil, 0, fmt.Errorf("xen: export: empty VM uuid")
	}
	// b.url is "<endpoint>/" (the XML-RPC path); the export handler lives on the same
	// host. Derive the base and hang /export off it.
	base := strings.TrimRight(b.url, "/")
	exportURL := base + "/export?session_id=" + url.QueryEscape(sess) +
		"&uuid=" + url.QueryEscape(vmUUID)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, exportURL, nil)
	if err != nil {
		return nil, 0, err
	}
	// XVA is an opaque binary tar stream.
	req.Header.Set("Accept", "application/octet-stream")

	resp, err := b.client.Do(req)
	if err != nil {
		b.fail(err)
		return nil, 0, fmt.Errorf("xen: export GET: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		// Read a small slice of the error body for diagnostics, then close.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		resp.Body.Close()
		msg := strings.TrimSpace(string(snippet))
		switch resp.StatusCode {
		case http.StatusUnauthorized, http.StatusForbidden:
			return nil, 0, fmt.Errorf("xen: export refused: HTTP %d (session rejected): %s",
				resp.StatusCode, msg)
		case http.StatusNotFound:
			return nil, 0, fmt.Errorf("xen: export: VM not found: HTTP %d: %s", resp.StatusCode, msg)
		default:
			return nil, 0, fmt.Errorf("xen: export failed: HTTP %d: %s", resp.StatusCode, msg)
		}
	}
	size := resp.ContentLength // -1 when the XVA is chunked (the common case)
	return resp.Body, size, nil
}

// --- XML-RPC transport ---

// call POSTs an XML-RPC methodCall to the XAPI endpoint and returns the decoded XAPI
// response value (the inner value of the {Status,Value} struct), or an *xapiFault.
func (b *liveBackend) call(method string, params ...xmlrpcValue) (*xmlrpcValue, error) {
	body, err := encodeMethodCall(method, params)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, b.url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xen: XAPI HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return decodeMethodResponse(raw)
}

var _ xapiBackend = (*liveBackend)(nil)
