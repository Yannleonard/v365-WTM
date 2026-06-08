package storage

// object.go adds OBJECT OPERATIONS (Put/Get/List/Delete) to the storage backends
// so they can be used as real BACKUP TARGETS by the VM backup engine
// (server/internal/backup). Each backend stores backup artifacts (a converted
// qcow2 disk image plus a small JSON manifest) under a keyed path
// (vm/<vmId>/<timestamp>/<disk>.qcow2). The connectivity-test surface (Test) is
// unchanged; this only adds the data plane.
//
// Two families implement it:
//   - cloud object stores (S3 / Azure Blob) via the existing minimal stdlib REST
//     clients (SigV4 / SharedKey) — added in s3.go / azure.go;
//   - filesystem targets (SAN/NAS mountpoint or a plain local directory) via the
//     localBackend in local.go (a SAN/NAS pool, once mounted by libvirt, is just a
//     directory; for backups we address it directly as a filesystem path).
//
// The interface is intentionally small and stream-oriented so a multi-GB disk
// image never has to be buffered fully in memory.

import (
	"context"
	"io"
)

// ObjectInfo describes one stored object (a backup artifact).
type ObjectInfo struct {
	Key       string `json:"key"`
	SizeBytes int64  `json:"sizeBytes"`
}

// ObjectStore is the data-plane contract a backend implements to be usable as a
// backup target. Keys use forward slashes as separators regardless of backend.
type ObjectStore interface {
	// PutObject stores r under key, returning the number of bytes written. size is
	// the expected length (-1 if unknown); backends that need a content length
	// (Azure block blob single-shot) use it, others may ignore it.
	PutObject(ctx context.Context, key string, r io.Reader, size int64) (int64, error)
	// GetObject opens key for reading. The caller closes the reader.
	GetObject(ctx context.Context, key string) (io.ReadCloser, error)
	// ListObjects returns every object whose key starts with prefix.
	ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error)
	// DeleteObject removes key (idempotent: a missing key is not an error).
	DeleteObject(ctx context.Context, key string) error
}

// AsObjectStore returns the backend as an ObjectStore if it supports the data
// plane, else (nil,false). All current backends implement it EXCEPT the iSCSI
// SAN family (a raw block target has no filesystem namespace for objects).
func AsObjectStore(b Backend) (ObjectStore, bool) {
	os, ok := b.(ObjectStore)
	return os, ok
}
