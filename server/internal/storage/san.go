package storage

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
)

// sanBackend realizes an NFS / iSCSI / SMB(CIFS) target as a libvirt storage pool
// of the matching type on a target libvirt (KVM) host. Test() defines + starts the
// pool as the connectivity probe, then tears it down (a dry-run): a successful
// pool-start means the export/portal/share is reachable and mountable by the host.
type sanBackend struct {
	cfg      Config
	poolName string
}

func newSANBackend(cfg Config) (Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(cfg.PoolName)
	if name == "" {
		name = derivePoolName(cfg)
	}
	return &sanBackend{cfg: cfg, poolName: name}, nil
}

func (b *sanBackend) Type() Type { return b.cfg.Type }

// Test dials libvirt, defines a transient pool of the requested type, starts it,
// and (best-effort) tears it down. A failure to start surfaces the real mount /
// portal error from libvirt with the secret stripped.
func (b *sanBackend) Test(ctx context.Context) error {
	endpoint := strings.TrimSpace(b.cfg.LibvirtEndpoint)
	network, addr := parseLibvirtEndpoint(endpoint)

	dl := net.Dialer{Timeout: 15 * time.Second}
	if dctx, ok := ctx.Deadline(); ok {
		if d := time.Until(dctx); d > 0 && d < dl.Timeout {
			dl.Timeout = d
		}
	}
	conn, err := dl.DialContext(ctx, network, addr)
	if err != nil {
		return fmt.Errorf("connect libvirt %s://%s: %w", network, addr, err)
	}
	defer func() { _ = conn.Close() }()

	// Same handshake as live_libvirt.go: New(conn) + Connect().
	l := libvirt.New(conn)
	if err := l.Connect(); err != nil {
		return fmt.Errorf("libvirt handshake: %w", err)
	}
	defer func() { _ = l.Disconnect() }()

	poolXML := renderPoolXML(b.cfg, b.poolName)

	// Define the (persistent) pool, then start it. StoragePoolDefineXML validates
	// the XML and StoragePoolCreate performs the actual mount/login — that is the
	// real connectivity check.
	pool, err := l.StoragePoolDefineXML(poolXML, 0)
	if err != nil {
		return fmt.Errorf("define %s pool: %w", b.cfg.Type, sanitizeLibvirtErr(err))
	}
	// Always undefine on the way out so the probe leaves no orphan.
	defer func() { _ = l.StoragePoolUndefine(pool) }()

	if err := l.StoragePoolCreate(pool, 0); err != nil {
		return fmt.Errorf("start %s pool (mount/login failed): %w", b.cfg.Type, sanitizeLibvirtErr(err))
	}
	// Tear the live pool down (best-effort): the probe must not leave it mounted.
	_ = l.StoragePoolDestroy(pool)
	return nil
}

// fsDelegate returns a filesystem ObjectStore rooted at this SAN/NAS pool's
// mountpoint. NFS/SMB pools, once started by libvirt, mount at a deterministic
// path under /var/lib/libvirt/storage/<pool>, so backup artifacts are addressed
// there as plain files. iSCSI exposes raw block devices (no object namespace) and
// is therefore NOT an object store.
func (b *sanBackend) fsDelegate() (*localBackend, error) {
	if b.cfg.Type == TypeISCSI {
		return nil, fmt.Errorf("storage: iscsi backend does not support object operations (raw block target)")
	}
	mount := "/var/lib/libvirt/storage/" + sanitizeName(b.poolName)
	return &localBackend{base: mount}, nil
}

// PutObject stores key under the mounted NFS/SMB pool path.
func (b *sanBackend) PutObject(ctx context.Context, key string, r io.Reader, size int64) (int64, error) {
	d, err := b.fsDelegate()
	if err != nil {
		return 0, err
	}
	return d.PutObject(ctx, key, r, size)
}

// GetObject reads key from the mounted NFS/SMB pool path.
func (b *sanBackend) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	d, err := b.fsDelegate()
	if err != nil {
		return nil, err
	}
	return d.GetObject(ctx, key)
}

// ListObjects lists artifacts under prefix on the mounted NFS/SMB pool path.
func (b *sanBackend) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	d, err := b.fsDelegate()
	if err != nil {
		return nil, err
	}
	return d.ListObjects(ctx, prefix)
}

// DeleteObject removes key from the mounted NFS/SMB pool path.
func (b *sanBackend) DeleteObject(ctx context.Context, key string) error {
	d, err := b.fsDelegate()
	if err != nil {
		return err
	}
	return d.DeleteObject(ctx, key)
}

