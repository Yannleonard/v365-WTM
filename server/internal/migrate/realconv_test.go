package migrate_test

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/gtek-it/castor/server/internal/migrate"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/sim"
)

// TestRealQemuImgConversionChain proves the V2V disk converter performs REAL
// format conversions with qemu-img across the full format set, validating each
// output is genuinely that format (qemu-img info). Skips when qemu-img is absent
// (e.g. the CGO-free CI container) — run it where qemu-img is installed (WSL).
func TestRealQemuImgConversionChain(t *testing.T) {
	qemu, err := exec.LookPath("qemu-img")
	if err != nil {
		t.Skip("qemu-img not installed; skipping real-conversion test")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.qcow2")
	if out, err := exec.Command(qemu, "create", "-f", "qcow2", src, "10M").CombinedOutput(); err != nil {
		t.Fatalf("create source qcow2: %v: %s", err, out)
	}
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}

	conv := migrate.QemuImgConverter{}
	// Cross-hypervisor format hops: KVM(qcow2) -> VMware(vmdk) -> Hyper-V(vhdx) ->
	// raw -> back to qcow2. Each output must really be that format.
	chain := []struct{ from, to vp.DiskFormat }{
		{vp.DiskQcow2, vp.DiskVMDK},
		{vp.DiskVMDK, vp.DiskVHDX},
		{vp.DiskVHDX, vp.DiskRaw},
		{vp.DiskRaw, vp.DiskQcow2},
	}
	cur := data
	for _, step := range chain {
		var out bytes.Buffer
		n, err := conv.Convert(context.Background(), bytes.NewReader(cur), &out, step.from, step.to)
		if err != nil {
			t.Fatalf("convert %s->%s: %v", step.from, step.to, err)
		}
		if n == 0 || out.Len() == 0 {
			t.Fatalf("convert %s->%s produced empty output", step.from, step.to)
		}
		// Validate the real format with qemu-img info.
		tmp := filepath.Join(dir, "conv."+string(step.to))
		if err := os.WriteFile(tmp, out.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		info, err := exec.Command(qemu, "info", tmp).CombinedOutput()
		if err != nil {
			t.Fatalf("qemu-img info %s: %v: %s", tmp, err, info)
		}
		want := string(step.to)
		if want == "vhdx" {
			want = "vhdx"
		}
		if !bytes.Contains(info, []byte("file format: "+want)) && !(want == "vhd" && bytes.Contains(info, []byte("vpc"))) {
			t.Errorf("convert %s->%s: qemu-img reports wrong format:\n%s", step.from, step.to, info)
		} else {
			t.Logf("convert %s->%s OK (%d bytes), qemu-img confirms format=%s", step.from, step.to, n, want)
		}
		cur = out.Bytes()
	}
}

// TestRealV2VEndToEnd runs the WHOLE engine (preflight -> export -> real qemu-img
// convert -> import -> power-on) between two providers of different families,
// using the real QemuImgConverter. Skips without qemu-img.
func TestRealV2VEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("qemu-img"); err != nil {
		t.Skip("qemu-img not installed; skipping real V2V e2e")
	}
	// Use the sim providers as the two endpoints (their ExportVM emits a small real
	// byte stream); the REAL part under test is the engine + qemu-img conversion of
	// the exported bytes between vmdk and qcow2 families. (A live two-hypervisor V2V
	// is covered operationally; here we exercise the real converter in the pipeline.)
	reg := newReg(
		sim.New("esxi-1", sim.WithKind(vp.KindVMware)),
		sim.New("kvm-1", sim.WithKind(vp.KindKVM)),
	)
	eng := migrate.New(reg, migrate.QemuImgConverter{})
	prog, err := eng.Run(context.Background(), migrate.Request{
		SourceProviderID: "esxi-1", SourceVMID: "vm-1",
		TargetProviderID: "kvm-1", PowerOnAfter: true,
	})
	if err != nil {
		t.Fatalf("V2V run: %v", err)
	}
	if prog.Phase != migrate.PhaseDone || prog.Percent != 100 {
		t.Fatalf("V2V not done: phase=%s pct=%d err=%s", prog.Phase, prog.Percent, prog.Error)
	}
	if prog.TargetVMID == "" {
		t.Fatal("V2V produced no target VM")
	}
	t.Logf("V2V esxi(vmdk)->kvm(qcow2) done, target=%s", prog.TargetVMID)
}
