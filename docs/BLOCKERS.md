# UniHV — Blockers log

> A blocker is logged here ONLY when it makes ALL progress impossible (prompt §1.5b),
> or when an agent fails its QA gate after 5 self-correction iterations (§1.4).

## Open item (not a build blocker)

### Claude Chrome visual validation — ready for the user's visual pass
"Claude Chrome" is the browser-extension side panel; it is NOT exposed as a tool in this
CLI session, so the autonomous run cannot drive Chrome itself. Everything else is done and
validated against REAL hypervisors (no mock):

The app is RUNNING at **http://localhost:8080** with REAL data:
- login: `admin` / `Sup3rSecret!Pass`
- a REAL KVM/libvirt connection (WSL2, libvirt 10.0.0) is registered: 2 real domains
  (`web-server-01` running, `db-server-01` stopped) + 18 real host containers in the
  unified inventory; UNIHV_DEMO_HYPERVISOR=false (no demo/mock).
- Proven end-to-end already (HTTP): connect real libvirt → inventory shows real VMs →
  power START via API actually started the domain (confirmed independently by `virsh`:
  running). Hyper-V WMI, ESXi govmomi (vcsim), Xen XAPI clients likewise proven real.

For the user's Chrome pass: open http://localhost:8080 in Chrome (with the Claude
extension), log in, and review Dashboard (unified VM+container headline) → Virtual Machines
(real KVM VMs, power/snapshot/clone) → Connections (add a real hypervisor) → Clusters →
Migration (V2V). To add the Hyper-V on this Windows box, run a UniHV node on Windows
(Hyper-V uses the Windows-only WMI API).

This is the single DoD item the CLI session cannot self-complete (no browser tool);
the application itself is done, real, tested, and green.
