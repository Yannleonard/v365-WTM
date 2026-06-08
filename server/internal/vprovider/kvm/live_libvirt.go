// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// live_libvirt.go is the REAL libvirt backend. It is PURE Go (it speaks the
// libvirt RPC wire protocol over a socket via github.com/digitalocean/go-libvirt,
// no cgo) so it compiles everywhere and carries NO build tag — the distroless,
// CGO_ENABLED=0 Linux image (D-005 / D-007) stays intact. It satisfies the
// existing libvirtBackend seam (kvm.go) so the pure-Go normalization core in
// kvm.go / libvirt.go is reused verbatim against a real libvirtd.
//
// Official libvirt API surface used (via go-libvirt, 1:1 with libvirt's C API):
//   connection : New + Connect / Disconnect, ConnectGetLibVersion
//   host       : ConnectGetHostname, NodeGetInfo
//   inventory  : ConnectListAllDomains, DomainGetState, DomainGetInfo,
//                DomainGetXMLDesc (parsed for vCPU/mem/disks/NICs/firmware),
//                ConnectListAllStoragePools + StoragePoolGetInfo +
//                StoragePoolGetXMLDesc, ConnectListAllNetworks + NetworkGetXMLDesc
//   lifecycle  : DomainCreate, DomainShutdown/DomainDestroy, DomainReset,
//                DomainSuspend, DomainResume, DomainDefineXML, DomainUndefineFlags,
//                DomainSetVcpusFlags, DomainSetMemoryFlags
//   snapshots  : DomainSnapshotCreateXML, DomainListAllSnapshots,
//                DomainRevertToSnapshot
//   migrate    : DomainMigratePerform3Params (URI-based live/offline migration)
//
// The libvirtBackend seam methods are synchronous and DO NOT return errors (they
// mirror an in-memory model). The live backend therefore performs the RPC eagerly,
// records the last transport error, and flips healthy()→false on a hard transport
// failure so HealthCheck surfaces it. Read methods refresh a cached snapshot of
// libvirt objects keyed by UUID so getDomain/getNode resolve native handles for
// the write path.
package kvm

import (
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// liveBackend is the real, pure-Go libvirt RPC backend.
type liveBackend struct {
	endpoint string

	mu      sync.RWMutex
	l       *libvirt.Libvirt
	conn    net.Conn
	ver     string
	nodeID  string // logical host id == hostname (KVM has no native host id)
	healthOK bool
	lastErr error

	// handle cache: UUID(string) -> libvirt native handle, refreshed on list.
	domHandles  map[string]libvirt.Domain
	poolHandles map[string]libvirt.StoragePool
	netHandles  map[string]libvirt.Network
}

// NewLive constructs a Provider backed by a REAL libvirt connection at endpoint.
// endpoint is a unix socket path (default /var/run/libvirt/libvirt-sock when
// empty) or a "host:port" / "tcp://host:port" for libvirt's TCP transport.
func NewLive(endpoint string) (*Provider, error) {
	return NewLiveWithID("kvm-live", endpoint)
}

// NewLiveWithID is like NewLive but uses the given provider id, so the connection-
// management layer can key the registry by the connection id (multiple libvirt
// endpoints then coexist without colliding on a fixed id).
func NewLiveWithID(id, endpoint string) (*Provider, error) {
	be, err := newLiveBackend(endpoint)
	if err != nil {
		return nil, err
	}
	// The live backend additionally services the extension contract (console,
	// network write, storage/ISO write) against the real libvirt API, so the
	// live provider advertises those capability bits on top of FullCaps. The
	// sim-backed kvm.New keeps plain FullCaps (no extBackend behind it).
	return New(id, WithBackend(be), WithCaps(LiveCaps)), nil
}

// LiveCaps is FullCaps plus the extension capability bits the REAL libvirt backend
// fulfils: graphical console (<graphics> in domain XML), virtual-network write
// (NetworkDefineXML/Create/Destroy/Undefine) and storage/ISO write (StorageVol*).
const LiveCaps = FullCaps | vp.CapConsole | vp.CapNetworkWrite | vp.CapStorageWrite

// newLiveBackend dials libvirt and runs the RPC handshake (the official
// New(conn)+Connect() sequence).
func newLiveBackend(endpoint string) (*liveBackend, error) {
	network, addr := parseEndpoint(endpoint)
	conn, err := net.DialTimeout(network, addr, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("kvm: dial libvirt %s://%s: %w", network, addr, err)
	}
	l := libvirt.New(conn)
	if err := l.Connect(); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("kvm: libvirt Connect handshake: %w", err)
	}
	be := &liveBackend{
		endpoint:    endpoint,
		l:           l,
		conn:        conn,
		healthOK:    true,
		domHandles:  map[string]libvirt.Domain{},
		poolHandles: map[string]libvirt.StoragePool{},
		netHandles:  map[string]libvirt.Network{},
	}
	// Cache version + hostname (the logical node id).
	if v, err := l.ConnectGetLibVersion(); err == nil {
		be.ver = "libvirt-" + formatLibVersion(v)
	}
	if hn, err := l.ConnectGetHostname(); err == nil {
		be.nodeID = hn
	}
	if be.nodeID == "" {
		be.nodeID = "kvm-host"
	}
	return be, nil
}

// endpointHost extracts the reachable host from a libvirt endpoint for use as the
// console host (so guacd dials a host it can actually reach). Returns "" for unix
// sockets / local connections (the caller then uses loopback/nodeID).
func endpointHost(endpoint string) string {
	network, addr := parseEndpoint(endpoint)
	if network != "tcp" {
		return "" // unix socket: VNC is local to the libvirt host
	}
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.TrimSpace(host)
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		return ""
	}
	return host
}

