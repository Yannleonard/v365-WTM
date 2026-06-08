// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
package hyperv

import (
	"errors"
	"io"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// liveLikeBackend wraps the in-memory sim fake but reports isLive()==true, so it
// exercises the LIVE ExportVM code path (the liveExportHook delegation) WITHOUT a real
// Hyper-V host. It lets us prove the non-regression guarantee from travaux.md §7.4: on
// the live path, ExportVM must NEVER return the sim placeholder bytes — it returns
// either a real stream (windows build, hook installed) or a CLEAR error (cross-platform
// build, hook nil). It must not fabricate a stub as SUCCESS.
type liveLikeBackend struct{ *simBackend }

func (liveLikeBackend) isLive() bool { return true }

// placeholderMarker is the magic prefix the SIM (test-only) placeholder stream begins
// with. The live path must never emit it.
const placeholderMarker = "HYPERVEXPORT"

// TestExportVM_LivePathNeverPlaceholder asserts that when the backend is live, ExportVM
// does not return the fabricated sim placeholder. On the default cross-platform build
// (where liveExportHook is nil because live_windows.go is not compiled) it must return a
// clear ErrUnsupported. On the windows build (hook installed) it returns a real stream
// or a clear error — but in NO case the placeholder bytes.
func TestExportVM_LivePathNeverPlaceholder(t *testing.T) {
	p := New("hyperv-live-gate", WithBackend(liveLikeBackend{newSimBackend()}))
	defer p.Close()

	// Grab a real VM id from the seeded inventory.
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil || len(vms) == 0 {
		t.Fatalf("ListVMs: err=%v n=%d", err, len(vms))
	}
	id := vms[0].ID

	rc, info, err := p.ExportVM(t.Context(), id, vp.DiskVHDX)

	if liveExportHook == nil {
		// Cross-platform build: the live VHDX exporter is not compiled in, so the live
		// path MUST hard-error (never a placeholder), per §7.4.
		if !errors.Is(err, vp.ErrUnsupported) {
			t.Fatalf("live ExportVM without hook must return ErrUnsupported, got rc=%v info=%v err=%v", rc, info, err)
		}
		if rc != nil {
			t.Fatal("live ExportVM without hook must return a nil stream (no placeholder)")
		}
		return
	}

	// Windows build (hook installed): a real stream or a clear error — but if a stream is
	// returned it must NOT be the sim placeholder.
	if err != nil {
		return // a clear error is an acceptable, honest outcome
	}
	defer rc.Close()
	data, rerr := io.ReadAll(rc)
	if rerr != nil {
		return // streaming error is honest, not a fabricated success
	}
	if strings.HasPrefix(string(data), placeholderMarker) {
		t.Fatalf("live ExportVM returned the SIM placeholder bytes — forbidden by §7.4")
	}
	if info == nil || info.SourceVMID != id {
		t.Fatalf("live ExportVM ExportInfo missing/incorrect: %+v", info)
	}
}

// TestExportVM_SimPathPlaceholderAllowed documents the converse: the SIM (isLive==false)
// path IS allowed to return its placeholder, but only for tests, and it must be a real,
// non-empty, correctly-described stream.
func TestExportVM_SimPathPlaceholderAllowed(t *testing.T) {
	p := New("hyperv-sim-export")
	defer p.Close()
	vms, err := p.ListVMs(t.Context(), vp.ListOptions{})
	if err != nil || len(vms) == 0 {
		t.Fatalf("ListVMs: err=%v n=%d", err, len(vms))
	}
	id := vms[0].ID
	rc, info, err := p.ExportVM(t.Context(), id, vp.DiskVHDX)
	if err != nil {
		t.Fatalf("sim ExportVM: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	if len(data) == 0 {
		t.Fatal("sim ExportVM produced empty stream")
	}
	if info == nil || info.SourceVMID != id {
		t.Fatalf("sim ExportInfo missing/incorrect: %+v", info)
	}
}
