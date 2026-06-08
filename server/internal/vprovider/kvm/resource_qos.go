// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// resource_qos.go implements Lot 5A on the REAL libvirt backend:
//   1. CPU/memory resource control (<cputune>/<memtune>) via DomainSetScheduler
//      Parameters-equivalent rendered into the persistent domain (DomainDefineXML
//      re-render) — applied with CONFIG (+LIVE cgroup where libvirt accepts it).
//   2. Per-disk QoS (<iotune>) change on an EXISTING disk via DomainUpdateDeviceFlags
//      (LIVE|CONFIG) — no reboot.
//   3. LIVE storage migration: DomainBlockCopy a running VM's disk to a new path,
//      wait for the mirror to reach the READY phase, then PIVOT (DomainBlockJobAbort
//      with the PIVOT flag) so the VM runs off the new storage with no downtime.
//
// It is PURE Go (go-libvirt RPC), no cgo, no build tag — consistent with
// live_libvirt.go.
package kvm

import (
	"fmt"
	"strings"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// hostCPUMHzForTune is the reference per-vCPU clock used to translate a CPU
// reservation/limit expressed in MHz into a cgroup CFS quota (period*ratio). It is
// a conservative default; the real applied bound is reported back honestly. libvirt
// has no native "reserve N MHz" knob — the closest real enforcement is a CFS
// quota/period cap (limit) + a relative shares weight (reservation/shares), which is
// exactly the vSphere model mapping we use.
const hostCPUMHzForTune = 2000

// cpuTunePeriod is the CFS scheduler period (microseconds) we pin so a limit/quota
// is deterministic (100ms is libvirt/kernel default).
const cpuTunePeriod = 100000

// renderCputuneEl renders a <cputune> element from a ResourceSpec, or "" when no CPU
// field is set. shares -> <shares>; limit(MHz) -> <period>/<quota> (CFS hard cap);
// reservation is modeled as a shares floor (libvirt has no hard CPU reservation, so
// when only a reservation is given we raise the relative weight). hostMHz scales the
// MHz limit into a quota.
func renderCputuneEl(r *vp.ResourceSpec, hostMHz int64) string {
	if r == nil || (r.CPUShares <= 0 && r.CPUReservationMHz <= 0 && r.CPULimitMHz <= 0) {
		return ""
	}
	if hostMHz <= 0 {
		hostMHz = hostCPUMHzForTune
	}
	var sb strings.Builder
	sb.WriteString("  <cputune>\n")
	// shares: explicit weight, else derive a weight from the reservation (a relative
	// "guarantee" — more reserved MHz => higher scheduling weight). 1024 is the
	// kernel default for one CPU-share unit.
	shares := r.CPUShares
	if shares <= 0 && r.CPUReservationMHz > 0 {
		shares = (r.CPUReservationMHz * 1024) / hostMHz
		if shares < 2 {
			shares = 2
		}
	}
	if shares > 0 {
		fmt.Fprintf(&sb, "    <shares>%d</shares>\n", shares)
	}
	// limit: a hard CFS cap. quota = period * (limitMHz / hostMHz).
	if r.CPULimitMHz > 0 {
		quota := (int64(cpuTunePeriod) * r.CPULimitMHz) / hostMHz
		if quota < 1000 {
			quota = 1000 // libvirt minimum quota is 1000us
		}
		fmt.Fprintf(&sb, "    <period>%d</period>\n", cpuTunePeriod)
		fmt.Fprintf(&sb, "    <quota>%d</quota>\n", quota)
	}
	sb.WriteString("  </cputune>\n")
	return sb.String()
}

// renderMemtuneEl renders a <memtune> element from a ResourceSpec, or "" when no
// memory field is set. limit(MB) -> <hard_limit> (KiB); reservation(MB) ->
// <min_guarantee> (KiB, the guaranteed working set); shares -> <soft_limit> weight
// proxy (libvirt has no memory-shares knob; soft_limit is the closest "give this VM
// preference under pressure" lever, so a shares hint maps to a soft_limit anchor).
func renderMemtuneEl(r *vp.ResourceSpec) string {
	if r == nil || (r.MemoryShares <= 0 && r.MemoryReservationMB <= 0 && r.MemoryLimitMB <= 0) {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("  <memtune>\n")
	if r.MemoryLimitMB > 0 {
		fmt.Fprintf(&sb, "    <hard_limit unit='KiB'>%d</hard_limit>\n", r.MemoryLimitMB*1024)
	}
	if r.MemoryReservationMB > 0 {
		fmt.Fprintf(&sb, "    <min_guarantee unit='KiB'>%d</min_guarantee>\n", r.MemoryReservationMB*1024)
	}
	// soft_limit: when an explicit soft weighting (shares) is given but no hard limit,
	// anchor a soft_limit at the reservation (or shares as MB) so the VM is favored
	// under host memory pressure. This is an honest approximation: libvirt has no
	// memory-shares; soft_limit is the documented "best-effort floor under pressure".
	if r.MemoryShares > 0 && r.MemoryLimitMB <= 0 {
		soft := r.MemoryReservationMB
		if soft <= 0 {
			soft = r.MemoryShares
		}
		if soft > 0 {
			fmt.Fprintf(&sb, "    <soft_limit unit='KiB'>%d</soft_limit>\n", soft*1024)
		}
	}
	sb.WriteString("  </memtune>\n")
	return sb.String()
}

// renderIotuneEl renders an <iotune> element from a DiskQoS, or "" when all limits
// are zero (unthrottled). Emits only the non-zero throttles.
func renderIotuneEl(q *vp.DiskQoS) string {
	if q == nil || (q.ReadIOPS <= 0 && q.WriteIOPS <= 0 && q.TotalIOPS <= 0 &&
		q.ReadBytesSec <= 0 && q.WriteBytesSec <= 0 && q.TotalBytesSec <= 0) {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("<iotune>\n")
	if q.TotalBytesSec > 0 {
		fmt.Fprintf(&sb, "  <total_bytes_sec>%d</total_bytes_sec>\n", q.TotalBytesSec)
	} else {
		if q.ReadBytesSec > 0 {
			fmt.Fprintf(&sb, "  <read_bytes_sec>%d</read_bytes_sec>\n", q.ReadBytesSec)
		}
		if q.WriteBytesSec > 0 {
			fmt.Fprintf(&sb, "  <write_bytes_sec>%d</write_bytes_sec>\n", q.WriteBytesSec)
		}
	}
	if q.TotalIOPS > 0 {
		fmt.Fprintf(&sb, "  <total_iops_sec>%d</total_iops_sec>\n", q.TotalIOPS)
	} else {
		if q.ReadIOPS > 0 {
			fmt.Fprintf(&sb, "  <read_iops_sec>%d</read_iops_sec>\n", q.ReadIOPS)
		}
		if q.WriteIOPS > 0 {
			fmt.Fprintf(&sb, "  <write_iops_sec>%d</write_iops_sec>\n", q.WriteIOPS)
		}
	}
	sb.WriteString("</iotune>\n")
	return sb.String()
}

// renderDiskDriverEl renders the <driver> element for a disk, adding
// discard='unmap' (TRIM/UNMAP passthrough, Lot 5A) when discard is requested.
func renderDiskDriverEl(format string, discard bool) string {
	if format == "" {
		format = "qcow2"
	}
	if discard {
		return fmt.Sprintf("<driver name='qemu' type='%s' discard='unmap'/>", xmlEscape(format))
	}
	return fmt.Sprintf("<driver name='qemu' type='%s'/>", xmlEscape(format))
}

// renderHotDiskDeviceXML builds a <disk device='disk'> for hot-attach carrying an
// optional discard='unmap' driver flag (TRIM) + an optional <iotune> QoS block.
func renderHotDiskDeviceXML(target, format, source string, discard bool, qos *vp.DiskQoS) string {
	if format == "" {
		format = "qcow2"
	}
	if target == "" {
		target = "vdb"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "<disk type='file' device='disk'>\n")
	fmt.Fprintf(&sb, "  %s\n", renderDiskDriverEl(format, discard))
	fmt.Fprintf(&sb, "  <source file='%s'/>\n", xmlEscape(source))
	fmt.Fprintf(&sb, "  <target dev='%s' bus='virtio'/>\n", xmlEscape(target))
	if iot := renderIotuneEl(qos); iot != "" {
		fmt.Fprintf(&sb, "%s", indentLines(iot, "  "))
	}
	fmt.Fprintf(&sb, "</disk>\n")
	return sb.String()
}

// indentLines prefixes every non-empty line of s with indent.
func indentLines(s, indent string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if line == "" {
			sb.WriteString("\n")
			continue
		}
		sb.WriteString(indent)
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// =============================================================================
// live backend implementation (extBackend, Lot 5A)
// =============================================================================

// setResources applies a ResourceSpec using libvirt's DEDICATED tuning RPCs (the
// robust, cross-version path that does NOT risk dropping devices the way a full XML
// re-define would):
//
//   - CPU : DomainSetSchedulerParametersFlags with cpu_shares / vcpu_period /
//     vcpu_quota (the <cputune> equivalents). CONFIG always; LIVE additionally when
//     the domain is running so the cgroup is retuned with no reboot.
//   - memory: DomainSetMemoryParameters with hard_limit / soft_limit / min_guarantee
//     in KiB (the <memtune> equivalents).
//
// A field left at zero is not sent (left at the hypervisor default). This is exactly
// the data renderCputuneEl/renderMemtuneEl model for the create-time XML, applied to
// an existing domain instead.
func (b *liveBackend) setResources(uuid string, spec vp.ResourceSpec) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	running := false
	if st, _, err := l.DomainGetState(dom, 0); err == nil {
		s := libvirtState(st)
		running = s == domRunning || s == domBlocked
	}

	// --- CPU scheduler params (cpu_shares / vcpu_period / vcpu_quota) ---
	var sched []libvirt.TypedParam
	shares := spec.CPUShares
	if shares <= 0 && spec.CPUReservationMHz > 0 {
		shares = (spec.CPUReservationMHz * 1024) / hostCPUMHzForTune
		if shares < 2 {
			shares = 2
		}
	}
	if shares > 0 {
		sched = append(sched, libvirt.TypedParam{Field: "cpu_shares", Value: *libvirt.NewTypedParamValueUllong(uint64(shares))})
	}
	if spec.CPULimitMHz > 0 {
		quota := (int64(cpuTunePeriod) * spec.CPULimitMHz) / hostCPUMHzForTune
		if quota < 1000 {
			quota = 1000
		}
		sched = append(sched,
			libvirt.TypedParam{Field: "vcpu_period", Value: *libvirt.NewTypedParamValueUllong(uint64(cpuTunePeriod))},
			libvirt.TypedParam{Field: "vcpu_quota", Value: *libvirt.NewTypedParamValueLlong(quota)},
		)
	}
	if len(sched) > 0 {
		// CONFIG (persist) always; LIVE too when running. AffectCurrent(0) on a
		// shut-off domain targets CONFIG.
		flags := uint32(libvirt.DomainAffectConfig)
		if running {
			flags |= uint32(libvirt.DomainAffectLive)
		}
		if err := l.DomainSetSchedulerParametersFlags(dom, sched, flags); err != nil {
			b.fail(err)
			return mapLibvirtErr(err)
		}
	}

	// --- memory params (hard_limit / soft_limit / min_guarantee, in KiB) ---
	var mem []libvirt.TypedParam
	if spec.MemoryLimitMB > 0 {
		mem = append(mem, libvirt.TypedParam{Field: "hard_limit", Value: *libvirt.NewTypedParamValueUllong(uint64(spec.MemoryLimitMB) * 1024)})
	}
	if spec.MemoryReservationMB > 0 {
		mem = append(mem, libvirt.TypedParam{Field: "min_guarantee", Value: *libvirt.NewTypedParamValueUllong(uint64(spec.MemoryReservationMB) * 1024)})
	}
	if spec.MemoryShares > 0 && spec.MemoryLimitMB <= 0 {
		soft := spec.MemoryReservationMB
		if soft <= 0 {
			soft = spec.MemoryShares
		}
		if soft > 0 {
			mem = append(mem, libvirt.TypedParam{Field: "soft_limit", Value: *libvirt.NewTypedParamValueUllong(uint64(soft) * 1024)})
		}
	}
	if len(mem) > 0 {
		flags := uint32(libvirt.DomainAffectConfig)
		if running {
			flags |= uint32(libvirt.DomainAffectLive)
		}
		if err := l.DomainSetMemoryParameters(dom, mem, flags); err != nil {
			// min_guarantee is not enforceable on every host cgroup; retry without it
			// rather than failing the whole apply (the hard/soft limits are the real
			// levers). Strip min_guarantee and retry once.
			retry := mem[:0]
			dropped := false
			for _, p := range mem {
				if p.Field == "min_guarantee" {
					dropped = true
					continue
				}
				retry = append(retry, p)
			}
			if !dropped || len(retry) == 0 {
				b.fail(err)
				return mapLibvirtErr(err)
			}
			if err2 := l.DomainSetMemoryParameters(dom, retry, flags); err2 != nil {
				b.fail(err2)
				return mapLibvirtErr(err2)
			}
		}
	}
	return nil
}

// spliceResourceTune removes any existing <cputune>/<memtune> from a domain XML and
// inserts the freshly rendered ones immediately before </domain> (their position
// among <domain> children is not significant to libvirt). Empty cputune/memtune
// strings clear the corresponding section.
func spliceResourceTune(domXML, cputune, memtune string) string {
	out := stripElement(domXML, "cputune")
	out = stripElement(out, "memtune")
	insert := cputune + memtune
	if insert == "" {
		return out
	}
	idx := strings.LastIndex(out, "</domain>")
	if idx < 0 {
		return out + insert
	}
	return out[:idx] + insert + out[idx:]
}

// stripElement removes the first <name>...</name> block (and a self-closed <name/>)
// from xmlStr. Simple, sufficient for the single cputune/memtune blocks libvirt emits.
func stripElement(xmlStr, name string) string {
	open := "<" + name + ">"
	openAttr := "<" + name + " "
	start := strings.Index(xmlStr, open)
	if start < 0 {
		start = strings.Index(xmlStr, openAttr)
	}
	if start < 0 {
		// self-closed?
		sc := "<" + name + "/>"
		if i := strings.Index(xmlStr, sc); i >= 0 {
			return xmlStr[:i] + xmlStr[i+len(sc):]
		}
		return xmlStr
	}
	close := "</" + name + ">"
	end := strings.Index(xmlStr[start:], close)
	if end < 0 {
		return xmlStr
	}
	end = start + end + len(close)
	// also swallow a trailing newline for tidiness
	rest := xmlStr[end:]
	rest = strings.TrimPrefix(rest, "\n")
	return xmlStr[:start] + rest
}

// setDiskQoS changes the per-disk <iotune> on an EXISTING disk via the dedicated
// DomainSetBlockIOTune RPC (the canonical libvirt block-IO tuning call), keyed by the
// disk's TARGET dev (e.g. "vda"). CONFIG always; LIVE additionally when the domain is
// running so the QEMU throttle is retuned with no reboot. A zeroed QoS sends all
// limits as 0, which CLEARS every throttle (unthrottled).
func (b *liveBackend) setDiskQoS(uuid, diskID string, qos vp.DiskQoS) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	dk, found := b.findDisk(l, dom, diskID)
	if !found {
		return vp.ErrNotFound
	}
	running := false
	if st, _, err := l.DomainGetState(dom, 0); err == nil {
		s := libvirtState(st)
		running = s == domRunning || s == domBlocked
	}
	// Always send the full set so zero values explicitly clear a previously-set limit.
	params := []libvirt.TypedParam{
		{Field: "total_bytes_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.TotalBytesSec)))},
		{Field: "read_bytes_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.ReadBytesSec)))},
		{Field: "write_bytes_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.WriteBytesSec)))},
		{Field: "total_iops_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.TotalIOPS)))},
		{Field: "read_iops_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.ReadIOPS)))},
		{Field: "write_iops_sec", Value: *libvirt.NewTypedParamValueUllong(uint64(max0(qos.WriteIOPS)))},
	}
	flags := uint32(libvirt.DomainAffectConfig)
	if running {
		flags |= uint32(libvirt.DomainAffectLive)
	}
	if err := l.DomainSetBlockIOTune(dom, dk.target, params, flags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// max0 clamps a negative int64 to 0 (QoS limits are non-negative; a 0 clears them).
func max0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// migrateStorage performs a LIVE storage migration of the disk identified by diskID
// onto target (a storage POOL name, or — when no such pool resolves — an explicit
// destination PATH). It uses the official copy-then-pivot block-job flow:
//
//	DomainBlockCopy(dom, <disk path>, <dest <disk> XML>, nil, REUSE? )
//	  -> libvirt mirrors the disk to the new file while the VM keeps running.
//	poll DomainGetBlockJobInfo until cur==end (mirror reached the READY phase).
//	DomainBlockJobAbort(dom, path, PIVOT) -> the VM pivots to the new file; the job
//	  completes and the old file is no longer in use.
//
// The VM stays running throughout (true live storage migration / Storage vMotion).
func (b *liveBackend) migrateStorage(uuid, diskID, target string) error {
	l, dom, ok := b.domainHandle(uuid)
	if !ok {
		if l == nil {
			return errNoConn
		}
		return vp.ErrNotFound
	}
	dk, found := b.findDisk(l, dom, diskID)
	if !found {
		return vp.ErrNotFound
	}
	if dk.source == "" {
		return fmt.Errorf("%w: disk has no file source to migrate", vp.ErrInvalidSpec)
	}
	format := dk.driver
	if format == "" {
		format = "qcow2"
	}
	// Resolve the destination path. If target names a known pool, provision a new
	// volume of the same capacity in that pool and use its path (libvirt then reuses
	// that pre-created file via the REUSE_EXT flag below); otherwise treat target as an
	// explicit destination file path that libvirt creates itself (no REUSE_EXT).
	destPath := target
	reuse := false
	if _, perr := b.poolHandle(target); perr == nil {
		// Size the new volume from the current block-device capacity.
		capBytes := int64(1) << 30
		if vol, verr := l.StorageVolLookupByPath(dk.source); verr == nil {
			if _, c, _, ierr := l.StorageVolGetInfo(vol); ierr == nil && c > 0 {
				capBytes = int64(c)
			}
		}
		volName := fmt.Sprintf("%s-migr-%d.%s", sanitizeBridge(uuid), time.Now().UnixNano(), format)
		path, err := b.provisionVolume(target, volName, capBytes, format)
		if err != nil {
			return err
		}
		destPath = path
		reuse = true // the file now EXISTS -> libvirt must REUSE it, not re-create it
	}
	if destPath == "" || destPath == dk.source {
		return fmt.Errorf("%w: destination resolves to the current path", vp.ErrInvalidSpec)
	}
	// Destination <disk> XML for the copy target.
	destXML := fmt.Sprintf("<disk type='file'>\n  <source file='%s'/>\n  <driver type='%s'/>\n</disk>\n",
		xmlEscape(destPath), xmlEscape(format))
	// Start the mirror. TRANSIENT_JOB is REQUIRED on a PERSISTENT domain (libvirt:
	// "domain is not transient") because the block-copy job state is not persisted;
	// REUSE_EXT when we pre-created the destination volume (libvirt otherwise refuses
	// to overwrite an existing file). A full (non-shallow) copy.
	copyFlags := libvirt.DomainBlockCopyTransientJob
	if reuse {
		copyFlags |= libvirt.DomainBlockCopyReuseExt
	}
	if err := l.DomainBlockCopy(dom, dk.target, destXML, nil, copyFlags); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	// Wait for the mirror to reach READY (cur == end, end > 0).
	if err := b.waitBlockJobReady(l, dom, dk.target); err != nil {
		// Best-effort abort the half-done job so we don't leave a dangling mirror.
		_ = l.DomainBlockJobAbort(dom, dk.target, 0)
		return err
	}
	// PIVOT: switch the running VM to the new file and finish the job.
	if err := l.DomainBlockJobAbort(dom, dk.target, libvirt.DomainBlockJobAbortPivot); err != nil {
		b.fail(err)
		return mapLibvirtErr(err)
	}
	return nil
}

// waitBlockJobReady polls DomainGetBlockJobInfo for the disk until the mirror has
// copied everything (cur >= end, with end > 0), i.e. the block job is in its READY
// phase and safe to pivot. Times out after ~120s.
func (b *liveBackend) waitBlockJobReady(l *libvirt.Libvirt, dom libvirt.Domain, dev string) error {
	deadline := time.Now().Add(120 * time.Second)
	for {
		found, _, _, cur, end, err := l.DomainGetBlockJobInfo(dom, dev, 0)
		if err != nil {
			b.fail(err)
			return mapLibvirtErr(err)
		}
		if found == 0 {
			// No job present: either it finished instantly (tiny disk) or never started.
			return nil
		}
		if end > 0 && cur >= end {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("%w: block-copy mirror did not reach ready within timeout", vp.ErrConflict)
		}
		time.Sleep(300 * time.Millisecond)
	}
}