// parseEndpoint maps an endpoint string to a net.Dial (network, address) pair.
// "" or an absolute path -> unix socket; "tcp://h:p" or "h:p" -> tcp.
func parseEndpoint(endpoint string) (network, addr string) {
	endpoint = strings.TrimSpace(endpoint)
	switch {
	case endpoint == "":
		return "unix", "/var/run/libvirt/libvirt-sock"
	case strings.HasPrefix(endpoint, "unix://"):
		return "unix", strings.TrimPrefix(endpoint, "unix://")
	case strings.HasPrefix(endpoint, "tcp://"):
		return "tcp", strings.TrimPrefix(endpoint, "tcp://")
	case strings.HasPrefix(endpoint, "/"):
		return "unix", endpoint
	case strings.Contains(endpoint, ":"):
		return "tcp", endpoint
	default:
		return "unix", endpoint
	}
}

func formatLibVersion(v uint64) string {
	// libvirt encodes version as major*1000000 + minor*1000 + release.
	major := v / 1000000
	minor := (v % 1000000) / 1000
	rel := v % 1000
	return fmt.Sprintf("%d.%d.%d", major, minor, rel)
}

// fail records a transport error. A hard transport failure (connection-level)
// marks the backend unhealthy; logical libvirt errors (ErrNoDomain, etc.) do not.
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

// isTransportError reports a connection-level failure (not a logical libvirt
// API error which carries a libvirt.Error code).
func isTransportError(err error) bool {
	var le libvirt.Error
	return !errorAs(err, &le)
}

// --- connection ---

func (b *liveBackend) version() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.ver
}

func (b *liveBackend) healthy() bool {
	b.mu.RLock()
	l := b.l
	ok := b.healthOK
	b.mu.RUnlock()
	if !ok || l == nil {
		return false
	}
	// Active probe: a cheap RPC confirms the socket is still alive.
	if _, err := l.ConnectGetLibVersion(); err != nil {
		b.fail(err)
		return false
	}
	return true
}

func (b *liveBackend) close() error {
	b.mu.Lock()
	l, conn := b.l, b.conn
	b.l, b.conn = nil, nil
	b.healthOK = false
	b.mu.Unlock()
	if l != nil {
		_ = l.Disconnect()
	}
	if conn != nil {
		return conn.Close()
	}
	return nil
}

// --- inventory ---

func (b *liveBackend) listNodes() []*libvirtNode {
	b.mu.RLock()
	l := b.l
	nodeID := b.nodeID
	ver := b.ver
	b.mu.RUnlock()
	if l == nil {
		return nil
	}
	// NodeGetInfo: memory in KiB, cpus, mhz. KVM has one logical host == hostname.
	_, memKiB, cpus, mhz, _, _, _, _, err := l.NodeGetInfo()
	if err != nil {
		b.fail(err)
		return nil
	}
	return []*libvirtNode{{
		ID:       nodeID,
		Name:     nodeID,
		Online:   true,
		CPUs:     int(cpus),
		MHz:      int(mhz),
		MemoryKB: int64(memKiB),
		Version:  ver,
	}}
}

func (b *liveBackend) getNode(id string) (*libvirtNode, bool) {
	for _, n := range b.listNodes() {
		if n.ID == id {
			return n, true
		}
	}
	return nil, false
}

