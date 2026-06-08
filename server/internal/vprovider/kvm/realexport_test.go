package kvm

import (
	"context"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// TestRealExportDisk proves ExportVM streams a REAL converted disk image from a
// live libvirt domain via qemu-img. Skips unless UNIHV_LIBVIRT_URI is set and
// qemu-img is present.
func TestRealExportDisk(t *testing.T) {
	uri := os.Getenv("UNIHV_LIBVIRT_URI")
	if uri == "" {
		t.Skip("set UNIHV_LIBVIRT_URI to run the live export test")
	}
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not present")
	}
	ctx := context.Background()
	p, err := NewLiveWithID("kvm-live", uri)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()
	vms, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil || len(vms) == 0 {
		t.Skipf("no VMs (err=%v)", err)
	}
	var id string
	for _, v := range vms {
		if len(v.Disks) > 0 {
			id = v.ID
			break
		}
	}
	if id == "" {
		t.Skip("no disked VM to export")
	}
	rc, info, err := p.ExportVM(ctx, id, vp.DiskVMDK)
	if err != nil {
		t.Fatalf("ExportVM: %v", err)
	}
	defer rc.Close()
	tmp, _ := os.CreateTemp("", "exp-*.vmdk")
	defer os.Remove(tmp.Name())
	n, _ := io.Copy(tmp, rc)
	tmp.Close()
	if n < 1024 {
		t.Fatalf("export too small (%d bytes) - not a real disk", n)
	}
	out, err := exec.Command("qemu-img", "info", tmp.Name()).CombinedOutput()
	if err != nil {
		t.Fatalf("qemu-img info: %v: %s", err, out)
	}
	if !strings.Contains(string(out), "file format: vmdk") {
		t.Fatalf("exported image is not vmdk:\n%s", out)
	}
	t.Logf("ExportVM streamed a REAL %d-byte VMDK (info.SizeBytes=%d); qemu-img confirms vmdk", n, info.SizeBytes)
}
