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
	"os"
	"os/exec"
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
// (NetworkDefineXML/Create/Destroy/Undefine), storage/ISO write (StorageVol*) and
// hot-plug device management (DomainAttach/Detach/UpdateDeviceFlags, LIVE|CONFIG).
const LiveCaps = FullCaps | vp.CapConsole | vp.CapNetworkWrite | vp.CapStorageWrite | vp.CapHotPlug |
	vp.CapGuestAgent | vp.CapDiskResize

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
	// Best-effort guest IPs: prefer the qemu-guest-agent, fall back to the DHCP
	// lease source, then ARP. ALL of these fail silently when the guest is off or
	// the agent is absent — they only ENRICH the VM, never block normalization.
	if libvirtState(state) == domRunning {
		ld.IPs = b.domainIPs(l, d)
	}
	return ld
}

// domainIPs returns the guest IPs (loopback filtered) via, in order:
// DomainInterfaceAddresses source=AGENT (qemu-ga), then source=LEASE (libvirt
// dnsmasq leases), then source=ARP. Returns nil silently when none answer.
func (b *liveBackend) domainIPs(l *libvirt.Libvirt, d libvirt.Domain) []string {
	for _, src := range []libvirt.DomainInterfaceAddressesSource{
		libvirt.DomainInterfaceAddressesSrcAgent,
		libvirt.DomainInterfaceAddressesSrcLease,
		libvirt.DomainInterfaceAddressesSrcArp,
	} {
		ifaces, err := l.DomainInterfaceAddresses(d, uint32(src), 0)
		if err != nil {
			continue // source unavailable (e.g. no agent) -> try the next one
		}
		ips := ipsFromInterfaces(ifaces)
		if len(ips) > 0 {
			return ips
		}
	}
	return nil
}

// ipsFromInterfaces flattens DomainInterface addresses, skipping loopback.
func ipsFromInterfaces(ifaces []libvirt.DomainInterface) []string {
	var out []string
	for _, iface := range ifaces {
		for _, a := range iface.Addrs {
			ip := strings.TrimSpace(a.Addr)
			if ip == "" || strings.HasPrefix(ip, "127.") || ip == "::1" {
				continue
			}
			out = append(out, ip)
		}
	}
	return out
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
func (b *liveBackend) defineDomain(d *libvirtDomain) error {
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return errNoConn
	}
	// Provision a backing volume for each disk that was requested by SIZE only (no
	// explicit Source path). Without this, renderDomainXML drops the disk (it needs
	// a source file) and the VM boots diskless — which then can't be snapshotted or
	// booted. We create a qcow2 volume in the disk's pool (default "default") and
	// point the disk at the real file path libvirt returns.
	for i := range d.Disks {
		dk := &d.Disks[i]
		if dk.Source != "" || dk.CapBytes <= 0 {
			continue
		}
		poolName := dk.Pool
		if poolName == "" {
			poolName = "default"
		}
		format := dk.Driver
		if format == "" {
			format = "qcow2"
		}
		volName := fmt.Sprintf("%s-%s.%s", sanitizeBridge(d.Name), dk.Target, format)
		path, err := b.provisionVolume(poolName, volName, dk.CapBytes, format)
		if err != nil {
			return err
		}
		dk.Source = path
		dk.Driver = format
	}
	// Cloud-init (NoCloud) guest customization: build the 'cidata' seed ISO and
	// attach it as an extra cdrom (renderDomainXML reads d.SeedISO). The seed lands
	// in the first disk's pool (or "default") so the qemu process can read it.
	if d.CloudInit != nil {
		poolName := "default"
		if len(d.Disks) > 0 && d.Disks[0].Pool != "" {
			poolName = d.Disks[0].Pool
		}
		seed, err := b.buildSeedISO(d.Name, poolName, d.CloudInit)
		if err != nil {
			return err
		}
		d.SeedISO = seed
	}
	// Windows sysprep (Lot 4A): build the autounattend.xml seed ISO (volid 'sysprep')
	// and attach it as an extra cdrom (renderDomainXML reads d.SysprepISO). Mirrors the
	// cloud-init seed path. Live-only (the sim backend ignores Sysprep).
	if d.Sysprep != nil {
		poolName := "default"
		if len(d.Disks) > 0 && d.Disks[0].Pool != "" {
			poolName = d.Disks[0].Pool
		}
		seed, err := b.buildSysprepISO(d.Name, poolName, d.Sysprep)
		if err != nil {
			return err
		}
		d.SysprepISO = seed
	}
	xmlDesc := renderDomainXML(d)
	dom, err := l.DomainDefineXML(xmlDesc)
	if err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	// Reflect the real libvirt-assigned UUID back into the model.
	d.UUID = uuidString(dom.UUID)
	b.mu.Lock()
	b.domHandles[d.UUID] = dom
	b.mu.Unlock()
	if d.State == domRunning {
		if err := l.DomainCreate(dom); err != nil {
			b.fail(err)
			// Roll back the persistent definition so a failed start leaves no orphan.
			_ = l.DomainUndefineFlags(dom, libvirt.DomainUndefineNvram)
			b.mu.Lock()
			delete(b.domHandles, d.UUID)
			b.mu.Unlock()
			return mapLibvirtErr(err)
		}
	}
	return nil
}

func (b *liveBackend) undefineDomain(uuid string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	// Best-effort destroy if running, then undefine (also drop NVRAM/snapshots).
	_ = l.DomainDestroy(dom)
	flags := libvirt.DomainUndefineManagedSave |
		libvirt.DomainUndefineSnapshotsMetadata |
		libvirt.DomainUndefineNvram
	if err := l.DomainUndefineFlags(dom, flags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	b.mu.Lock()
	delete(b.domHandles, uuid)
	b.mu.Unlock()
	return nil
}

func (b *liveBackend) setDomainState(uuid string, s libvirtState) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
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
		return mapLibvirtErr(err)
	}
	return nil
}