func (b *liveBackend) listDomains() []*libvirtDomain {
	b.mu.RLock()
	l := b.l
	nodeID := b.nodeID
	b.mu.RUnlock()
	if l == nil {
		return nil
	}
	// ConnectListAllDomains(NeedResults=1, Flags=0) -> all domains (active+inactive).
	doms, _, err := l.ConnectListAllDomains(1, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	out := make([]*libvirtDomain, 0, len(doms))
	handles := make(map[string]libvirt.Domain, len(doms))
	for _, d := range doms {
		ld := b.describeDomain(l, d, nodeID)
		if ld == nil {
			continue
		}
		handles[ld.UUID] = d
		out = append(out, ld)
	}
	b.mu.Lock()
	b.domHandles = handles
	b.mu.Unlock()
	return out
}

// describeDomain fuses DomainGetState + DomainGetInfo + DomainGetXMLDesc into the
// libvirtDomain the core normalizes.
func (b *liveBackend) describeDomain(l *libvirt.Libvirt, d libvirt.Domain, nodeID string) *libvirtDomain {
	uuid := uuidString(d.UUID)
	state, _, err := l.DomainGetState(d, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	_, maxMem, _, nVCPU, _, err := l.DomainGetInfo(d)
	if err != nil {
		b.fail(err)
	}
	ld := &libvirtDomain{
		UUID:     uuid,
		Name:     d.Name,
		State:    libvirtState(state),
		HostID:   nodeID,
		VCPUs:    int(nVCPU),
		MemoryKB: int64(maxMem),
	}
	// DomainGetXMLDesc gives vCPU/mem/disks/NICs/firmware authoritatively.
	if xmlDesc, err := l.DomainGetXMLDesc(d, 0); err == nil {
		applyDomainXML(ld, xmlDesc)
	} else {
		b.fail(err)
	}
	return ld
}

func (b *liveBackend) getDomain(uuid string) (*libvirtDomain, bool) {
	for _, d := range b.listDomains() {
		if d.UUID == uuid {
			return d, true
		}
	}
	return nil, false
}

func (b *liveBackend) listPools() []*libvirtPool {
	b.mu.RLock()
	l := b.l
	nodeID := b.nodeID
	b.mu.RUnlock()
	if l == nil {
		return nil
	}
	pools, _, err := l.ConnectListAllStoragePools(1, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	out := make([]*libvirtPool, 0, len(pools))
	handles := make(map[string]libvirt.StoragePool, len(pools))
	for _, p := range pools {
		uuid := uuidString(p.UUID)
		st, cap, _, avail, err := l.StoragePoolGetInfo(p)
		if err != nil {
			b.fail(err)
			continue
		}
		ptype := ""
		if xmlDesc, err := l.StoragePoolGetXMLDesc(p, 0); err == nil {
			ptype = poolTypeFromXML(xmlDesc)
		}
		handles[uuid] = p
		out = append(out, &libvirtPool{
			UUID:       uuid,
			Name:       p.Name,
			Type:       ptype,
			CapBytes:   int64(cap),
			AvailBytes: int64(avail),
			Active:     libvirt.StoragePoolState(st) == libvirt.StoragePoolRunning,
			Hosts:      []string{nodeID},
		})
	}
	b.mu.Lock()
	b.poolHandles = handles
	b.mu.Unlock()
	return out
}

func (b *liveBackend) listNets() []*libvirtNet {
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return nil
	}
	nets, _, err := l.ConnectListAllNetworks(1, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	out := make([]*libvirtNet, 0, len(nets))
	handles := make(map[string]libvirt.Network, len(nets))
	for _, n := range nets {
		uuid := uuidString(n.UUID)
		mode := ""
		if xmlDesc, err := l.NetworkGetXMLDesc(n, 0); err == nil {
			mode = netModeFromXML(xmlDesc)
		}
		handles[uuid] = n
		out = append(out, &libvirtNet{UUID: uuid, Name: n.Name, Mode: mode})
	}
	b.mu.Lock()
	b.netHandles = handles
	b.mu.Unlock()
	return out
}

// --- lifecycle ---

// defineDomain defines (and optionally starts) a domain from a libvirtDomain. The
// core builds the struct from a VMSpec; here we render it to libvirt domain XML and
// call DomainDefineXML (persistent define) then DomainCreate (start) if requested.
func (b *liveBackend) defineDomain(d *libvirtDomain) {
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return
	}
	xmlDesc := renderDomainXML(d)
	dom, err := l.DomainDefineXML(xmlDesc)
	if err != nil {
		b.fail(err)
		return
	}
	// Reflect the real libvirt-assigned UUID back into the model.
	d.UUID = uuidString(dom.UUID)
	b.mu.Lock()
	b.domHandles[d.UUID] = dom
	b.mu.Unlock()
	if d.State == domRunning {
		if err := l.DomainCreate(dom); err != nil {
			b.fail(err)
		}
	}
}

func (b *liveBackend) undefineDomain(uuid string) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return
	}
	// Best-effort destroy if running, then undefine (also drop NVRAM/snapshots).
	_ = l.DomainDestroy(dom)
	flags := libvirt.DomainUndefineManagedSave |
		libvirt.DomainUndefineSnapshotsMetadata |
		libvirt.DomainUndefineNvram
	if err := l.DomainUndefineFlags(dom, flags); err != nil {
		b.fail(err)
	}
	b.mu.Lock()
	delete(b.domHandles, uuid)
	b.mu.Unlock()
}

func (b *liveBackend) setDomainState(uuid string, s libvirtState) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return
	}
	var err error
	switch s {
	case domRunning:
		// Resume a paused/pmsuspended domain; otherwise start it. Reset handled by
		// the provider's PowerReset which also routes here as domRunning, so prefer
		// a graceful path: try resume, fall back to create.
		cur, _, sErr := l.DomainGetState(dom, 0)
		if sErr == nil && libvirtState(cur) == domPaused {
			err = l.DomainResume(dom)
		} else if sErr == nil && (libvirtState(cur) == domRunning || libvirtState(cur) == domBlocked) {
			err = l.DomainReset(dom, 0) // already running -> treat as reset
		} else {
			err = l.DomainCreate(dom)
		}
	case domShutoff:
		// Graceful shutdown; if that is unsupported the caller's force path will
		// have already destroyed it.
		if err = l.DomainShutdown(dom); err != nil {
			err = l.DomainDestroy(dom)
		}
	case domPMSuspended, domPaused:
		err = l.DomainSuspend(dom)
	}
	if err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) domainsOnHost(hostID string) int {
	n := 0
	for _, d := range b.listDomains() {
		if d.HostID == hostID {
			n++
		}
	}
	return n
}

// --- snapshots ---

func (b *liveBackend) listSnapshots(uuid string) []vp.Snapshot {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return nil
	}
	snaps, _, err := l.DomainListAllSnapshots(dom, 1, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	out := make([]vp.Snapshot, 0, len(snaps))
	for _, s := range snaps {
		out = append(out, vp.Snapshot{
			ID:   s.Name,
			VMID: uuid,
			Name: s.Name,
		})
	}
	return out
}

func (b *liveBackend) createSnapshot(uuid string, snap vp.Snapshot) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return
	}
	xmlDesc := renderSnapshotXML(snap)
	if _, err := l.DomainSnapshotCreateXML(dom, xmlDesc, 0); err != nil {
		b.fail(err)
	}
}

func (b *liveBackend) setCurrentSnapshot(uuid, snapID string) bool {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return false
	}
	snap := libvirt.DomainSnapshot{Name: snapID, Dom: dom}
	if err := l.DomainRevertToSnapshot(snap, 0); err != nil {
		b.fail(err)
		return false
	}
	return true
}

// --- host/cluster identity ---

func (b *liveBackend) hostIDs() []string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.nodeID == "" {
		return nil
	}
	return []string{b.nodeID}
}

func (b *liveBackend) clusterName() string {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.nodeID != "" {
		return "kvm-" + b.nodeID
	}
	return "kvm-live"
}

