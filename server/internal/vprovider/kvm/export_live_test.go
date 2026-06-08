package kvm

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// fakeExtBackend embeds the seeded sim backend (so getDomain & the rest of the core
// libvirtBackend seam work) and additionally satisfies extBackend. Satisfying
// extBackend is precisely what marks a backend as "LIVE" to Provider.ExportVM, so a
// Provider wired with this fake takes the live path. Its exportDisk returns
// errNoRealDisk — the exact condition that previously triggered the fabricated
// "KVMEXPORT" placeholder. The test asserts that no longer happens.
type fakeExtBackend struct {
	*simBackend
	exportErr error
}

func (f *fakeExtBackend) exportDisk(uuid string, format vp.DiskFormat) (io.ReadCloser, int64, error) {
	return nil, 0, f.exportErr
}

// remaining extBackend methods — unused by this test, present only to satisfy the seam.
func (f *fakeExtBackend) console(string) (*vp.ConsoleEndpoint, error) { return nil, vp.ErrUnsupported }
func (f *fakeExtBackend) createNetwork(vp.NetworkSpec) error          { return vp.ErrUnsupported }
func (f *fakeExtBackend) deleteNetwork(string) error                  { return vp.ErrUnsupported }
func (f *fakeExtBackend) listVolumes(string) ([]vp.Volume, error)     { return nil, vp.ErrUnsupported }
func (f *fakeExtBackend) createVolume(vp.VolumeSpec) error            { return vp.ErrUnsupported }
func (f *fakeExtBackend) deleteVolume(string, string) error           { return vp.ErrUnsupported }
func (f *fakeExtBackend) uploadISO(string, string, int64, io.Reader) (*vp.Volume, error) {
	return nil, vp.ErrUnsupported
}
func (f *fakeExtBackend) attachDisk(string, vp.DiskSpec) error        { return vp.ErrUnsupported }
func (f *fakeExtBackend) detachDisk(string, string) error             { return vp.ErrUnsupported }
func (f *fakeExtBackend) attachNIC(string, vp.NICSpec) error          { return vp.ErrUnsupported }
func (f *fakeExtBackend) detachNIC(string, string) error              { return vp.ErrUnsupported }
func (f *fakeExtBackend) mountISO(string, string) error               { return vp.ErrUnsupported }
func (f *fakeExtBackend) unmountISO(string) error                     { return vp.ErrUnsupported }
func (f *fakeExtBackend) guestInfo(string) (*vp.GuestInfo, error)     { return nil, vp.ErrUnsupported }
func (f *fakeExtBackend) guestShutdown(string) (bool, error)          { return false, vp.ErrUnsupported }
func (f *fakeExtBackend) deleteSnapshot(string, string) error         { return vp.ErrUnsupported }
func (f *fakeExtBackend) resizeDisk(string, string, float64) error    { return vp.ErrUnsupported }
func (f *fakeExtBackend) setResources(string, vp.ResourceSpec) error  { return vp.ErrUnsupported }
func (f *fakeExtBackend) setDiskQoS(string, string, vp.DiskQoS) error { return vp.ErrUnsupported }
func (f *fakeExtBackend) migrateStorage(string, string, string) error { return vp.ErrUnsupported }

// TestLiveExportNoFakeOnNonFileDisk proves the owner's guarantee: on the LIVE path,
// when the disk is NOT file-backed (exportDisk -> errNoRealDisk), ExportVM returns a
// HARD ERROR (vp.ErrUnsupported) and NEVER the fabricated "KVMEXPORT" placeholder.
func TestLiveExportNoFakeOnNonFileDisk(t *testing.T) {
	p := New("kvm-live-fake",
		WithBackend(&fakeExtBackend{simBackend: newSimBackend(), exportErr: errNoRealDisk}),
		WithCaps(LiveCaps),
	)
	rc, info, err := p.ExportVM(context.Background(), "dom-seed-1", vp.DiskQcow2)
	if rc != nil {
		data, _ := io.ReadAll(rc)
		_ = rc.Close()
		if strings.Contains(string(data), "KVMEXPORT") {
			t.Fatalf("live ExportVM fabricated a KVMEXPORT placeholder: %q", data)
		}
		t.Fatalf("live ExportVM returned a stream on a non-file disk; expected error. info=%+v", info)
	}
	if err == nil {
		t.Fatal("live ExportVM on non-file disk must return an error, got nil")
	}
	if !errors.Is(err, vp.ErrUnsupported) {
		t.Fatalf("expected vp.ErrUnsupported, got %v", err)
	}
}

// TestLiveExportRealDiskStillStreams proves the file-backed real export path is
// untouched: when exportDisk succeeds, ExportVM returns that real stream verbatim
// (no placeholder), with the size reported by the backend.
func TestLiveExportRealDiskStillStreams(t *testing.T) {
	real := io.NopCloser(strings.NewReader("REAL-QCOW2-BYTES"))
	fb := &fakeExtBackend{simBackend: newSimBackend()}
	fb.exportErr = nil
	// Wrap to return a real stream on success.
	p := New("kvm-live-real", WithBackend(&realExportBackend{fakeExtBackend: fb, rc: real, size: 16}), WithCaps(LiveCaps))
	rc, info, err := p.ExportVM(context.Background(), "dom-seed-1", vp.DiskQcow2)
	if err != nil {
		t.Fatalf("ExportVM: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if string(data) != "REAL-QCOW2-BYTES" {
		t.Fatalf("expected real stream verbatim, got %q", data)
	}
	if strings.Contains(string(data), "KVMEXPORT") {
		t.Fatal("real export must not contain the placeholder marker")
	}
	if info.SizeBytes != 16 {
		t.Fatalf("expected backend-reported size 16, got %d", info.SizeBytes)
	}
}

type realExportBackend struct {
	*fakeExtBackend
	rc   io.ReadCloser
	size int64
}

func (r *realExportBackend) exportDisk(string, vp.DiskFormat) (io.ReadCloser, int64, error) {
	return r.rc, r.size, nil
}