// reconfigureDomain applies a vCPU and/or memory change to the REAL domain.
//
//   - vCPUs: DomainSetVcpusFlags with VCPU_CONFIG (persistent). When the new count
//     exceeds the domain's persisted MAXIMUM, libvirt rejects it ("requested vcpus
//     is greater than max allowable"); we raise the CONFIG MAXIMUM first, then set
//     the live count. When the domain is running we also apply VCPU_LIVE so the
//     change takes effect without a reboot (best-effort; running guests cannot
//     always hot-add, in which case the CONFIG change still persists).
//   - memory: DomainSetMemoryFlags with MEM_CONFIG. Raising current memory above
//     the persisted maximum requires bumping MEM_MAXIMUM first.
//
// Any hard libvirt rejection is returned (mapped to a contract sentinel) so the
// API surfaces a 409/422 instead of a false success.
func (b *liveBackend) reconfigureDomain(uuid string, vcpus *int, memMB *int64) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	running := false
	if st, _, err := l.DomainGetState(dom, 0); err == nil {
		s := libvirtState(st)
		running = s == domRunning || s == domBlocked
	}

	if vcpus != nil {
		n := uint32(*vcpus)
		// Persisted maximum vCPUs (CONFIG|MAXIMUM); if we're asking for more, raise
		// the max in the persistent config first (only valid while shut off).
		maxCfg, err := l.DomainGetVcpusFlags(dom, uint32(libvirt.DomainVCPUConfig|libvirt.DomainVCPUMaximum))
		if err == nil && int32(n) > maxCfg {
			if running {
				// Cannot grow the maximum of a live domain; libvirt requires it be
				// shut off. Surface a clear conflict.
				return fmt.Errorf("%w: cannot raise maximum vCPUs above %d while the domain is running (stop it first)", vp.ErrConflict, maxCfg)
			}
			if err := l.DomainSetVcpusFlags(dom, n, uint32(libvirt.DomainVCPUConfig|libvirt.DomainVCPUMaximum)); err != nil {
				b.fail(err)
				return mapLibvirtErr(err)
			}
		}
		// Set the persistent (CONFIG) vCPU count.
		if err := l.DomainSetVcpusFlags(dom, n, uint32(libvirt.DomainVCPUConfig)); err != nil {
			b.fail(err)
			return mapLibvirtErr(err)
		}
		// Apply to the running instance too (best-effort: a guest may refuse a
		// hot (un)plug, but the persisted config is already correct).
		if running {
			if err := l.DomainSetVcpusFlags(dom, n, uint32(libvirt.DomainVCPULive)); err != nil {
				b.fail(err)
			}
		}
	}

	if memMB != nil {
		memKiB := uint64(*memMB) * 1024 //nolint:gosec // memMB validated > 0 by caller
		// Raise the persisted MAXIMUM memory if needed (shut off only).
		maxMem, err := l.DomainGetMaxMemory(dom)
		if err == nil && memKiB > maxMem {
			if running {
				return fmt.Errorf("%w: cannot raise memory above the maximum %d KiB while the domain is running (stop it first)", vp.ErrConflict, maxMem)
			}
			if err := l.DomainSetMemoryFlags(dom, memKiB, uint32(libvirt.DomainMemConfig|libvirt.DomainMemMaximum)); err != nil {
				b.fail(err)
				return mapLibvirtErr(err)
			}
		}
		if err := l.DomainSetMemoryFlags(dom, memKiB, uint32(libvirt.DomainMemConfig)); err != nil {
			b.fail(err)
			return mapLibvirtErr(err)
		}
		if running {
			if err := l.DomainSetMemoryFlags(dom, memKiB, uint32(libvirt.DomainMemLive)); err != nil {
				b.fail(err)
			}
		}
	}
	return nil
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

// markTemplate sets/clears the UniHV template marker on a domain via the official
// libvirt DomainSetMetadata RPC (Lot 4A). It writes a custom <unihv:template>true</>
// element under the domain's <metadata> (DomainMetadataElement, namespace
// unihvMetadataNS, prefix "unihv") with DomainAffectConfig so it persists. Passing
// an empty metadata removes the element (unmark). This is the libvirt-recommended
// way to attach app metadata WITHOUT re-rendering (and risking dropping devices on)
// the full domain XML.
func (b *liveBackend) markTemplate(uuid string, isTemplate bool) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	meta := libvirt.OptString{} // empty -> remove the element (unmark)
	if isTemplate {
		meta = libvirt.OptString{"<template>true</template>"}
	}
	if err := l.DomainSetMetadata(
		dom,
		int32(libvirt.DomainMetadataElement),
		meta,
		libvirt.OptString{"unihv"},
		libvirt.OptString{unihvMetadataNS},
		libvirt.DomainAffectConfig,
	); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
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
	// Resolve the CURRENT snapshot name once (DomainHasCurrentSnapshot tells us one
	// exists; its name is the one whose XML carries <active>1</active>, which we read
	// per-snapshot below).
	out := make([]vp.Snapshot, 0, len(snaps))
	for _, s := range snaps {
		snap := vp.Snapshot{ID: s.Name, VMID: uuid, Name: s.Name}
		// DomainSnapshotGetXMLDesc gives <parent>, <description>, <state> (memory),
		// <creationTime> and <active> (== current) so the UI can render a TREE.
		if raw, xerr := l.DomainSnapshotGetXMLDesc(s, 0); xerr == nil {
			applySnapshotXML(&snap, raw)
		}
		out = append(out, snap)
	}
	return out
}

// snapshotXML is the subset of a <domainsnapshot> we read for the tree.
type snapshotXML struct {
	Name         string `xml:"name"`
	Description  string `xml:"description"`
	State        string `xml:"state"`        // "running"/"paused" => has memory; "shutoff"/"disk-snapshot" => no memory
	CreationTime int64  `xml:"creationTime"` // unix seconds
	Active       int    `xml:"active"`       // 1 == current snapshot
	Parent       struct {
		Name string `xml:"name"`
	} `xml:"parent"`
	Memory struct {
		Snapshot string `xml:"snapshot,attr"` // "internal"/"external" when memory captured; "no" otherwise
	} `xml:"memory"`
}

// applySnapshotXML enriches a Snapshot from its <domainsnapshot> XML.
func applySnapshotXML(s *vp.Snapshot, raw string) {
	var sx snapshotXML
	if err := xml.Unmarshal([]byte(raw), &sx); err != nil {
		return
	}
	if sx.Description != "" {
		s.Description = sx.Description
	}
	s.ParentID = sx.Parent.Name
	s.IsCurrent = sx.Active == 1
	// A snapshot has memory state when it was taken on a running/paused domain (its
	// <state> is running/paused) or it explicitly recorded a <memory snapshot=.../>.
	switch strings.ToLower(sx.State) {
	case "running", "paused", "pmsuspended":
		s.HasMemory = true
	}
	if sx.Memory.Snapshot != "" && !strings.EqualFold(sx.Memory.Snapshot, "no") {
		s.HasMemory = true
	}
	if sx.CreationTime > 0 {
		s.CreatedAt = time.Unix(sx.CreationTime, 0).UTC()
	}
}