// migrate performs a real libvirt migration to a destination URI using the
// official params-based migrate (DomainMigratePerform3Params). Exposed beyond the
// libvirtBackend seam for completeness; the provider's MigrateVM uses the model.
func (b *liveBackend) migrate(uuid, destURI string, live bool) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return vp.ErrNotFound
	}
	var flags libvirt.DomainMigrateFlags = libvirt.MigratePersistDest | libvirt.MigrateUndefineSource
	if live {
		flags |= libvirt.MigrateLive
	}
	params := []libvirt.TypedParam{
		{Field: "destination_uri", Value: *libvirt.NewTypedParamValueString(destURI)},
	}
	_, err := l.DomainMigratePerform3Params(dom, libvirt.OptString{destURI}, params, nil, flags)
	return mapLibvirtErr(err)
}

// --- helpers ---

// domainHandle resolves a cached native domain handle by UUID, refreshing the
// cache via listDomains if necessary.
func (b *liveBackend) domainHandle(uuid string) (*libvirt.Libvirt, libvirt.Domain, bool) {
	b.mu.RLock()
	l := b.l
	dom, ok := b.domHandles[uuid]
	b.mu.RUnlock()
	if l == nil {
		return nil, libvirt.Domain{}, false
	}
	if ok {
		return l, dom, true
	}
	b.listDomains() // refresh cache
	b.mu.RLock()
	dom, ok = b.domHandles[uuid]
	b.mu.RUnlock()
	return l, dom, ok
}

// uuidString formats a libvirt 16-byte UUID as the canonical 8-4-4-4-12 string.
func uuidString(u libvirt.UUID) string {
	return fmt.Sprintf("%x-%x-%x-%x-%x", u[0:4], u[4:6], u[6:8], u[8:10], u[10:16])
}

// errNoConn signals the live backend has no active libvirt connection (closed or
// never dialed). It surfaces as a transport-level failure to the caller.
var errNoConn = fmt.Errorf("kvm: libvirt connection unavailable")

// mapLibvirtErr maps libvirt RPC error codes to the contract sentinels.
func mapLibvirtErr(err error) error {
	if err == nil {
		return nil
	}
	var le libvirt.Error
	if !errorAs(err, &le) {
		return err
	}
	switch libvirt.ErrorNumber(le.Code) {
	case libvirt.ErrNoDomain, libvirt.ErrNoNetwork, libvirt.ErrNoStoragePool,
		libvirt.ErrNoStorageVol, libvirt.ErrNoDomainSnapshot:
		return vp.ErrNotFound
	case libvirt.ErrOperationInvalid:
		return vp.ErrConflict
	case libvirt.ErrInvalidArg, libvirt.ErrXMLError, libvirt.ErrXMLDetail:
		return vp.ErrInvalidSpec
	default:
		return err
	}
}

// errorAs is errors.As specialized to avoid importing errors twice across files.
func errorAs(err error, target *libvirt.Error) bool {
	for err != nil {
		if e, ok := err.(libvirt.Error); ok {
			*target = e
			return true
		}
		type unwrapper interface{ Unwrap() error }
		u, ok := err.(unwrapper)
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

var _ libvirtBackend = (*liveBackend)(nil)
var _ extBackend = (*liveBackend)(nil)

// =============================================================================
// extension surface (console / network write / storage write) over the REAL
// libvirt API. These are the methods behind the extBackend seam (kvm.go).
// =============================================================================

// --- console: read <graphics> from DomainGetXMLDesc ---

// graphicsXML is the subset of <domain><devices><graphics> we read.
type graphicsXML struct {
	Devices struct {
		Graphics []struct {
			Type     string `xml:"type,attr"`     // vnc|spice
			Port     string `xml:"port,attr"`     // numeric, or -1 if autoport not yet assigned
			TLSPort  string `xml:"tlsPort,attr"`  // spice TLS port
			Listen   string `xml:"listen,attr"`   // legacy listen addr attr
			Passwd   string `xml:"passwd,attr"`   // console password, if set
			ListenEl []struct {
				Type    string `xml:"type,attr"`
				Address string `xml:"address,attr"`
			} `xml:"listen"`
		} `xml:"graphics"`
	} `xml:"devices"`
}

// console resolves a domain's graphical console endpoint from its live domain XML
// <graphics> element (the official libvirt console-exposure mechanism). It prefers
// VNC, then SPICE.
func (b *liveBackend) console(uuid string) (*vp.ConsoleEndpoint, error) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return nil, vp.ErrNotFound
	}
	// VIR_DOMAIN_XML_SECURE surfaces the (otherwise redacted) <graphics passwd>
	// console ticket, which is the one-shot Password the contract returns.
	raw, err := l.DomainGetXMLDesc(dom, libvirt.DomainXMLSecure)
	if err != nil {
		// Fall back to the non-secure dump if the connection is unprivileged.
		raw, err = l.DomainGetXMLDesc(dom, 0)
		if err != nil {
			b.fail(err)
			return nil, mapLibvirtErr(err)
		}
	}
	var gx graphicsXML
	if err := xml.Unmarshal([]byte(raw), &gx); err != nil {
		return nil, vp.ErrUnsupported
	}
	if len(gx.Devices.Graphics) == 0 {
		// No <graphics> device -> no graphical console on this domain.
		return nil, vp.ErrUnsupported
	}
	// Prefer a VNC graphics device, else fall back to the first one.
	g := gx.Devices.Graphics[0]
	for _, cand := range gx.Devices.Graphics {
		if strings.EqualFold(cand.Type, "vnc") {
			g = cand
			break
		}
	}
	kind := vp.ConsoleVNC
	if strings.EqualFold(g.Type, "spice") {
		kind = vp.ConsoleSPICE
	}
	host := g.Listen
	if host == "" {
		for _, le := range g.ListenEl {
			if le.Address != "" {
				host = le.Address
				break
			}
		}
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		// libvirt listens on all interfaces. Surface a host that is actually
		// REACHABLE by the console bridge (guacd). The libvirt RPC endpoint we
		// connected on is the right answer for a TCP endpoint (e.g.
		// host.docker.internal) because guacd shares that network view; libvirt's
		// internal hostname (b.nodeID) is often NOT resolvable from the bridge
		// container. Fall back to nodeID, then loopback.
		host = endpointHost(b.endpoint)
		if host == "" {
			b.mu.RLock()
			host = b.nodeID
			b.mu.RUnlock()
		}
		if host == "" {
			host = "127.0.0.1"
		}
	}
	ep := &vp.ConsoleEndpoint{
		Kind:     kind,
		Host:     host,
		Port:     atoiSafe(g.Port),
		TLSPort:  atoiSafe(g.TLSPort),
		Password: g.Passwd,
	}
	return ep, nil
}

