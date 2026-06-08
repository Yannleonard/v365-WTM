//go:build windows

// Command hypervprobe is a Windows-only smoke test that proves the REAL Hyper-V
// provider talks to the live host through direct COM/WMI on root\virtualization\v2
// (Msvm_* classes via github.com/go-ole/go-ole) — NOT PowerShell, NOT a mock.
//
// It: constructs the live provider (NewLive), prints HealthCheck + ListHosts +
// ListVMs (read path: Msvm_ComputerSystem / Msvm_*SettingData), then exercises the
// WMI WRITE path by DefineSystem-ing a throwaway VM (Msvm_VirtualSystemManagementService.
// DefineSystem), listing it back through WMI, and DestroySystem-ing it again. Every
// step goes through the go-ole COM code in the hyperv package.
//
// Build (cross-compile from the Linux Docker container, CGO-free):
//
//	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o hypervprobe.exe ./server/cmd/hypervprobe
//
// Run on the Windows host:
//
//	.\hypervprobe.exe
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/hyperv"
)

func main() {
	ctx := context.Background()

	fmt.Println("== UniHV Hyper-V live probe (real WMI root\\virtualization\\v2 via go-ole/COM) ==")

	p, err := hyperv.NewLive("hyperv-local", "")
	if err != nil {
		fmt.Println("NewLive FAILED:", err)
		os.Exit(1)
	}
	defer p.Close()

	// 1) Health (queries Msvm_VirtualSystemManagementService over WMI).
	h, _ := p.HealthCheck(ctx)
	fmt.Printf("\n[HealthCheck] healthy=%v version=%q\n", h.Healthy, h.Version)
	if !h.Healthy {
		fmt.Println("host not healthy; aborting")
		os.Exit(1)
	}

	// 2) Hosts (the host-role Msvm_ComputerSystem).
	hosts, err := p.ListHosts(ctx)
	if err != nil {
		fmt.Println("ListHosts FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("\n[ListHosts] %d host(s):\n", len(hosts))
	for _, x := range hosts {
		fmt.Printf("  - id=%q name=%q cpuCores=%d state=%s version=%q\n",
			x.ID, x.Name, x.CPUCores, x.State, x.Version)
	}

	// 3) VMs before (Msvm_ComputerSystem + associated *SettingData).
	before, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		fmt.Println("ListVMs FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("\n[ListVMs:before] %d VM(s):\n", len(before))
	for _, v := range before {
		fmt.Printf("  - %s state=%s(raw=%s) vcpu=%d memMB=%d\n", v.Name, v.State, v.StateRaw, v.VCPUs, v.MemoryMB)
	}

	// 4) WRITE path: create a throwaway VM via Msvm_VirtualSystemManagementService.DefineSystem.
	const throwaway = "unihv-probe"
	fmt.Printf("\n[CreateVM] DefineSystem %q via WMI ...\n", throwaway)
	if _, err := p.CreateVM(ctx, vp.VMSpec{
		Name:     throwaway,
		VCPUs:    1,
		MemoryMB: 512,
		Firmware: vp.FirmwareUEFI,
		Disks:    []vp.DiskSpec{},
	}); err != nil {
		fmt.Println("CreateVM FAILED:", err)
		os.Exit(1)
	}
	time.Sleep(1500 * time.Millisecond) // let WMI realize the object

	// 5) List again — the throwaway must now appear (read back through WMI).
	after, err := p.ListVMs(ctx, vp.ListOptions{})
	if err != nil {
		fmt.Println("ListVMs(after) FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("\n[ListVMs:after-create] %d VM(s):\n", len(after))
	var created *vp.VM
	for i := range after {
		v := after[i]
		marker := ""
		if v.Name == throwaway {
			marker = "  <-- created via WMI DefineSystem"
			created = &after[i]
		}
		fmt.Printf("  - %s id=%s state=%s(raw=%s)%s\n", v.Name, v.ID, v.State, v.StateRaw, marker)
	}
	if created == nil {
		fmt.Println("PROOF FAILED: throwaway VM did not appear via WMI after DefineSystem")
		os.Exit(1)
	}
	fmt.Printf("\nPROOF: WMI created %q (Msvm_ComputerSystem.Name=%s) — visible through go-ole read path.\n",
		throwaway, created.ID)

	// 6) Cleanup: DestroySystem the throwaway via WMI.
	fmt.Printf("\n[DeleteVM] DestroySystem %q via WMI ...\n", created.ID)
	if _, err := p.DeleteVM(ctx, created.ID, vp.DeleteOptions{Force: true}); err != nil {
		fmt.Println("DeleteVM FAILED:", err)
		os.Exit(1)
	}
	time.Sleep(1000 * time.Millisecond)

	final, _ := p.ListVMs(ctx, vp.ListOptions{})
	stillThere := false
	for _, v := range final {
		if v.Name == throwaway {
			stillThere = true
		}
	}
	fmt.Printf("\n[ListVMs:after-delete] %d VM(s); throwaway present=%v\n", len(final), stillThere)
	if stillThere {
		fmt.Println("PROOF FAILED: throwaway VM still present after DestroySystem")
		os.Exit(1)
	}

	fmt.Println("\nOK: real WMI Msvm_* create/list/remove round-trip succeeded via go-ole COM.")
}