func (b *liveBackend) createSnapshot(uuid string, snap vp.Snapshot, opts vp.SnapshotOptions) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	xmlDesc := renderSnapshotXML(snap)
	// App-consistent (quiesced) snapshot: pass VIR_DOMAIN_SNAPSHOT_CREATE_QUIESCE so
	// libvirt asks the qemu-guest-agent to fsfreeze the guest filesystems first. This
	// only works when the agent is present + the domain is running; if the agent is
	// absent libvirt fails the QUIESCE attempt, so we retry WITHOUT the flag (a
	// crash-consistent snapshot) rather than failing the operation outright.
	var flags libvirt.DomainSnapshotCreateFlags
	if opts.Quiesce {
		flags |= libvirt.DomainSnapshotCreateQuiesce
	}
	// DomainSnapshotCreateXML fails (and MUST surface) for e.g. a diskless domain:
	// "internal and full system snapshots require all disks to be selected".
	if _, err := l.DomainSnapshotCreateXML(dom, xmlDesc, uint32(flags)); err != nil {
		if opts.Quiesce {
			// Fall back to a non-quiesced snapshot (agent not present / not frozen).
			if _, ferr := l.DomainSnapshotCreateXML(dom, xmlDesc, 0); ferr == nil {
				return nil
			}
		}
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// deleteSnapshot removes a SINGLE snapshot (DomainSnapshotDelete with no flags).
// libvirt re-parents the deleted snapshot's children and consolidates the disk
// chain (a DomainBlockCommit-equivalent) internally for external/disk snapshots.
func (b *liveBackend) deleteSnapshot(uuid, snapID string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	snap := libvirt.DomainSnapshot{Name: snapID, Dom: dom}
	if err := l.DomainSnapshotDelete(snap, 0); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

func (b *liveBackend) setCurrentSnapshot(uuid, snapID string) (bool, error) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return false, errNoConn
		}
		return false, vp.ErrNotFound
	}
	snap := libvirt.DomainSnapshot{Name: snapID, Dom: dom}
	if err := l.DomainRevertToSnapshot(snap, 0); err != nil {
		b.fail(err)
		// A missing snapshot maps to ErrNotFound (-> provider returns it); other
		// libvirt failures (e.g. revert-while-running constraints) propagate too.
		return false, mapLibvirtErr(err)
	}
	return true, nil
}

// --- guest agent (qemu-guest-agent over libvirt) ---

// guestInfo queries the in-guest agent for hostname + OS (DomainGetGuestInfo) and
// guest IPs (DomainInterfaceAddresses source=AGENT). When the agent is not present
// it returns AgentConnected=false WITHOUT an error (the contract's soft fallback);
// the only hard errors returned are connection/not-found ones.
func (b *liveBackend) guestInfo(uuid string) (*vp.GuestInfo, error) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return nil, errNoConn
		}
		return nil, vp.ErrNotFound
	}
	gi := &vp.GuestInfo{}
	// DomainGetGuestInfo(types=HOSTNAME|OS). A missing/disconnected agent makes this
	// fail with VIR_ERR_AGENT_UNRESPONSIVE / "guest agent is not connected" — treat
	// that as the soft "agent absent" state, not a transport failure.
	types := uint32(libvirt.DomainGuestInfoHostname | libvirt.DomainGuestInfoOs)
	params, err := l.DomainGetGuestInfo(dom, types, 0)
	if err != nil {
		gi.AgentConnected = false
		gi.Note = "qemu-guest-agent not connected: " + friendlyAgentErr(err)
		return gi, nil
	}
	gi.AgentConnected = true
	for _, p := range params {
		val := typedParamString(p.Value)
		switch p.Field {
		case libvirt.DomainGuestInfoHostnameHostname:
			gi.Hostname = val
		case libvirt.DomainGuestInfoOsPrettyName:
			gi.OS = val
		case libvirt.DomainGuestInfoOsName:
			if gi.OS == "" {
				gi.OS = val
			}
		}
	}
	// Guest IPs from the agent (best-effort; the agent answered above so this should
	// too, but tolerate a partial reply).
	if ifaces, ierr := l.DomainInterfaceAddresses(dom, uint32(libvirt.DomainInterfaceAddressesSrcAgent), 0); ierr == nil {
		gi.IPAddresses = ipsFromInterfaces(ifaces)
	}
	return gi, nil
}

// guestShutdown attempts a graceful shutdown through the qemu-guest-agent first
// (DomainShutdownFlags GUEST_AGENT — a clean OS shutdown), falling back to ACPI
// (the power button) and finally a hard destroy if neither is honored. Returns
// whether the agent path was taken.
func (b *liveBackend) guestShutdown(uuid string) (bool, error) {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return false, errNoConn
		}
		return false, vp.ErrNotFound
	}
	// 1) Guest-agent driven clean shutdown.
	if err := l.DomainShutdownFlags(dom, libvirt.DomainShutdownGuestAgent); err == nil {
		return true, nil
	}
	// 2) ACPI power button (works without the agent if the guest has acpid).
	if err := l.DomainShutdownFlags(dom, libvirt.DomainShutdownAcpiPowerBtn); err == nil {
		return false, nil
	}
	// 3) Plain shutdown (libvirt picks a method), else hard destroy.
	if err := l.DomainShutdown(dom); err == nil {
		return false, nil
	}
	if err := l.DomainDestroy(dom); err != nil {
		b.fail(err)
		return false, mapLibvirtErr(err)
	}
	return false, nil
}