// --- network write ---

func (b *liveBackend) createNetwork(spec vp.NetworkSpec) error {
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return errNoConn
	}
	xmlDesc := renderNetworkXML(spec)
	net, err := l.NetworkDefineXML(xmlDesc)
	if err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	if err := l.NetworkCreate(net); err != nil {
		// roll back the persistent definition so a failed start leaves no orphan.
		_ = l.NetworkUndefine(net)
		b.fail(err)
		return mapLibvirtErr(err)
	}
	if err := l.NetworkSetAutostart(net, 1); err != nil {
		b.fail(err)
		// autostart is best-effort; the network is up, do not fail the op.
	}
	b.mu.Lock()
	b.netHandles[uuidString(net.UUID)] = net
	b.mu.Unlock()
	return nil
}

func (b *liveBackend) deleteNetwork(id string) error {
	net, err := b.networkHandle(id)
	if err != nil {
		return err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return errNoConn
	}
	// Destroy (stop) if active, then undefine (remove persistent config).
	_ = l.NetworkDestroy(net)
	if err := l.NetworkUndefine(net); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	b.mu.Lock()
	delete(b.netHandles, uuidString(net.UUID))
	b.mu.Unlock()
	return nil
}

// networkHandle resolves a network by UUID string or by name.
func (b *liveBackend) networkHandle(id string) (libvirt.Network, error) {
	b.mu.RLock()
	l := b.l
	net, ok := b.netHandles[id]
	b.mu.RUnlock()
	if l == nil {
		return libvirt.Network{}, errNoConn
	}
	if ok {
		return net, nil
	}
	// Try UUID, then name.
	if u, perr := parseUUID(id); perr == nil {
		if n, err := l.NetworkLookupByUUID(u); err == nil {
			return n, nil
		}
	}
	n, err := l.NetworkLookupByName(id)
	if err != nil {
		b.fail(err)
		return libvirt.Network{}, mapLibvirtErr(err)
	}
	return n, nil
}

// --- storage write ---

func (b *liveBackend) poolHandle(id string) (libvirt.StoragePool, error) {
	b.mu.RLock()
	l := b.l
	pool, ok := b.poolHandles[id]
	b.mu.RUnlock()
	if l == nil {
		return libvirt.StoragePool{}, errNoConn
	}
	if ok {
		return pool, nil
	}
	if u, perr := parseUUID(id); perr == nil {
		if p, err := l.StoragePoolLookupByUUID(u); err == nil {
			return p, nil
		}
	}
	p, err := l.StoragePoolLookupByName(id)
	if err != nil {
		b.fail(err)
		return libvirt.StoragePool{}, mapLibvirtErr(err)
	}
	return p, nil
}

func (b *liveBackend) listVolumes(storageID string) ([]vp.Volume, error) {
	pool, err := b.poolHandle(storageID)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return nil, errNoConn
	}
	vols, _, err := l.StoragePoolListAllVolumes(pool, 1, 0)
	if err != nil {
		b.fail(err)
		return nil, mapLibvirtErr(err)
	}
	out := make([]vp.Volume, 0, len(vols))
	for _, v := range vols {
		_, capBytes, allocBytes, ierr := l.StorageVolGetInfo(v)
		if ierr != nil {
			b.fail(ierr)
		}
		format := vp.DiskQcow2
		path := v.Key
		if x, xerr := l.StorageVolGetXMLDesc(v, 0); xerr == nil {
			if f := volFormatFromXML(x); f != "" {
				format = normalizeDiskFormat(f)
			}
			if pth := volPathFromXML(x); pth != "" {
				path = pth
			}
		}
		out = append(out, vp.Volume{
			ID:         v.Key,
			Name:       v.Name,
			StorageID:  storageID,
			Format:     format,
			CapacityGB: float64(capBytes) / bytesPerGB,
			AllocGB:    float64(allocBytes) / bytesPerGB,
			IsISO:      isISOName(v.Name),
			Path:       path,
		})
	}
	return out, nil
}

