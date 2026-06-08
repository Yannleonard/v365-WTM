// Command kvmprobe PROVES the real, pure-Go libvirt backend against a live
// libvirt 10.0.0 / QEMU. It opens NewLive(socket), prints HealthCheck + ListHosts
// + ListVMs, and — if no domains exist — defines a throwaway TRANSIENT-ish domain
// (defined shut-off, never started), lists it, then deletes it, proving the
// lifecycle write path hits the real libvirt RPC API end to end.
//
// Run inside WSL Ubuntu where the libvirt socket is local:
//
//	wsl -d Ubuntu -u root -- bash -lc \
//	  "cd /mnt/c/.../unihv && go run ./server/cmd/kvmprobe"
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/gtek-it/castor/server/internal/vprovider"
	"github.com/gtek-it/castor/server/internal/vprovider/kvm"
)

func main() {
	socket := "/var/run/libvirt/libvirt-sock"
	if len(os.Args) > 1 {
		socket = os.Args[1]
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	p, err := kvm.NewLive(socket)
	if err != nil {
		fatalf("NewLive(%q): %v", socket, err)
	}
	defer p.Close()

	fmt.Printf("== KVM live probe ==\nprovider id=%s kind=%s socket=%s\n\n", p.ID(), p.Kind(), socket)

	// --- HealthCheck (ConnectGetLibVersion under the hood) ---
	hs, err := p.HealthCheck(ctx)
	if err != nil {
		fatalf("HealthCheck: %v", err)
	}
	fmt.Printf("HealthCheck: healthy=%v version=%q message=%q\n\n", hs.Healthy, hs.Version, hs.Message)
	if !hs.Healthy {
		fatalf("libvirt reported unhealthy; aborting")
	}

	// --- ListHosts (ConnectGetHostname + NodeGetInfo) ---
	hosts, err := p.ListHosts(ctx)
	if err != nil {
		fatalf("ListHosts: %v", err)
	}
	fmt.Printf("ListHosts: %d host(s)\n", len(hosts))
	for _, h := range hosts {
		fmt.Printf("  - id=%s name=%s state=%s cpu=%dc @ %dMHz mem=%dMB ver=%s\n",
			h.ID, h.Name, h.State, h.CPUCores, h.CPUMHz, h.MemoryMB, h.Version)
	}
	fmt.Println()

	// --- ListClusters / ListStorage / ListNetworks ---
	if cls, err := p.ListClusters(ctx); err == nil {
		fmt.Printf("ListClusters: %d\n", len(cls))
		for _, c := range cls {
			fmt.Printf("  - id=%s name=%s hosts=%v\n", c.ID, c.Name, c.HostIDs)
		}
	}
	if pools, err := p.ListStorage(ctx); err == nil {
		fmt.Printf("ListStorage: %d pool(s)\n", len(pools))
		for _, pl := range pools {
			fmt.Printf("  - %s type=%s cap=%.1fGB free=%.1fGB active=%v\n",
				pl.Name, pl.Type, pl.CapacityGB, pl.FreeGB, pl.Accessible)
		}
	}
	if nets, err := p.ListNetworks(ctx); err == nil {
		fmt.Printf("ListNetworks: %d network(s)\n", len(nets))
		for _, n := range nets {
			fmt.Printf("  - %s mode=%s\n", n.Name, n.Type)
		}
	}
	fmt.Println()

	// --- ListVMs (ConnectListAllDomains + DomainGetState/Info/XMLDesc) ---
	vms, err := p.ListVMs(ctx, vprovider.ListOptions{})
	if err != nil {
		fatalf("ListVMs: %v", err)
	}
	fmt.Printf("ListVMs: %d domain(s)\n", len(vms))
	for _, v := range vms {
		fmt.Printf("  - id=%s name=%s state=%s(%s) vcpu=%d mem=%dMB fw=%s disks=%d nics=%d\n",
			v.ID, v.Name, v.State, v.StateRaw, v.VCPUs, v.MemoryMB, v.Firmware, len(v.Disks), len(v.NICs))
	}
	fmt.Println()

	// --- Prove the write path if the host is empty: define + list + delete a
	//     throwaway domain (defined shut-off, never started). ---
	if len(vms) == 0 {
		name := fmt.Sprintf("unihv-kvmprobe-%d", time.Now().Unix())
		fmt.Printf("no domains present -> defining throwaway domain %q via DomainDefineXML\n", name)
		task, err := p.CreateVM(ctx, vprovider.VMSpec{
			Name:     name,
			VCPUs:    1,
			MemoryMB: 256,
			Firmware: vprovider.FirmwareBIOS,
		})
		if err != nil {
			fatalf("CreateVM: %v", err)
		}
		fmt.Printf("  CreateVM task=%s state=%s entity=%s\n", task.ID, task.State, task.EntityID)

		vms2, err := p.ListVMs(ctx, vprovider.ListOptions{})
		if err != nil {
			fatalf("ListVMs after create: %v", err)
		}
		var newID string
		for _, v := range vms2 {
			if v.Name == name {
				newID = v.ID
			}
		}
		fmt.Printf("  ListVMs now shows %d domain(s); created id=%s\n", len(vms2), newID)

		if newID != "" {
			del, err := p.DeleteVM(ctx, newID, vprovider.DeleteOptions{Force: true})
			if err != nil {
				fatalf("DeleteVM: %v", err)
			}
			fmt.Printf("  DeleteVM task=%s state=%s -> cleaned up\n", del.ID, del.State)
		}
	}

	// --- EXTENSION: console (read <graphics> from live domain XML) ---
	fmt.Println("== extension: console ==")
	if cp, ok := any(p).(vprovider.ConsoleProvider); ok {
		printed := false
		for _, v := range vms {
			ep, err := cp.Console(ctx, v.ID)
			if err != nil {
				fmt.Printf("  Console(%s): %v\n", v.Name, err)
				continue
			}
			fmt.Printf("  Console(%s): kind=%s host=%s port=%d tlsPort=%d passwordSet=%v\n",
				v.Name, ep.Kind, ep.Host, ep.Port, ep.TLSPort, ep.Password != "")
			printed = true
		}
		if !printed {
			fmt.Println("  (no domain exposed a <graphics> console endpoint)")
		}
	} else {
		fmt.Println("  provider does not implement ConsoleProvider")
	}
	fmt.Println()

	// --- EXTENSION: network write (CreateNetwork + DeleteNetwork) ---
	fmt.Println("== extension: network write ==")
	if nw, ok := any(p).(vprovider.NetworkWriter); ok {
		netName := fmt.Sprintf("unihv-probe-net-%d", time.Now().Unix())
		ct, err := nw.CreateNetwork(ctx, vprovider.NetworkSpec{
			Name: netName, Type: "nat", CIDR: "192.168.211.0/24",
		})
		if err != nil {
			fatalf("CreateNetwork: %v", err)
		}
		fmt.Printf("  CreateNetwork(%s) task=%s state=%s\n", netName, ct.ID, ct.State)
		// Confirm it now appears in the live network list.
		if nets, err := p.ListNetworks(ctx); err == nil {
			for _, n := range nets {
				if n.Name == netName {
					fmt.Printf("  ListNetworks confirms: id=%s name=%s mode=%s\n", n.ID, n.Name, n.Type)
				}
			}
		}
		dt, err := nw.DeleteNetwork(ctx, netName)
		if err != nil {
			fatalf("DeleteNetwork: %v", err)
		}
		fmt.Printf("  DeleteNetwork(%s) task=%s state=%s -> cleaned up\n", netName, dt.ID, dt.State)
	} else {
		fmt.Println("  provider does not implement NetworkWriter")
	}
	fmt.Println()

	// --- EXTENSION: storage write (ListVolumes + CreateVolume + DeleteVolume) ---
	fmt.Println("== extension: storage write ==")
	if sp, ok := any(p).(vprovider.StorageProvider); ok {
		pools, _ := p.ListStorage(ctx)
		if len(pools) == 0 {
			fmt.Println("  (no storage pool present; define a 'default' dir pool to exercise storage)")
		}
		for _, pl := range pools {
			vols, err := sp.ListVolumes(ctx, pl.Name)
			if err != nil {
				fmt.Printf("  ListVolumes(%s): %v\n", pl.Name, err)
				continue
			}
			fmt.Printf("  ListVolumes(%s): %d volume(s)\n", pl.Name, len(vols))
			for _, v := range vols {
				fmt.Printf("    - %s fmt=%s cap=%.2fGB alloc=%.2fGB iso=%v path=%s\n",
					v.Name, v.Format, v.CapacityGB, v.AllocGB, v.IsISO, v.Path)
			}
		}
		// Exercise create + delete against the first writable (active) pool.
		var pool string
		for _, pl := range pools {
			if pl.Accessible {
				pool = pl.Name
				break
			}
		}
		if pool != "" {
			volName := fmt.Sprintf("unihv-probe-vol-%d.qcow2", time.Now().Unix())
			ct, err := sp.CreateVolume(ctx, vprovider.VolumeSpec{
				Name: volName, StorageID: pool, CapacityGB: 1, Format: vprovider.DiskQcow2,
			})
			if err != nil {
				fatalf("CreateVolume: %v", err)
			}
			fmt.Printf("  CreateVolume(%s/%s) task=%s state=%s\n", pool, volName, ct.ID, ct.State)
			if vols, err := sp.ListVolumes(ctx, pool); err == nil {
				for _, v := range vols {
					if v.Name == volName {
						fmt.Printf("  ListVolumes confirms created: %s cap=%.2fGB fmt=%s path=%s\n",
							v.Name, v.CapacityGB, v.Format, v.Path)
					}
				}
			}
			dt, err := sp.DeleteVolume(ctx, pool, volName)
			if err != nil {
				fatalf("DeleteVolume: %v", err)
			}
			fmt.Printf("  DeleteVolume(%s/%s) task=%s state=%s -> cleaned up\n", pool, volName, dt.ID, dt.State)
		} else {
			fmt.Println("  (no active pool to create/delete a volume in)")
		}
	} else {
		fmt.Println("  provider does not implement StorageProvider")
	}
	fmt.Println()

	fmt.Println("\nPROBE OK: real libvirt RPC API exercised end to end.")
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "kvmprobe: "+format+"\n", args...)
	os.Exit(1)
}
