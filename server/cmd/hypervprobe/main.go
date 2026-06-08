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
	"strings"
	"time"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/hyperv"
)

func main() {
	ctx := context.Background()

	fmt.Println("== UniHV Hyper-V live probe (real WMI root\\virtualization\\v2 via go-ole/COM) ==")

	// REMOTE vs LOCAL: env UNIHV_HV_HOST selects the WMI server (empty = local "."),
	// UNIHV_HV_USER / UNIHV_HV_PASS supply DCOM credentials for a remote host. With no
	// host set this probes the LOCAL Hyper-V (integrated auth, no credentials).
	host := os.Getenv("UNIHV_HV_HOST")
	user := os.Getenv("UNIHV_HV_USER")
	pass := os.Getenv("UNIHV_HV_PASS")
	if host == "" {
		fmt.Println("\n[Connect] LOCAL host (computerName=\".\", integrated auth)")
	} else {
		fmt.Printf("\n[Connect] REMOTE host %q via DCOM ConnectServer (user=%q)\n", host, user)
	}

	p, err := hyperv.NewLiveRemote("hyperv-probe", host, user, pass)
	if err != nil {
		fmt.Println("NewLiveRemote FAILED:", err)
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

	// ---------------------------------------------------------------------------
	// EXTENSION FEATURES: console endpoint, virtual switch write, VHD storage.
	// ---------------------------------------------------------------------------
	fmt.Println("\n== EXTENSION FEATURES (console / network write / storage write) ==")

	// (E1) Console endpoint (RDP/VMConnect). Requires a VM; reuse an existing one or
	// spin up a throwaway VM purely to resolve its console endpoint, then remove it.
	if cp, ok := interface{}(p).(vp.ConsoleProvider); ok {
		vms, _ := p.ListVMs(ctx, vp.ListOptions{})
		var vmID, vmName, tempID string
		if len(vms) > 0 {
			vmID, vmName = vms[0].ID, vms[0].Name
		} else {
			const cname = "unihv-probe-console"
			if _, err := p.CreateVM(ctx, vp.VMSpec{Name: cname, VCPUs: 1, MemoryMB: 512, Firmware: vp.FirmwareUEFI}); err == nil {
				time.Sleep(1200 * time.Millisecond)
				now, _ := p.ListVMs(ctx, vp.ListOptions{})
				for _, v := range now {
					if v.Name == cname {
						vmID, vmName, tempID = v.ID, v.Name, v.ID
					}
				}
			}
		}
		if vmID != "" {
			ep, err := cp.Console(ctx, vmID)
			if err != nil {
				fmt.Println("[Console] FAILED:", err)
			} else {
				fmt.Printf("[Console] vm=%q -> kind=%s host=%q port=%d (hand to VMConnect/RDP client)\n",
					vmName, ep.Kind, ep.Host, ep.Port)
			}
		} else {
			fmt.Println("[Console] could not resolve a VM to query (skipped)")
		}
		if tempID != "" {
			_, _ = p.DeleteVM(ctx, tempID, vp.DeleteOptions{Force: true})
			time.Sleep(800 * time.Millisecond)
		}
	}

	// (E2) Virtual switch create + list + delete via Msvm_VirtualEthernetSwitchManagementService.
	nw, nwOK := interface{}(p).(vp.NetworkWriter)
	if nwOK {
		const swName = "unihv-probe-switch"
		fmt.Printf("\n[CreateNetwork] DefineSystem private switch %q via WMI ...\n", swName)
		if _, err := nw.CreateNetwork(ctx, vp.NetworkSpec{Name: swName, Type: "isolated"}); err != nil {
			fmt.Println("CreateNetwork FAILED:", err)
			os.Exit(1)
		}
		nets, _ := p.ListNetworks(ctx)
		var swID string
		for _, n := range nets {
			if n.Name == swName {
				swID = n.ID
			}
		}
		fmt.Printf("[ListNetworks] %d switch(es); created present=%v id=%q\n", len(nets), swID != "", swID)
		if swID == "" {
			fmt.Println("PROOF FAILED: created switch not visible via WMI")
			os.Exit(1)
		}
		fmt.Printf("[DeleteNetwork] DestroySystem switch %q via WMI ...\n", swID)
		if _, err := nw.DeleteNetwork(ctx, swID); err != nil {
			fmt.Println("DeleteNetwork FAILED:", err)
			os.Exit(1)
		}
		nets2, _ := p.ListNetworks(ctx)
		still := false
		for _, n := range nets2 {
			if n.Name == swName {
				still = true
			}
		}
		fmt.Printf("[ListNetworks:after-delete] %d switch(es); created present=%v\n", len(nets2), still)
		if still {
			fmt.Println("PROOF FAILED: switch still present after DestroySystem")
			os.Exit(1)
		}
		fmt.Println("PROOF: real WMI virtual-switch create/list/delete round-trip succeeded.")
	}

	// (E3) VHD create + list + delete via Msvm_ImageManagementService.
	sp, spOK := interface{}(p).(vp.StorageProvider)
	if spOK {
		dir := os.Getenv("UNIHV_HV_VHDDIR")
		if dir == "" {
			dir = os.TempDir() // local FS dir on the host; works for VHD create
		}
		const vhdName = "unihv-probe-disk.vhdx"
		full := dir
		if !strings.HasSuffix(full, "\\") && !strings.HasSuffix(full, "/") {
			full += "\\"
		}
		full += vhdName
		fmt.Printf("\n[CreateVolume] CreateVirtualHardDisk %q (4GB dynamic VHDX) via WMI ...\n", full)
		if _, err := sp.CreateVolume(ctx, vp.VolumeSpec{Name: vhdName, StorageID: dir, CapacityGB: 4, Format: vp.DiskVHDX}); err != nil {
			fmt.Println("CreateVolume FAILED:", err)
			os.Exit(1)
		}
		vols, err := sp.ListVolumes(ctx, dir)
		if err != nil {
			fmt.Println("ListVolumes FAILED:", err)
			os.Exit(1)
		}
		found := false
		for _, v := range vols {
			if strings.EqualFold(v.Name, vhdName) {
				found = true
				fmt.Printf("[ListVolumes] found %q capGB=%.1f allocGB=%.3f isIso=%v\n", v.Name, v.CapacityGB, v.AllocGB, v.IsISO)
			}
		}
		if !found {
			fmt.Println("PROOF FAILED: created VHD not visible via ListVolumes")
			os.Exit(1)
		}
		fmt.Printf("[DeleteVolume] removing %q ...\n", full)
		if _, err := sp.DeleteVolume(ctx, dir, full); err != nil {
			fmt.Println("DeleteVolume FAILED:", err)
			os.Exit(1)
		}
		vols2, _ := sp.ListVolumes(ctx, dir)
		still := false
		for _, v := range vols2 {
			if strings.EqualFold(v.Name, vhdName) {
				still = true
			}
		}
		fmt.Printf("[ListVolumes:after-delete] created present=%v\n", still)
		if still {
			fmt.Println("PROOF FAILED: VHD still present after delete")
			os.Exit(1)
		}
		fmt.Println("PROOF: real WMI VHD create/list/delete round-trip succeeded.")
	}

	fmt.Println("\nOK: extension features (console + switch + VHD) exercised against real WMI.")
}