func (b *liveBackend) createVolume(spec vp.VolumeSpec) error {
	pool, err := b.poolHandle(spec.StorageID)
	if err != nil {
		return err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return errNoConn
	}
	format := "qcow2"
	if spec.Format == vp.DiskRaw {
		format = "raw"
	}
	sizeBytes := uint64(spec.CapacityGB * bytesPerGB)
	xmlDesc := renderVolumeXML(spec.Name, format, sizeBytes)
	if _, err := l.StorageVolCreateXML(pool, xmlDesc, 0); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

func (b *liveBackend) deleteVolume(storageID, volumeID string) error {
	vol, err := b.volHandle(storageID, volumeID)
	if err != nil {
		return err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return errNoConn
	}
	if err := l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// volHandle resolves a StorageVol by name within a pool. volumeID is matched
// against the vol Key (path) first, then its Name.
func (b *liveBackend) volHandle(storageID, volumeID string) (libvirt.StorageVol, error) {
	pool, err := b.poolHandle(storageID)
	if err != nil {
		return libvirt.StorageVol{}, err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return libvirt.StorageVol{}, errNoConn
	}
	// Direct name lookup is the fast path.
	if v, err := l.StorageVolLookupByName(pool, volumeID); err == nil {
		return v, nil
	}
	// Otherwise match by Key (libvirt's volume id is the path) across the pool.
	vols, _, lerr := l.StoragePoolListAllVolumes(pool, 1, 0)
	if lerr != nil {
		b.fail(lerr)
		return libvirt.StorageVol{}, mapLibvirtErr(lerr)
	}
	for _, v := range vols {
		if v.Key == volumeID || v.Name == volumeID {
			return v, nil
		}
	}
	return libvirt.StorageVol{}, vp.ErrNotFound
}

// uploadISO creates a raw volume of the given size and streams the ISO bytes into
// it via the official libvirt vol-upload stream API (StorageVolUpload, which takes
// an io.Reader and pumps it over a libvirt Stream).
func (b *liveBackend) uploadISO(storageID, name string, size int64, r io.Reader) (*vp.Volume, error) {
	pool, err := b.poolHandle(storageID)
	if err != nil {
		return nil, err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return nil, errNoConn
	}
	if size <= 0 {
		return nil, vp.ErrInvalidSpec
	}
	xmlDesc := renderVolumeXML(name, "raw", uint64(size))
	vol, err := l.StorageVolCreateXML(pool, xmlDesc, 0)
	if err != nil {
		b.fail(err)
		return nil, mapLibvirtErr(err)
	}
	// Official streaming upload: go-libvirt's StorageVolUpload reads from r and
	// drives the libvirt Stream protocol (virStreamSend) under the hood.
	if err := l.StorageVolUpload(vol, r, 0, uint64(size), 0); err != nil {
		b.fail(err)
		_ = l.StorageVolDelete(vol, libvirt.StorageVolDeleteNormal)
		return nil, mapLibvirtErr(err)
	}
	gb := float64(size) / bytesPerGB
	return &vp.Volume{
		ID:         vol.Key,
		Name:       vol.Name,
		StorageID:  storageID,
		Format:     vp.DiskRaw,
		CapacityGB: gb,
		AllocGB:    gb,
		IsISO:      true,
		Path:       vol.Key,
	}, nil
}

// --- extension XML rendering / parsing helpers ---

// renderNetworkXML builds libvirt <network> XML from a NetworkSpec. Supports nat
// (with optional managed IP range + DHCP), bridge (host bridge passthrough), and
// isolated networks.
func renderNetworkXML(spec vp.NetworkSpec) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<network>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEscape(spec.Name))
	switch strings.ToLower(spec.Type) {
	case "bridge":
		// Bridged to an existing host bridge: forward mode='bridge', no managed IP.
		fmt.Fprintf(&sb, "  <forward mode='bridge'/>\n")
		if spec.Bridge != "" {
			fmt.Fprintf(&sb, "  <bridge name='%s'/>\n", xmlEscape(spec.Bridge))
		}
	case "isolated", "":
		// No <forward>: a host-private isolated network.
		bridge := spec.Bridge
		if bridge == "" {
			bridge = "virbr-" + sanitizeBridge(spec.Name)
		}
		fmt.Fprintf(&sb, "  <bridge name='%s' stp='on' delay='0'/>\n", xmlEscape(bridge))
		appendNetworkIP(&sb, spec.CIDR)
	default: // nat / route / managed
		mode := strings.ToLower(spec.Type)
		if mode != "route" {
			mode = "nat"
		}
		fmt.Fprintf(&sb, "  <forward mode='%s'/>\n", mode)
		bridge := spec.Bridge
		if bridge == "" {
			bridge = "virbr-" + sanitizeBridge(spec.Name)
		}
		fmt.Fprintf(&sb, "  <bridge name='%s' stp='on' delay='0'/>\n", xmlEscape(bridge))
		appendNetworkIP(&sb, spec.CIDR)
	}
	fmt.Fprintf(&sb, "</network>\n")
	return sb.String()
}

// appendNetworkIP renders an <ip>/<dhcp> block for a managed network if a CIDR is
// given (e.g. 192.168.50.0/24 -> address 192.168.50.1, /24, DHCP .2-.254).
func appendNetworkIP(sb *strings.Builder, cidr string) {
	if cidr == "" {
		return
	}
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return
	}
	ip4 := ipnet.IP.To4()
	if ip4 == nil {
		return
	}
	_ = ip
	prefix, _ := ipnet.Mask.Size()
	gw := make(net.IP, len(ip4))
	copy(gw, ip4)
	gw[3]++ // .1 as the gateway
	dhcpStart := make(net.IP, len(ip4))
	copy(dhcpStart, ip4)
	dhcpStart[3] += 2
	dhcpEnd := make(net.IP, len(ip4))
	copy(dhcpEnd, ip4)
	dhcpEnd[3] = 254
	fmt.Fprintf(sb, "  <ip address='%s' prefix='%d'>\n", gw.String(), prefix)
	fmt.Fprintf(sb, "    <dhcp>\n")
	fmt.Fprintf(sb, "      <range start='%s' end='%s'/>\n", dhcpStart.String(), dhcpEnd.String())
	fmt.Fprintf(sb, "    </dhcp>\n")
	fmt.Fprintf(sb, "  </ip>\n")
}

// renderVolumeXML builds a libvirt <volume> for StorageVolCreateXML.
func renderVolumeXML(name, format string, sizeBytes uint64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<volume>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEscape(name))
	fmt.Fprintf(&sb, "  <capacity unit='bytes'>%d</capacity>\n", sizeBytes)
	fmt.Fprintf(&sb, "  <allocation unit='bytes'>0</allocation>\n")
	fmt.Fprintf(&sb, "  <target>\n")
	fmt.Fprintf(&sb, "    <format type='%s'/>\n", xmlEscape(format))
	fmt.Fprintf(&sb, "  </target>\n")
	fmt.Fprintf(&sb, "</volume>\n")
	return sb.String()
}