// typedParamString extracts the string value out of a libvirt TypedParamValue
// (its concrete value lives in the .I interface field).
func typedParamString(v libvirt.TypedParamValue) string {
	switch t := v.I.(type) {
	case string:
		return t
	case fmt.Stringer:
		return t.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

// friendlyAgentErr extracts the libvirt message for an agent failure.
func friendlyAgentErr(err error) string {
	var le libvirt.Error
	if errorAs(err, &le) {
		return le.Message
	}
	return err.Error()
}

// --- online disk resize ---

// resizeDisk grows the disk identified by diskID to newCapacityGB via
// DomainBlockResize on the live domain block device. The underlying StorageVol is
// grown first (StorageVolResize) so the qcow2/raw file actually has room. Shrinking
// is rejected (ErrInvalidSpec) before any write. Works on a RUNNING domain (online
// resize); also valid while shut off (CONFIG-only effect).
func (b *liveBackend) resizeDisk(uuid, diskID string, newCapacityGB float64) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	dk, found := b.findDisk(l, dom, diskID)
	if !found {
		return vp.ErrNotFound
	}
	newBytes := uint64(newCapacityGB * bytesPerGB)
	// Determine current capacity (to reject a shrink) and whether the domain is live.
	running := false
	if st, _, serr := l.DomainGetState(dom, 0); serr == nil {
		s := libvirtState(st)
		running = s == domRunning || s == domBlocked
	}
	if dk.source != "" {
		if vol, verr := l.StorageVolLookupByPath(dk.source); verr == nil {
			if _, capBytes, _, ierr := l.StorageVolGetInfo(vol); ierr == nil {
				if newBytes < capBytes {
					return fmt.Errorf("%w: cannot shrink disk from %d to %d bytes (online shrink is unsafe)", vp.ErrInvalidSpec, capBytes, newBytes)
				}
			}
			// Grow the backing volume ONLY when the domain is shut off. While the
			// domain is RUNNING, qemu holds an exclusive write lock on the image, so a
			// separate StorageVolResize (qemu-img resize) would fail "Failed to get
			// write lock". In that case DomainBlockResize below grows the qcow2 in
			// place through the running qemu process itself, which is the correct
			// online path. For raw/offline disks the volume resize is what extends the
			// file, so we keep it for the shut-off case.
			if !running {
				if rerr := l.StorageVolResize(vol, newBytes, 0); rerr != nil {
					b.fail(rerr)
					return mapResizeErr(rerr)
				}
			}
		}
	}
	// DomainBlockResize takes the disk's TARGET dev (e.g. "vda") and a size; with the
	// BYTES flag the size is interpreted as bytes (default is KiB). On a RUNNING
	// domain qemu grows the qcow2 in place and surfaces the new size to the guest
	// (true online resize); on a shut-off domain it resizes the just-grown image.
	if err := l.DomainBlockResize(dom, dk.target, newBytes, libvirt.DomainBlockResizeBytes); err != nil {
		b.fail(err)
		return mapResizeErr(err)
	}
	return nil
}

// mapResizeErr maps a block-resize failure to a contract sentinel, recognizing the
// libvirt "shrink" rejection as an invalid spec (422) rather than an opaque 500.
func mapResizeErr(err error) error {
	if err == nil {
		return nil
	}
	var le libvirt.Error
	if errorAs(err, &le) {
		m := strings.ToLower(le.Message)
		if strings.Contains(m, "shrink") || strings.Contains(m, "smaller") || strings.Contains(m, "cannot decrease") {
			return fmt.Errorf("%w: %s", vp.ErrInvalidSpec, le.Message)
		}
	}
	return mapLibvirtErr(err)
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
	case libvirt.ErrNetworkExist, libvirt.ErrDomExist, libvirt.ErrStorageVolExist,
		libvirt.ErrOperationInvalid:
		// Duplicate-name / already-exists and "operation invalid in this state"
		// are conflicts (HTTP 409). libvirt returns ErrNetworkExist (37) for a
		// duplicate network name on NetworkDefineXML/NetworkCreate.
		return vp.ErrConflict
	case libvirt.ErrInvalidArg, libvirt.ErrXMLError, libvirt.ErrXMLDetail,
		libvirt.ErrConfigUnsupported:
		// Bad spec / unacceptable config (e.g. "requested vcpus is greater than
		// max allowable", or a diskless internal-snapshot request) -> 422.
		return vp.ErrInvalidSpec
	case libvirt.ErrOperationFailed:
		// ErrOperationFailed (9) is generic; disambiguate on the message and surface
		// an actionable 409 instead of an opaque 500 where we can.
		//   - duplicate-name collision (NetworkDefineXML "... already exists ...")
		//   - hot-plug unsupported on i440fx ("Bus 'pci.0' does not support hotplugging")
		//   - disk lock contention ("Failed to get ... lock")
		if isAlreadyExistsMsg(le.Message) || isHotplugUnsupportedMsg(le.Message) || isLockMsg(le.Message) {
			return fmt.Errorf("%w: %s", vp.ErrConflict, friendlyLibvirtMsg(le.Message))
		}
		return err
	default:
		return err
	}
}

// isAlreadyExistsMsg reports whether a libvirt error message describes a
// duplicate-name / already-exists collision (which libvirt sometimes reports
// under the generic ErrOperationFailed code rather than a dedicated *Exist code).
func isAlreadyExistsMsg(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "already exists")
}

// isHotplugUnsupportedMsg detects the i440fx PCI-hotplug limitation.
func isHotplugUnsupportedMsg(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "does not support hotplug") || strings.Contains(m, "no more available pci slots")
}

// isLockMsg detects disk-lock contention (e.g. attaching a disk already in use).
func isLockMsg(msg string) bool {
	return strings.Contains(strings.ToLower(msg), "lock")
}

// friendlyLibvirtMsg turns a raw libvirt message into an actionable hint.
func friendlyLibvirtMsg(msg string) string {
	switch {
	case isHotplugUnsupportedMsg(msg):
		return "this VM's machine type (i440fx) does not support hot-plug; recreate the VM as q35, or stop the VM to attach the device offline"
	case isLockMsg(msg):
		return "the disk is already in use (locked) by this or another VM"
	case isAlreadyExistsMsg(msg):
		return "an object with that name already exists"
	default:
		return msg
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
			// ID is the volume NAME (unique within a pool) so the API URL carries a
			// simple token, not a URL-encoded absolute path. volHandle resolves it via
			// StorageVolLookupByName (fast path); the real path is in .Path.
			ID:         v.Name,
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

// exportDisk streams the domain's primary disk image converted to `format` using a
// real `qemu-img convert`. The source path comes from the live domain XML's first
// <disk><source file>. Returns a reader of the converted image + its byte size.
// This is the REAL disk export that powers cross-hypervisor V2V.
func (b *liveBackend) exportDisk(uuid string, format vp.DiskFormat) (io.ReadCloser, int64, error) {
	// Resolve the domain's first disk source path from its XML.
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		return nil, 0, vp.ErrNotFound
	}
	raw, err := l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		b.fail(err)
		return nil, 0, mapLibvirtErr(err)
	}
	var dx struct {
		Devices struct {
			Disks []struct {
				Device string `xml:"device,attr"`
				Source struct {
					File string `xml:"file,attr"`
				} `xml:"source"`
			} `xml:"disk"`
		} `xml:"devices"`
	}
	if err := xml.Unmarshal([]byte(raw), &dx); err != nil {
		return nil, 0, vp.ErrInvalidSpec
	}
	srcPath := ""
	for _, dk := range dx.Devices.Disks {
		if dk.Device == "disk" && dk.Source.File != "" {
			srcPath = dk.Source.File
			break
		}
	}
	if srcPath == "" {
		// No real backing disk -> let the Provider fall back to the placeholder.
		return nil, 0, errNoRealDisk
	}
	qemuImg, err := exec.LookPath("qemu-img")
	if err != nil {
		return nil, 0, fmt.Errorf("qemu-img not available for disk export: %w", err)
	}
	tmpDir, err := os.MkdirTemp("", "unihv-export-*")
	if err != nil {
		return nil, 0, err
	}
	toTok := qemuExportFormat(format)
	outPath := filepath.Join(tmpDir, "disk."+toTok)
	// qemu-img convert -U -O <fmt> <src> <out>
	//   -U forces a SHARED lock so a RUNNING VM's disk can be exported (otherwise
	//      libvirt's exclusive write lock makes qemu-img fail "Failed to get lock").
	//      This is the standard live-export approach; the snapshot is crash-consistent.
	//   -O selects the target format; the source format is auto-detected.
	cmd := exec.Command(qemuImg, "convert", "-U", "-O", toTok, srcPath, outPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		os.RemoveAll(tmpDir)
		return nil, 0, fmt.Errorf("qemu-img export convert failed: %v: %s", err, string(out))
	}
	f, err := os.Open(outPath)
	if err != nil {
		os.RemoveAll(tmpDir)
		return nil, 0, err
	}
	fi, _ := f.Stat()
	size := int64(0)
	if fi != nil {
		size = fi.Size()
	}
	// Wrap so closing the reader also removes the temp dir.
	return &cleanupReadCloser{f: f, dir: tmpDir}, size, nil
}