// renderPoolXML builds libvirt <pool type='netfs'|'iscsi'|...> XML for a SAN/NAS
// backend. The pool target path is a deterministic mountpoint under /var/lib/libvirt.
func renderPoolXML(cfg Config, poolName string) string {
	var sb strings.Builder
	mountPath := "/var/lib/libvirt/storage/" + sanitizeName(poolName)
	switch cfg.Type {
	case TypeNFS:
		// netfs pool: <source><host name='SERVER'/><dir path='/export'/><format type='nfs'/>
		fmt.Fprintf(&sb, "<pool type='netfs'>\n")
		fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEsc(poolName))
		fmt.Fprintf(&sb, "  <source>\n")
		fmt.Fprintf(&sb, "    <host name='%s'/>\n", xmlEsc(hostOnly(cfg.Endpoint)))
		fmt.Fprintf(&sb, "    <dir path='%s'/>\n", xmlEsc(cfg.Target))
		fmt.Fprintf(&sb, "    <format type='nfs'/>\n")
		fmt.Fprintf(&sb, "  </source>\n")
		fmt.Fprintf(&sb, "  <target><path>%s</path></target>\n", xmlEsc(mountPath))
		fmt.Fprintf(&sb, "</pool>\n")
	case TypeSMB:
		// netfs pool with cifs format: <source><host name='SERVER'/><dir path='/share'/><format type='cifs'/>
		fmt.Fprintf(&sb, "<pool type='netfs'>\n")
		fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEsc(poolName))
		fmt.Fprintf(&sb, "  <source>\n")
		fmt.Fprintf(&sb, "    <host name='%s'/>\n", xmlEsc(smbHost(cfg.Endpoint, cfg.Target)))
		fmt.Fprintf(&sb, "    <dir path='%s'/>\n", xmlEsc(smbShare(cfg.Target)))
		fmt.Fprintf(&sb, "    <format type='cifs'/>\n")
		fmt.Fprintf(&sb, "  </source>\n")
		fmt.Fprintf(&sb, "  <target><path>%s</path></target>\n", xmlEsc(mountPath))
		fmt.Fprintf(&sb, "</pool>\n")
	case TypeISCSI:
		// iscsi pool: <source><host name='PORTAL' port='3260'/><device path='IQN'/>
		host, port := hostPort(cfg.Endpoint, "3260")
		fmt.Fprintf(&sb, "<pool type='iscsi'>\n")
		fmt.Fprintf(&sb, "  <name>%s</name>\n", xmlEsc(poolName))
		fmt.Fprintf(&sb, "  <source>\n")
		fmt.Fprintf(&sb, "    <host name='%s' port='%s'/>\n", xmlEsc(host), xmlEsc(port))
		fmt.Fprintf(&sb, "    <device path='%s'/>\n", xmlEsc(cfg.Target))
		fmt.Fprintf(&sb, "  </source>\n")
		fmt.Fprintf(&sb, "  <target><path>/dev/disk/by-path</path></target>\n")
		fmt.Fprintf(&sb, "</pool>\n")
	}
	return sb.String()
}

// derivePoolName builds a deterministic, libvirt-safe pool name from the config.
func derivePoolName(cfg Config) string {
	base := "unihv-" + string(cfg.Type) + "-" + hostOnly(cfg.Endpoint)
	return sanitizeName(base)
}

// hostOnly strips any scheme / path / port from a host string.
func hostOnly(s string) string {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "smb://")
	s = strings.TrimPrefix(s, "cifs://")
	s = strings.TrimPrefix(s, "nfs://")
	s = strings.TrimPrefix(s, "//")
	if i := strings.IndexAny(s, "/\\"); i >= 0 {
		s = s[:i]
	}
	if i := strings.LastIndex(s, ":"); i >= 0 {
		s = s[:i]
	}
	return s
}

// hostPort splits "host" or "host:port" returning the port default when absent.
func hostPort(s, def string) (host, port string) {
	s = strings.TrimSpace(s)
	if i := strings.LastIndex(s, ":"); i >= 0 {
		return s[:i], s[i+1:]
	}
	return s, def
}

// smbHost resolves the SMB server: prefer the explicit endpoint, else parse a UNC
// target like \\server\share or //server/share.
func smbHost(endpoint, target string) string {
	if h := hostOnly(endpoint); h != "" {
		return h
	}
	t := strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")
	t = strings.TrimPrefix(t, "//")
	if i := strings.Index(t, "/"); i >= 0 {
		return t[:i]
	}
	return t
}

// smbShare resolves the SMB share path (a leading-slash dir) from a UNC or a bare
// share name.
func smbShare(target string) string {
	t := strings.ReplaceAll(strings.TrimSpace(target), "\\", "/")
	t = strings.TrimPrefix(t, "//")
	if i := strings.Index(t, "/"); i >= 0 {
		t = t[i+1:]
	}
	t = strings.TrimPrefix(t, "/")
	return "/" + t
}

// sanitizeName keeps only chars libvirt accepts in a pool name.
func sanitizeName(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			sb.WriteRune(r)
		default:
			sb.WriteRune('-')
		}
	}
	out := sb.String()
	if out == "" {
		out = "unihv-pool"
	}
	return out
}

// xmlEsc escapes a string for inclusion in XML text/attribute content.
func xmlEsc(s string) string {
	var sb strings.Builder
	_ = xml.EscapeText(&sb, []byte(s))
	return sb.String()
}

// parseLibvirtEndpoint maps an endpoint string to a net.Dial (network, address)
// pair, matching live_libvirt.parseEndpoint semantics.
func parseLibvirtEndpoint(endpoint string) (network, addr string) {
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

// sanitizeLibvirtErr returns a libvirt error message safe to surface (libvirt
// errors never carry our secret; this is defense-in-depth + trims noise).
func sanitizeLibvirtErr(err error) error {
	if err == nil {
		return nil
	}
	var le libvirt.Error
	if as(err, &le) {
		return fmt.Errorf("%s", strings.TrimSpace(le.Message))
	}
	return err
}

func as(err error, target *libvirt.Error) bool {
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