// volFormatFromXML reads <volume><target><format type='...'>.
func volFormatFromXML(raw string) string {
	var vx struct {
		Target struct {
			Format struct {
				Type string `xml:"type,attr"`
			} `xml:"format"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(raw), &vx); err != nil {
		return ""
	}
	return vx.Target.Format.Type
}

// volPathFromXML reads <volume><target><path>.
func volPathFromXML(raw string) string {
	var vx struct {
		Target struct {
			Path string `xml:"path"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(raw), &vx); err != nil {
		return ""
	}
	return vx.Target.Path
}

// isISOName reports whether a volume name/path looks like an ISO image.
func isISOName(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".iso")
}

// sanitizeBridge derives a short, valid bridge-name fragment from a network name.
func sanitizeBridge(name string) string {
	var sb strings.Builder
	for _, r := range name {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			sb.WriteRune(r)
		}
		if sb.Len() >= 8 {
			break
		}
	}
	if sb.Len() == 0 {
		return "net0"
	}
	return sb.String()
}

// atoiSafe parses a base-10 int, returning 0 on any error or for libvirt's "-1"
// (autoport-not-yet-assigned) sentinel.
func atoiSafe(s string) int {
	s = strings.TrimSpace(s)
	if s == "" || s == "-1" {
		return 0
	}
	n := 0
	neg := false
	for i, r := range s {
		if i == 0 && r == '-' {
			neg = true
			continue
		}
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if neg {
		return 0
	}
	return n
}

// parseUUID parses a canonical 8-4-4-4-12 UUID string into a libvirt.UUID.
func parseUUID(s string) (libvirt.UUID, error) {
	var u libvirt.UUID
	hex := strings.ReplaceAll(s, "-", "")
	if len(hex) != 32 {
		return u, fmt.Errorf("kvm: bad uuid %q", s)
	}
	for i := 0; i < 16; i++ {
		hi, err1 := hexVal(hex[i*2])
		lo, err2 := hexVal(hex[i*2+1])
		if err1 != nil || err2 != nil {
			return u, fmt.Errorf("kvm: bad uuid %q", s)
		}
		u[i] = hi<<4 | lo
	}
	return u, nil
}

func hexVal(c byte) (byte, error) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', nil
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, nil
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, nil
	default:
		return 0, fmt.Errorf("bad hex %q", c)
	}
}

// --- domain XML parsing/rendering (official libvirt domain XML format) ---