// qemuExportFormat maps a DiskFormat to a qemu-img -O token.
func qemuExportFormat(f vp.DiskFormat) string {
	switch f {
	case vp.DiskVMDK:
		return "vmdk"
	case vp.DiskRaw:
		return "raw"
	case vp.DiskVHDX:
		return "vhdx"
	case vp.DiskVHD:
		return "vpc"
	default:
		return "qcow2"
	}
}

// cleanupReadCloser removes a temp dir when the underlying file is closed.
type cleanupReadCloser struct {
	f   *os.File
	dir string
}

func (c *cleanupReadCloser) Read(p []byte) (int, error) { return c.f.Read(p) }
func (c *cleanupReadCloser) Close() error {
	err := c.f.Close()
	_ = os.RemoveAll(c.dir)
	return err
}

// provisionVolume creates a backing volume in poolName and returns its on-disk
// path (for use as a domain disk <source file>). Used by defineDomain to back
// size-only disks. If a volume of that name already exists, its existing path is
// returned (idempotent).
func (b *liveBackend) provisionVolume(poolName, volName string, capBytes int64, format string) (string, error) {
	pool, err := b.poolHandle(poolName)
	if err != nil {
		return "", err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return "", errNoConn
	}
	if format != "raw" {
		format = "qcow2"
	}
	// Reuse an existing volume of the same name if present.
	if vol, err := l.StorageVolLookupByName(pool, volName); err == nil {
		if p, perr := l.StorageVolGetPath(vol); perr == nil {
			return p, nil
		}
	}
	xmlDesc := renderVolumeXML(volName, format, uint64(capBytes))
	vol, err := l.StorageVolCreateXML(pool, xmlDesc, 0)
	if err != nil {
		b.fail(err)
		return "", mapLibvirtErr(err)
	}
	path, err := l.StorageVolGetPath(vol)
	if err != nil {
		b.fail(err)
		return "", mapLibvirtErr(err)
	}
	return path, nil
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

// =============================================================================
// hot-plug device management (DeviceManager) over the REAL libvirt API.
//
// All operations use the LIVE|CONFIG modify flags so the change is applied to the
// RUNNING domain AND persisted to the domain config (survives a power cycle):
//   - attach : DomainAttachDeviceFlags(<device xml>, LIVE|CONFIG)
//   - detach : DomainDetachDeviceFlags(<device xml>, LIVE|CONFIG)
//   - ISO    : DomainUpdateDeviceFlags(<cdrom xml>, LIVE|CONFIG) — update-device
//              semantics swap the media inside the EXISTING cdrom (insert/eject)
//              rather than adding/removing the cdrom device itself.
// =============================================================================

// hotPlugFlags is the LIVE|CONFIG flag pair used for every hot-plug op so the
// change hits the running instance and the persistent config. Attach/Detach take a
// raw uint32; UpdateDevice takes the typed DomainDeviceModifyFlags.
const hotPlugFlags = uint32(libvirt.DomainDeviceModifyLive | libvirt.DomainDeviceModifyConfig)

func (b *liveBackend) attachDisk(uuid string, spec vp.DiskSpec) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	// Resolve a backing source: an explicit SourcePath, else provision a fresh
	// qcow2/raw volume of the requested size (reusing provisionVolume).
	source := spec.SourcePath
	format := string(normalizeDiskFormat(string(spec.Format)))
	if source == "" {
		poolName := spec.StorageID
		if poolName == "" {
			poolName = "default"
		}
		volName := fmt.Sprintf("%s-hotdisk-%d.%s", sanitizeBridge(uuid), time.Now().UnixNano(), format)
		path, err := b.provisionVolume(poolName, volName, int64(spec.CapacityGB*bytesPerGB), format)
		if err != nil {
			return err
		}
		source = path
	}
	// Pick the next free virtio target (vdb, vdc, ...) from the live domain XML.
	target := b.nextDiskTarget(l, dom)
	xmlDesc := renderDiskDeviceXML(target, format, source)
	if err := l.DomainAttachDeviceFlags(dom, xmlDesc, hotPlugFlags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

func (b *liveBackend) detachDisk(uuid, diskID string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	dk, found := b.findDisk(l, dom, diskID)
	if !found {
		return vp.ErrNotFound
	}
	xmlDesc := renderDiskDeviceXML(dk.target, dk.driver, dk.source)
	if err := l.DomainDetachDeviceFlags(dom, xmlDesc, hotPlugFlags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

func (b *liveBackend) attachNIC(uuid string, spec vp.NICSpec) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	model := spec.Model
	if model == "" {
		model = "virtio"
	}
	xmlDesc := renderNICDeviceXML(spec.NetworkID, model, spec.MAC)
	if err := l.DomainAttachDeviceFlags(dom, xmlDesc, hotPlugFlags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

func (b *liveBackend) detachNIC(uuid, nicID string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	nic, found := b.findNIC(l, dom, nicID)
	if !found {
		return vp.ErrNotFound
	}
	xmlDesc := renderNICDeviceXML(nic.network, nic.model, nic.mac)
	if err := l.DomainDetachDeviceFlags(dom, xmlDesc, hotPlugFlags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// mountISO inserts media into the domain's EXISTING cdrom via update-device. If the
// domain has no cdrom yet, one is hot-attached carrying the media.
func (b *liveBackend) mountISO(uuid, isoPath string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	target, exists := b.cdromTarget(l, dom)
	if !exists {
		// No cdrom present -> hot-attach a fresh sata cdrom carrying the media.
		xmlDesc := renderCDROMDeviceXML(target, isoPath)
		if err := l.DomainAttachDeviceFlags(dom, xmlDesc, hotPlugFlags); err != nil {
			b.fail(err)
			return mapLibvirtErr(err)
		}
		return nil
	}
	// Update-device swaps the media inside the existing cdrom (no reboot).
	xmlDesc := renderCDROMDeviceXML(target, isoPath)
	if err := l.DomainUpdateDeviceFlags(dom, xmlDesc, libvirt.DomainDeviceModifyLive|libvirt.DomainDeviceModifyConfig); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// unmountISO ejects the media from the domain's cdrom (update-device with an empty
// <source>), leaving the empty cdrom in place.
func (b *liveBackend) unmountISO(uuid string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	target, exists := b.cdromTarget(l, dom)
	if !exists {
		return vp.ErrNotFound
	}
	xmlDesc := renderCDROMDeviceXML(target, "") // empty source -> eject
	if err := l.DomainUpdateDeviceFlags(dom, xmlDesc, libvirt.DomainDeviceModifyLive|libvirt.DomainDeviceModifyConfig); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// --- hot-plug helpers: resolve targets / locate devices from the live XML ---

// liveDisk / liveNIC are the resolved device coordinates needed to render a detach
// XML that libvirt can match against the running domain.
type liveDisk struct{ target, driver, source string }
type liveNIC struct{ network, model, mac string }

// domXMLDevices parses the live domain XML into the disk/NIC subset.
func (b *liveBackend) domXMLDevices(l *libvirt.Libvirt, dom libvirt.Domain) (domainXML, bool) {
	var dx domainXML
	raw, err := l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		b.fail(err)
		return dx, false
	}
	if err := xml.Unmarshal([]byte(raw), &dx); err != nil {
		return dx, false
	}
	return dx, true
}

// nextDiskTarget returns the next free virtio disk target (vdb, vdc, ...) for the
// domain, skipping targets already in use.
func (b *liveBackend) nextDiskTarget(l *libvirt.Libvirt, dom libvirt.Domain) string {
	dx, _ := b.domXMLDevices(l, dom)
	used := map[string]bool{}
	for _, dk := range dx.Devices.Disks {
		used[dk.Target.Dev] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		t := "vd" + string(c)
		if !used[t] {
			return t
		}
	}
	return "vdz"
}

// findDisk locates a disk on the domain by the normalized device id (uuid-target,
// as surfaced by GetVM/normalizeDomain), or by bare target dev, or by source path.
func (b *liveBackend) findDisk(l *libvirt.Libvirt, dom libvirt.Domain, diskID string) (liveDisk, bool) {
	dx, ok := b.domXMLDevices(l, dom)
	if !ok {
		return liveDisk{}, false
	}
	uuid := uuidString(dom.UUID)
	for _, dk := range dx.Devices.Disks {
		if dk.Device == "cdrom" {
			continue
		}
		src := dk.Source.File
		if src == "" {
			src = dk.Source.Dev
		}
		driver := dk.Driver.Type
		if driver == "" {
			driver = "qcow2"
		}
		normID := uuid + "-" + dk.Target.Dev
		if diskID == normID || diskID == dk.Target.Dev || (src != "" && diskID == src) {
			return liveDisk{target: dk.Target.Dev, driver: driver, source: src}, true
		}
	}
	return liveDisk{}, false
}

// findNIC locates a NIC by normalized id (uuid-nic<index>), by MAC, or by network.
func (b *liveBackend) findNIC(l *libvirt.Libvirt, dom libvirt.Domain, nicID string) (liveNIC, bool) {
	dx, ok := b.domXMLDevices(l, dom)
	if !ok {
		return liveNIC{}, false
	}
	uuid := uuidString(dom.UUID)
	for i, n := range dx.Devices.Interfaces {
		net := n.Source.Network
		if net == "" {
			net = n.Source.Bridge
		}
		normID := fmt.Sprintf("%s-nic%d", uuid, i)
		if nicID == normID || (n.MAC.Address != "" && nicID == n.MAC.Address) || nicID == net {
			return liveNIC{network: net, model: n.Model.Type, mac: n.MAC.Address}, true
		}
	}
	return liveNIC{}, false
}

// cdromTarget returns the target dev of the domain's first cdrom (and whether one
// exists). When absent it proposes a free sata target so a fresh cdrom can be added.
func (b *liveBackend) cdromTarget(l *libvirt.Libvirt, dom libvirt.Domain) (string, bool) {
	dx, _ := b.domXMLDevices(l, dom)
	used := map[string]bool{}
	for _, dk := range dx.Devices.Disks {
		if dk.Device == "cdrom" {
			return dk.Target.Dev, true
		}
		used[dk.Target.Dev] = true
	}
	for c := 'a'; c <= 'z'; c++ {
		t := "sd" + string(c)
		if !used[t] {
			return t, false
		}
	}
	return "sdz", false
}

// renderDiskDeviceXML builds a single <disk device='disk'> element for attach/detach.
func renderDiskDeviceXML(target, format, source string) string {
	if format == "" {
		format = "qcow2"
	}
	if target == "" {
		target = "vdb"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<disk type='file' device='disk'>\n")
	fmt.Fprintf(&sb, "  <driver name='qemu' type='%s'/>\n", xmlEscape(format))
	fmt.Fprintf(&sb, "  <source file='%s'/>\n", xmlEscape(source))
	fmt.Fprintf(&sb, "  <target dev='%s' bus='virtio'/>\n", xmlEscape(target))
	fmt.Fprintf(&sb, "</disk>\n")
	return sb.String()
}

// renderNICDeviceXML builds a single <interface type='network'> element.
func renderNICDeviceXML(network, model, mac string) string {
	if model == "" {
		model = "virtio"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<interface type='network'>\n")
	fmt.Fprintf(&sb, "  <source network='%s'/>\n", xmlEscape(network))
	if mac != "" {
		fmt.Fprintf(&sb, "  <mac address='%s'/>\n", xmlEscape(mac))
	}
	fmt.Fprintf(&sb, "  <model type='%s'/>\n", xmlEscape(model))
	fmt.Fprintf(&sb, "</interface>\n")
	return sb.String()
}

// renderCDROMDeviceXML builds a <disk device='cdrom'> element. An empty isoPath
// renders an EMPTY cdrom (no <source>), which is exactly an eject when used with
// update-device.
func renderCDROMDeviceXML(target, isoPath string) string {
	if target == "" {
		target = "sda"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<disk type='file' device='cdrom'>\n")
	fmt.Fprintf(&sb, "  <driver name='qemu' type='raw'/>\n")
	if isoPath != "" {
		fmt.Fprintf(&sb, "  <source file='%s'/>\n", xmlEscape(isoPath))
	}
	fmt.Fprintf(&sb, "  <target dev='%s' bus='sata'/>\n", xmlEscape(target))
	fmt.Fprintf(&sb, "  <readonly/>\n")
	fmt.Fprintf(&sb, "</disk>\n")
	return sb.String()
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
		// libvirt REQUIRES an IP for nat/route forwarding ("nat forwarding requested,
		// but no IP address provided"). When the caller did not supply a CIDR, default
		// to a deterministic per-name managed subnet so the common "create a NAT
		// network" path just works (192.168.<h>.0/24, h derived from the name).
		cidr := spec.CIDR
		if cidr == "" {
			cidr = defaultNATCIDR(spec.Name)
		}
		appendNetworkIP(&sb, cidr)
	}
	fmt.Fprintf(&sb, "</network>\n")
	return sb.String()
}

// defaultNATCIDR returns a deterministic private /24 for a managed NAT/route network
// when the caller gives no CIDR. The third octet is derived from the name hash to
// reduce collisions across multiple auto-created networks (range 100..199).
func defaultNATCIDR(name string) string {
	var h uint32 = 2166136261
	for i := 0; i < len(name); i++ {
		h ^= uint32(name[i])
		h *= 16777619
	}
	octet := 100 + int(h%100) // 100..199
	return fmt.Sprintf("192.168.%d.0/24", octet)
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
	VCPU    struct {
		// chardata is the MAXIMUM vCPUs; current attr (when present) is the active
		// count after a DomainSetVcpusFlags(...CONFIG) without raising the maximum.
		Current string `xml:"current,attr"`
		Max     int    `xml:",chardata"`
	} `xml:"vcpu"`
	Memory struct {
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
	// CPU topology + model (Lot 4A). Mode is host-passthrough/host-model/custom; the
	// <topology> gives sockets/cores/threads; <model> the named CPU model (custom).
	CPU struct {
		Mode     string `xml:"mode,attr"`
		Model    string `xml:"model"`
		Topology struct {
			Sockets int `xml:"sockets,attr"`
			Cores   int `xml:"cores,attr"`
			Threads int `xml:"threads,attr"`
		} `xml:"topology"`
	} `xml:"cpu"`
	// Metadata carries UniHV's custom <unihv:template> element. The local-name match
	// (any-namespace 'template') reads the template marker set via DomainSetMetadata.
	Metadata struct {
		Template string `xml:"template"`
	} `xml:"metadata"`
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
	// Prefer the current (active) vCPU count when libvirt records one (e.g. after a
	// CONFIG vcpu change that did not raise the maximum: <vcpu current='2'>4</vcpu>),
	// else fall back to the maximum chardata.
	if cur := atoiSafe(dx.VCPU.Current); cur > 0 {
		d.VCPUs = cur
	} else if dx.VCPU.Max > 0 {
		d.VCPUs = dx.VCPU.Max
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
	// CPU topology + model (Lot 4A): surface an explicit <cpu><topology> back into the
	// model so GetVM/ListVMs reflect sockets/cores/threads + the CPU model (via Labels).
	if t := dx.CPU.Topology; t.Sockets > 0 || t.Cores > 0 || t.Threads > 0 || dx.CPU.Mode != "" || dx.CPU.Model != "" {
		model := strings.TrimSpace(dx.CPU.Model)
		if model == "" {
			model = strings.TrimSpace(dx.CPU.Mode) // host-passthrough/host-model
		}
		d.CPU = &vp.CPUSpec{
			Sockets:        t.Sockets,
			CoresPerSocket: t.Cores,
			ThreadsPerCore: t.Threads,
			Model:          model,
		}
	}
	// Template marker (Lot 4A): the <unihv:template>true</> metadata element set via
	// DomainSetMetadata.
	if strings.EqualFold(strings.TrimSpace(dx.Metadata.Template), "true") {
		d.IsTemplate = true
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
// Secure-boot OVMF firmware paths (confirmed present on the WSL libvirt host).
//   - secureBootCodePath    : the read-only OVMF CODE built with secure-boot support.
//   - secureBootVarsTemplate: the VARS template PRE-ENROLLED with Microsoft's KEK/db
//     keys, so a guest can verify Windows' Microsoft-signed bootloader out of the box.
//     libvirt copies it to a per-VM nvram file on first define.
const (
	secureBootCodePath     = "/usr/share/OVMF/OVMF_CODE_4M.secboot.fd"
	secureBootVarsTemplate = "/usr/share/OVMF/OVMF_VARS_4M.ms.fd"
	// secureBootNVRAMDir is libvirt's default per-VM nvram location.
	secureBootNVRAMDir = "/var/lib/libvirt/images"
)

// secureBootNVRAMPath is the per-VM nvram (VARS) copy path. Each VM gets its own
// writable copy of the MS-keys VARS template so their secure-boot state is isolated.
// The path is derived from the (libvirt-unique) domain name, sanitized to a safe
// filename — NOT truncated, so distinct VMs never collide on the same nvram file.
func secureBootNVRAMPath(name string) string {
	return filepath.Join(secureBootNVRAMDir, sanitizeFilename(name)+"_VARS.fd")
}

// sanitizeFilename keeps [A-Za-z0-9._-] from name (replacing other runes with '-')
// so the result is a safe, collision-free filename component. Unlike sanitizeBridge
// it does NOT cap the length, preserving uniqueness across VM names.
func sanitizeFilename(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	if sb.Len() == 0 {
		return "vm"
	}
	return sb.String()
}

func renderDomainXML(d *libvirtDomain) string {
	memKiB := d.MemoryKB
	if memKiB <= 0 {
		memKiB = 512 * 1024
	}
	vcpu := d.VCPUs
	if vcpu <= 0 {
		vcpu = 1
	}
	// CPU topology (Lot 4A): when an explicit topology is set, <vcpu> MUST equal
	// sockets*cores*threads or libvirt rejects the domain. Override the flat count.
	if n := cpuTopologyVCPUs(d.CPU); n > 0 {
		vcpu = n
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<domain type='kvm'>\n")
	fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEscape(d.Name))
	// Template metadata (Lot 4A): UniHV marks golden-image domains with a custom
	// <unihv:template> element. Emitted at create time when IsTemplate is set; the
	// MarkTemplate path uses DomainSetMetadata to toggle it later.
	if d.IsTemplate {
		fmt.Fprintf(&sb, "  <metadata>\n")
		fmt.Fprintf(&sb, "    <unihv:template xmlns:unihv='%s'>true</unihv:template>\n", xmlEscape(unihvMetadataNS))
		fmt.Fprintf(&sb, "  </metadata>\n")
	}
	fmt.Fprintf(&sb, "  <memory unit='KiB'>%d</memory>\n", memKiB)
	fmt.Fprintf(&sb, "  <currentMemory unit='KiB'>%d</currentMemory>\n", memKiB)
	fmt.Fprintf(&sb, "  <vcpu placement='static'>%d</vcpu>\n", vcpu)
	// CPU topology + model (Lot 4A): emit a <cpu> element when a topology/model is
	// requested. Mode 'host-passthrough' (the default / empty model) exposes the host
	// CPU verbatim; a named model uses mode 'custom'. A <topology> pins sockets/cores/
	// threads so the guest sees the intended core layout (licensing/NUMA/perf).
	if c := d.CPU; c != nil {
		model := strings.TrimSpace(c.Model)
		mode := "host-passthrough"
		switch strings.ToLower(model) {
		case "", "host-passthrough":
			mode = "host-passthrough"
			model = ""
		case "host-model":
			mode = "host-model"
			model = ""
		default:
			mode = "custom"
		}
		hasTopo := c.Sockets > 0 && c.CoresPerSocket > 0 && c.ThreadsPerCore > 0
		if mode == "custom" {
			fmt.Fprintf(&sb, "  <cpu mode='custom' match='exact' check='partial'>\n")
			fmt.Fprintf(&sb, "    <model fallback='allow'>%s</model>\n", xmlEscape(model))
			if hasTopo {
				fmt.Fprintf(&sb, "    <topology sockets='%d' cores='%d' threads='%d'/>\n", c.Sockets, c.CoresPerSocket, c.ThreadsPerCore)
			}
			fmt.Fprintf(&sb, "  </cpu>\n")
		} else if hasTopo {
			fmt.Fprintf(&sb, "  <cpu mode='%s'>\n", mode)
			fmt.Fprintf(&sb, "    <topology sockets='%d' cores='%d' threads='%d'/>\n", c.Sockets, c.CoresPerSocket, c.ThreadsPerCore)
			fmt.Fprintf(&sb, "  </cpu>\n")
		} else {
			fmt.Fprintf(&sb, "  <cpu mode='%s'/>\n", mode)
		}
	}
	// Always use the modern q35 machine type so EVERY UniHV-created VM supports PCIe
	// hot-plug (live add/remove of disks & NICs). i440fx's pci.0 bus cannot hotplug,
	// so we no longer emit it. UEFI adds the efi firmware loader.
	switch {
	case d.SecureBoot:
		// Secure Boot: pin the SECURE-BOOT OVMF firmware explicitly. The loader is
		// the secboot CODE image (secure='yes' arms the SMM-protected boot path); the
		// nvram is a PER-VM copy of the Microsoft-keys-enrolled VARS template so the
		// guest's secure-boot db/KEK/PK chain validates Windows' signed bootloader.
		// libvirt copies <nvram template=...> -> the per-VM path automatically on
		// first define. SMM (emitted in <features> below) is REQUIRED for secure boot.
		nvram := secureBootNVRAMPath(d.Name)
		// NOTE: do NOT set firmware='efi' on <os> here. firmware='efi' asks libvirt to
		// AUTOSELECT a firmware from its descriptors, which conflicts with an explicit
		// <loader> path ("Unable to find 'efi' firmware compatible with the current
		// configuration"). We pin the loader+nvram MANUALLY instead — the canonical way
		// to force a specific secure-boot OVMF build.
		fmt.Fprintf(&sb, "  <os>\n")
		fmt.Fprintf(&sb, "    <type arch='x86_64' machine='q35'>hvm</type>\n")
		fmt.Fprintf(&sb, "    <loader readonly='yes' secure='yes' type='pflash'>%s</loader>\n", xmlEscape(secureBootCodePath))
		fmt.Fprintf(&sb, "    <nvram template='%s'>%s</nvram>\n", xmlEscape(secureBootVarsTemplate), xmlEscape(nvram))
		fmt.Fprintf(&sb, "  </os>\n")
	case d.Firmware == vp.FirmwareUEFI:
		fmt.Fprintf(&sb, "  <os firmware='efi'><type arch='x86_64' machine='q35'>hvm</type></os>\n")
	default:
		fmt.Fprintf(&sb, "  <os><type arch='x86_64' machine='q35'>hvm</type></os>\n")
	}
	// ACPI is required by q35/UEFI on x86_64 and expected by every modern guest;
	// APIC enables SMP/IO interrupt routing. Secure Boot additionally REQUIRES SMM
	// (System Management Mode) so the firmware can protect the secure-boot variables.
	if d.SecureBoot {
		fmt.Fprintf(&sb, "  <features><acpi/><apic/><smm state='on'/></features>\n")
	} else {
		fmt.Fprintf(&sb, "  <features><acpi/><apic/></features>\n")
	}
	fmt.Fprintf(&sb, "  <devices>\n")
	// Pre-provision ample spare pcie-root-ports so devices can be HOT-PLUGGED later
	// (CapHotPlug) AND so the VM's own boot disk/NIC/cdrom have slots. Too few ports
	// causes "No more available PCI slots" on a later live attach. 14 gives generous
	// headroom (libvirt assigns the addresses automatically; unused ports are cheap).
	for i := 1; i <= 14; i++ {
		fmt.Fprintf(&sb, "    <controller type='pci' index='%d' model='pcie-root-port'/>\n", i)
	}
	// vTPM 2.0: an emulated TPM (swtpm) backing a tpm-crb device — Windows 11 requires
	// a TPM 2.0. tpm-crb is the modern CRB interface (preferred over tpm-tis for
	// UEFI/Win11). Independent of Secure Boot; Win11 needs BOTH (TPM + Secure Boot).
	if d.TPM {
		fmt.Fprintf(&sb, "    <tpm model='tpm-crb'>\n")
		fmt.Fprintf(&sb, "      <backend type='emulator' version='2.0'/>\n")
		fmt.Fprintf(&sb, "    </tpm>\n")
	}
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
	// CD-ROM: always present so media can be MOUNTED LATER via update-device
	// (CapHotPlug MountISO/UnmountISO). A sata cdrom cannot be hot-ADDED, so it must
	// exist from creation. When BootISO is set it carries that ISO and is bootable;
	// otherwise it is an empty (no <source>) cdrom ready to receive media.
	fmt.Fprintf(&sb, "    <disk type='file' device='cdrom'>\n")
	fmt.Fprintf(&sb, "      <driver name='qemu' type='raw'/>\n")
	if d.BootISO != "" {
		fmt.Fprintf(&sb, "      <source file='%s'/>\n", xmlEscape(d.BootISO))
	}
	fmt.Fprintf(&sb, "      <target dev='sda' bus='sata'/>\n")
	fmt.Fprintf(&sb, "      <readonly/>\n")
	if d.BootISO != "" {
		fmt.Fprintf(&sb, "      <boot order='1'/>\n")
	}
	fmt.Fprintf(&sb, "    </disk>\n")
	// Cloud-init NoCloud seed: a SECOND read-only cdrom carrying the 'cidata' ISO so a
	// cloud-init-enabled guest reads its datasource (user-data + meta-data) on first
	// boot. Uses a distinct sata target (sdb) so it coexists with the boot/media cdrom.
	if d.SeedISO != "" {
		fmt.Fprintf(&sb, "    <disk type='file' device='cdrom'>\n")
		fmt.Fprintf(&sb, "      <driver name='qemu' type='raw'/>\n")
		fmt.Fprintf(&sb, "      <source file='%s'/>\n", xmlEscape(d.SeedISO))
		fmt.Fprintf(&sb, "      <target dev='sdb' bus='sata'/>\n")
		fmt.Fprintf(&sb, "      <readonly/>\n")
		fmt.Fprintf(&sb, "    </disk>\n")
	}
	// Windows sysprep seed (Lot 4A): a read-only cdrom carrying the autounattend.xml
	// ISO (volid 'sysprep'). Windows Setup auto-discovers an autounattend.xml on any
	// attached removable media, so this drives an unattended specialize/OOBE pass —
	// the Windows analogue of the cloud-init NoCloud seed. Distinct sata target (sdc).
	if d.SysprepISO != "" {
		fmt.Fprintf(&sb, "    <disk type='file' device='cdrom'>\n")
		fmt.Fprintf(&sb, "      <driver name='qemu' type='raw'/>\n")
		fmt.Fprintf(&sb, "      <source file='%s'/>\n", xmlEscape(d.SysprepISO))
		fmt.Fprintf(&sb, "      <target dev='sdc' bus='sata'/>\n")
		fmt.Fprintf(&sb, "      <readonly/>\n")
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
