//go:build libvirt_live

// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// This file is compiled ONLY under `-tags libvirt_live`. It is the seam where a
// real, pure-Go libvirt RPC socket client (github.com/digitalocean/go-libvirt —
// no cgo) would be wired into the existing pure-Go normalization core. Per D-005
// the default (CGO_ENABLED=0, distroless) build never compiles this file and never
// imports go-libvirt, so go.mod stays clean. It is intentionally a stub: bringing
// up a live connection requires libvirtd reachable over its socket/TLS endpoint,
// which is not available in hardware-free CI.
//
// To make this real, replace the body of liveBackend's methods with calls into a
// go-libvirt *libvirt.Libvirt connected via net.Dial("unix",
// "/var/run/libvirt/libvirt-sock"), translating virDomainGetState / domain XML /
// virStoragePool / virNetwork into the libvirt* model structs the core already
// normalizes. No change to kvm.go / libvirt.go is needed — only this file.
package kvm

import (
	"errors"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// errLiveUnavailable is returned until a real go-libvirt connection is wired here.
var errLiveUnavailable = errors.New("kvm: live libvirt backend not implemented in this build")

// liveBackend would hold the pure-Go go-libvirt connection. Stubbed.
type liveBackend struct {
	uri string
}

// NewLive constructs a Provider backed by a live libvirt connection at uri (e.g.
// "qemu+tcp://host/system" or a unix socket). Available only under libvirt_live.
func NewLive(id, uri string, opts ...Option) (*Provider, error) {
	be := &liveBackend{uri: uri}
	if !be.healthy() {
		return nil, errLiveUnavailable
	}
	opts = append(opts, WithBackend(be))
	return New(id, opts...), nil
}

func (l *liveBackend) version() string { return "" }
func (l *liveBackend) healthy() bool   { return false } // stub: never healthy
func (l *liveBackend) close() error    { return nil }

func (l *liveBackend) listNodes() []*libvirtNode             { return nil }
func (l *liveBackend) getNode(string) (*libvirtNode, bool)   { return nil, false }
func (l *liveBackend) listDomains() []*libvirtDomain         { return nil }
func (l *liveBackend) getDomain(string) (*libvirtDomain, bool) { return nil, false }
func (l *liveBackend) listPools() []*libvirtPool             { return nil }
func (l *liveBackend) listNets() []*libvirtNet               { return nil }

func (l *liveBackend) defineDomain(*libvirtDomain)        {}
func (l *liveBackend) undefineDomain(string)              {}
func (l *liveBackend) setDomainState(string, libvirtState) {}
func (l *liveBackend) domainsOnHost(string) int           { return 0 }

func (l *liveBackend) listSnapshots(string) []vp.Snapshot      { return nil }
func (l *liveBackend) createSnapshot(string, vp.Snapshot)      {}
func (l *liveBackend) setCurrentSnapshot(string, string) bool  { return false }

func (l *liveBackend) hostIDs() []string  { return nil }
func (l *liveBackend) clusterName() string { return "kvm-live" }

var _ libvirtBackend = (*liveBackend)(nil)