// domainXML is the subset of libvirt's <domain> XML we read.
type domainXML struct {
	XMLName xml.Name `xml:"domain"`
	Name    string   `xml:"name"`
	UUID    string   `xml:"uuid"`
	VCPU    int      `xml:"vcpu"`
	Memory  struct {
		Unit  string `xml:"unit,attr"`
		Value int64  `xml:",chardata"`
	} `xml:"memory"`
	OS struct {
		Type struct {
			Arch    string `xml:"arch,attr"`
			Machine string `xml:"machine,attr"`
			Value   string `xml:",chardata"`
		} `xml:"type"`
		Loader struct {
			Type     string `xml:"type,attr"`
			Readonly string `xml:"readonly,attr"`
			Value    string `xml:",chardata"`
		} `xml:"loader"`
		Firmware string `xml:"firmware,attr"`
	} `xml:"os"`
	Devices struct {
		Disks []struct {
			Device string `xml:"device,attr"`
			Driver struct {
				Type string `xml:"type,attr"`
			} `xml:"driver"`
			Source struct {
				File string `xml:"file,attr"`
				Dev  string `xml:"dev,attr"`
				Pool string `xml:"pool,attr"`
			} `xml:"source"`
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Interfaces []struct {
			Type string `xml:"type,attr"`
			MAC  struct {
				Address string `xml:"address,attr"`
			} `xml:"mac"`
			Source struct {
				Network string `xml:"network,attr"`
				Bridge  string `xml:"bridge,attr"`
			} `xml:"source"`
			Model struct {
				Type string `xml:"type,attr"`
			} `xml:"model"`
			Link struct {
				State string `xml:"state,attr"`
			} `xml:"link"`
		} `xml:"interface"`
	} `xml:"devices"`
}

// applyDomainXML enriches a libvirtDomain from its <domain> XML.
func applyDomainXML(d *libvirtDomain, raw string) {
	var dx domainXML
	if err := xml.Unmarshal([]byte(raw), &dx); err != nil {
		return
	}
	if dx.VCPU > 0 {
		d.VCPUs = dx.VCPU
	}
	if dx.Memory.Value > 0 {
		d.MemoryKB = toKiB(dx.Memory.Value, dx.Memory.Unit)
	}
	if dx.OS.Type.Value != "" {
		d.OSType = dx.OS.Type.Value
	}
	// Firmware: explicit firmware="efi" attr, or a loader/OVMF path => UEFI.
	if strings.EqualFold(dx.OS.Firmware, "efi") ||
		strings.Contains(strings.ToLower(dx.OS.Loader.Value), "ovmf") ||
		strings.Contains(strings.ToLower(dx.OS.Loader.Value), "efi") {
		d.Firmware = vp.FirmwareUEFI
	} else {
		d.Firmware = vp.FirmwareBIOS
	}
	for _, dk := range dx.Devices.Disks {
		if dk.Device == "cdrom" {
			continue
		}
		src := dk.Source.File
		if src == "" {
			src = dk.Source.Dev
		}
		d.Disks = append(d.Disks, libvirtDisk{
			Target: dk.Target.Dev,
			Driver: dk.Driver.Type,
			Source: src,
			Pool:   dk.Source.Pool,
		})
	}
	for _, nic := range dx.Devices.Interfaces {
		net := nic.Source.Network
		if net == "" {
			net = nic.Source.Bridge
		}
		link := true
		if nic.Link.State == "down" {
			link = false
		}
		d.NICs = append(d.NICs, libvirtNIC{
			MAC:     nic.MAC.Address,
			Network: net,
			Model:   nic.Model.Type,
			Link:    link,
		})
	}
}

func toKiB(value int64, unit string) int64 {
	switch strings.ToLower(unit) {
	case "", "kib", "k", "kb":
		return value
	case "mib", "m", "mb":
		return value * 1024
	case "gib", "g", "gb":
		return value * 1024 * 1024
	case "bytes", "b":
		return value / 1024
	default:
		return value
	}
}

// poolTypeFromXML extracts the type attr from <pool type='...'>.
func poolTypeFromXML(raw string) string {
	var px struct {
		Type string `xml:"type,attr"`
	}
	if err := xml.Unmarshal([]byte(raw), &px); err != nil {
		return ""
	}
	return px.Type
}

// netModeFromXML extracts the forward mode from <network><forward mode='...'>.
func netModeFromXML(raw string) string {
	var nx struct {
		Forward struct {
			Mode string `xml:"mode,attr"`
		} `xml:"forward"`
	}
	if err := xml.Unmarshal([]byte(raw), &nx); err != nil {
		return ""
	}
	if nx.Forward.Mode == "" {
		return "isolated"
	}
	return nx.Forward.Mode
}

// renderDomainXML builds a minimal-but-valid libvirt domain XML for define/create.
func renderDomainXML(d *libvirtDomain) string {
	memKiB := d.MemoryKB
	if memKiB <= 0 {
		memKiB = 512 * 1024
	}
	vcpu := d.VCPUs
	if vcpu <= 0 {
		vcpu = 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<domain type='kvm'>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEscape(d.Name))
	fmt.Fprintf(&sb, "  <memory unit='KiB'>%d</memory>\n", memKiB)
	fmt.Fprintf(&sb, "  <currentMemory unit='KiB'>%d</currentMemory>\n", memKiB)
	fmt.Fprintf(&sb, "  <vcpu placement='static'>%d</vcpu>\n", vcpu)
	if d.Firmware == vp.FirmwareUEFI {
		fmt.Fprintf(&sb, "  <os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type></os>\n")
	} else {
		fmt.Fprintf(&sb, "  <os><type arch='x86_64' machine='pc'>hvm</type></os>\n")
	}
	fmt.Fprintf(&sb, "  <devices>\n")
	for _, dk := range d.Disks {
		driver := dk.Driver
		if driver == "" {
			driver = "qcow2"
		}
		target := dk.Target
		if target == "" {
			target = "vda"
		}
		if dk.Source != "" {
			fmt.Fprintf(&sb, "    <disk type='file' device='disk'>\n")
			fmt.Fprintf(&sb, "      <driver name='qemu' type='%s'/>\n", xmlEscape(driver))
			fmt.Fprintf(&sb, "      <source file='%s'/>\n", xmlEscape(dk.Source))
			fmt.Fprintf(&sb, "      <target dev='%s' bus='virtio'/>\n", xmlEscape(target))
			fmt.Fprintf(&sb, "    </disk>\n")
		}
	}
	// Boot CD-ROM from an ISO (the wizard's bootIso maps to d.BootISO). Placed
	// before NICs; marked bootable so an empty disk boots the installer.
	if d.BootISO != "" {
		fmt.Fprintf(&sb, "    <disk type='file' device='cdrom'>\n")
		fmt.Fprintf(&sb, "      <driver name='qemu' type='raw'/>\n")
		fmt.Fprintf(&sb, "      <source file='%s'/>\n", xmlEscape(d.BootISO))
		fmt.Fprintf(&sb, "      <target dev='sda' bus='sata'/>\n")
		fmt.Fprintf(&sb, "      <readonly/>\n")
		fmt.Fprintf(&sb, "      <boot order='1'/>\n")
		fmt.Fprintf(&sb, "    </disk>\n")
	}
	for _, nic := range d.NICs {
		if nic.Network == "" {
			continue
		}
		model := nic.Model
		if model == "" {
			model = "virtio"
		}
		fmt.Fprintf(&sb, "    <interface type='network'>\n")
		fmt.Fprintf(&sb, "      <source network='%s'/>\n", xmlEscape(nic.Network))
		if nic.MAC != "" {
			fmt.Fprintf(&sb, "      <mac address='%s'/>\n", xmlEscape(nic.MAC))
		}
		fmt.Fprintf(&sb, "      <model type='%s'/>\n", xmlEscape(model))
		fmt.Fprintf(&sb, "    </interface>\n")
	}
	fmt.Fprintf(&sb, "    <console type='pty'/>\n")
	// Always attach a VNC graphical console + a video adapter so every VM created
	// through UniHV has a usable console (the Console feature reads this <graphics>).
	// autoport='yes' lets libvirt pick a free port; listen 0.0.0.0 so the console
	// proxy can reach it. (See ConsoleProvider.Console.)
	fmt.Fprintf(&sb, "    <graphics type='vnc' port='-1' autoport='yes' listen='0.0.0.0'/>\n")
	fmt.Fprintf(&sb, "    <video><model type='cirrus'/></video>\n")
	fmt.Fprintf(&sb, "  </devices>\n")
	fmt.Fprintf(&sb, "</domain>\n")
	return sb.String()
}

// renderSnapshotXML builds a <domainsnapshot> for DomainSnapshotCreateXML.
func renderSnapshotXML(snap vp.Snapshot) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "<domainsnapshot>\n")
	if snap.Name != "" {
		fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEscape(snap.Name))
	}
	if snap.Description != "" {
		fmt.Fprintf(&sb, "  <description>%s</description>\n", xmlEscape(snap.Description))
	}
	fmt.Fprintf(&sb, "</domainsnapshot>\n")
	return sb.String()
}

func xmlEscape(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}
