// Package storage provides pluggable STORAGE BACKENDS for UniHV: SAN/NAS targets
// (NFS, iSCSI, SMB/CIFS) realized as libvirt storage pools on a target KVM
// provider, and cloud object stores (Azure Blob, AWS S3) accessed via minimal,
// CGO-free, stdlib-only REST clients (Azure SharedKey, AWS SigV4). Each backend
// exposes a single Test(ctx) method that verifies connectivity and returns a
// clear, secret-free error.
//
// This package is self-contained: it does not modify vprovider core. For the
// SAN/NAS family it speaks the libvirt RPC wire protocol directly via
// github.com/digitalocean/go-libvirt (the same pure-Go client live_libvirt.go
// uses), defining + starting a storage pool of the requested type as the
// connectivity probe, then tearing it down (dry-run).
package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Type enumerates the supported storage backend kinds.
type Type string

const (
	TypeNFS       Type = "nfs"
	TypeISCSI     Type = "iscsi"
	TypeSMB       Type = "smb"
	TypeAzureBlob Type = "azureblob"
	TypeS3        Type = "s3"
)

// ValidType reports whether t is a known storage backend type.
func ValidType(t string) bool {
	switch Type(t) {
	case TypeNFS, TypeISCSI, TypeSMB, TypeAzureBlob, TypeS3:
		return true
	default:
		return false
	}
}

// IsSAN reports whether the type is a SAN/NAS family backend (libvirt pool).
func IsSAN(t Type) bool { return t == TypeNFS || t == TypeISCSI || t == TypeSMB }

// IsCloud reports whether the type is a cloud object store backend.
func IsCloud(t Type) bool { return t == TypeAzureBlob || t == TypeS3 }

// ErrUnsupported is returned for an unknown backend type.
var ErrUnsupported = errors.New("storage: unsupported backend type")

// Config is the resolved (plaintext-secret) configuration for a backend. The API
// layer builds this from a persisted store.StorageBackend after opening the sealed
// secret, OR straight from a create/test request body. The secret is never logged.
type Config struct {
	Type Type
	Name string

	// SAN/NAS family.
	Endpoint   string // NFS/SMB server host, iSCSI portal host[:port]
	Target     string // NFS export path, iSCSI IQN, SMB share/UNC
	Username   string // SMB user (NFS/iSCSI none)
	Secret     string // SMB password (plaintext, opened from seal)
	ProviderID string // target KVM provider id (which libvirt host defines the pool)
	PoolName   string // libvirt pool name to use for the probe (derived if empty)

	// Libvirt endpoint override for the SAN/NAS probe. When empty the package
	// uses LibvirtEndpoint resolved by the caller (the registered provider's
	// endpoint). Accepts the same forms as live_libvirt.parseEndpoint.
	LibvirtEndpoint string

	// Cloud family.
	Account   string // Azure storage account name / S3 access key id (also Username)
	Container string // Azure container / S3 bucket (also Target)
	Region    string // S3 region
	ServiceURL string // optional endpoint override (S3-compatible / Azurite)
}

// Backend is a pluggable storage backend that can verify its own connectivity.
type Backend interface {
	// Type returns the backend type.
	Type() Type
	// Test verifies connectivity (mount/define-and-start for SAN/NAS, list for
	// cloud) and returns a clear, secret-free error on failure.
	Test(ctx context.Context) error
}

// New builds a Backend for the given Config. The SAN/NAS family requires a
// reachable libvirt endpoint (cfg.LibvirtEndpoint); the cloud family requires
// account/bucket + credentials.
func New(cfg Config) (Backend, error) {
	switch cfg.Type {
	case TypeNFS, TypeISCSI, TypeSMB:
		return newSANBackend(cfg)
	case TypeAzureBlob:
		return newAzureBackend(cfg)
	case TypeS3:
		return newS3Backend(cfg)
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnsupported, cfg.Type)
	}
}

// Validate checks that a Config has the minimum fields for its type. It returns a
// clear validation error (no secrets) suitable for surfacing to the API as a 422.
func (c Config) Validate() error {
	switch c.Type {
	case TypeNFS:
		if strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(c.Target) == "" {
			return errors.New("nfs requires host (endpoint) and export path (target)")
		}
	case TypeISCSI:
		if strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(c.Target) == "" {
			return errors.New("iscsi requires portal host (endpoint) and target IQN (target)")
		}
	case TypeSMB:
		if strings.TrimSpace(c.Endpoint) == "" || strings.TrimSpace(c.Target) == "" {
			return errors.New("smb requires server (endpoint) and share/UNC (target)")
		}
	case TypeAzureBlob:
		acct := firstNonEmpty(c.Account, c.Username)
		if strings.TrimSpace(acct) == "" || strings.TrimSpace(firstNonEmpty(c.Container, c.Target)) == "" {
			return errors.New("azureblob requires account (username) and container (target)")
		}
		if strings.TrimSpace(c.Secret) == "" {
			return errors.New("azureblob requires an account key")
		}
	case TypeS3:
		if strings.TrimSpace(firstNonEmpty(c.Container, c.Target)) == "" {
			return errors.New("s3 requires a bucket (target)")
		}
		if strings.TrimSpace(firstNonEmpty(c.Account, c.Username)) == "" || strings.TrimSpace(c.Secret) == "" {
			return errors.New("s3 requires an access key id (username) and secret access key")
		}
		if strings.TrimSpace(c.Region) == "" {
			return errors.New("s3 requires a region")
		}
	default:
		return fmt.Errorf("%w: %q", ErrUnsupported, c.Type)
	}
	return nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
