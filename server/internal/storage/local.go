package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// localBackend is a plain LOCAL FILESYSTEM storage backend rooted at a base
// directory (cfg.Target). It needs no credentials and no libvirt — it is the
// simplest real backup target (and also models an already-mounted SAN/NAS path).
// Object keys map to paths under base/, with "/" as the separator on every OS.
type localBackend struct {
	base string
}

func newLocalBackend(cfg Config) (Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &localBackend{base: filepath.Clean(strings.TrimSpace(cfg.Target))}, nil
}

func (b *localBackend) Type() Type { return TypeLocal }

// Test verifies the base directory exists (creating it if needed) and is
// writable, by creating + removing a probe file.
func (b *localBackend) Test(ctx context.Context) error {
	if err := os.MkdirAll(b.base, 0o755); err != nil {
		return fmt.Errorf("local: create base dir: %w", err)
	}
	probe := filepath.Join(b.base, ".unihv-write-probe")
	if err := os.WriteFile(probe, []byte("ok"), 0o600); err != nil {
		return fmt.Errorf("local: base dir not writable: %w", err)
	}
	_ = os.Remove(probe)
	return nil
}

// resolve maps an object key to an absolute filesystem path under base, rejecting
// any key that would escape the base directory (path traversal defense).
func (b *localBackend) resolve(key string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash("/" + strings.TrimPrefix(key, "/")))
	p := filepath.Join(b.base, clean)
	rel, err := filepath.Rel(b.base, p)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("local: invalid key %q", key)
	}
	return p, nil
}

// PutObject writes r to base/key, creating parent directories. size is ignored
// (the filesystem does not need a content length).
func (b *localBackend) PutObject(ctx context.Context, key string, r io.Reader, size int64) (int64, error) {
	p, err := b.resolve(key)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return 0, fmt.Errorf("local: mkdir: %w", err)
	}
	f, err := os.Create(p)
	if err != nil {
		return 0, fmt.Errorf("local: create object: %w", err)
	}
	n, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		_ = os.Remove(p)
		return n, fmt.Errorf("local: write object: %w", copyErr)
	}
	if closeErr != nil {
		return n, fmt.Errorf("local: close object: %w", closeErr)
	}
	return n, nil
}

// GetObject opens base/key for reading.
func (b *localBackend) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	p, err := b.resolve(key)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("local: object not found: %s", key)
		}
		return nil, fmt.Errorf("local: open object: %w", err)
	}
	return f, nil
}

// ListObjects walks base and returns every file whose slash-key starts with
// prefix.
func (b *localBackend) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	err := filepath.Walk(b.base, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(b.base, path)
		if err != nil {
			return nil
		}
		key := filepath.ToSlash(rel)
		if key == ".unihv-write-probe" {
			return nil
		}
		if prefix == "" || strings.HasPrefix(key, prefix) {
			out = append(out, ObjectInfo{Key: key, SizeBytes: info.Size()})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("local: list objects: %w", err)
	}
	return out, nil
}

// DeleteObject removes base/key (a missing key is not an error). It also prunes
// now-empty parent directories up to base.
func (b *localBackend) DeleteObject(ctx context.Context, key string) error {
	p, err := b.resolve(key)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("local: delete object: %w", err)
	}
	// best-effort prune empty dirs back toward base
	dir := filepath.Dir(p)
	for dir != b.base && strings.HasPrefix(dir, b.base) {
		if err := os.Remove(dir); err != nil {
			break // not empty or gone
		}
		dir = filepath.Dir(dir)
	}
	return nil
}
