// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// live_metrics.go is the REAL per-domain / per-host metrics + lifecycle-event
// implementation for the live libvirt backend. It is PURE Go (go-libvirt RPC, no
// cgo) and carries NO build tag, like the rest of the live backend.
//
// The whole point: a POWERED-OFF domain reports ZERO — no CPU%, no usage — and a
// RUNNING domain reports REAL libvirt counters. NOTHING is fabricated.
//
// Official libvirt API surface used (1:1 with libvirt's C API, via go-libvirt):
//
//	state   : DomainGetState                 (skip metrics entirely when not running)
//	cpu     : DomainGetInfo                  (cpuTime ns + nrVirtCpu; two reads ~Δt)
//	memory  : DomainMemoryStats              (rss / actual-balloon / available-unused)
//	          DomainGetInfo.maxMem           (the domain's REAL configured RAM limit)
//	net     : DomainInterfaceStats           (rx/tx bytes summed over interfaces)
//	disk    : DomainBlockStats               (read/write bytes summed over disks)
//	host    : NodeGetInfo + NodeGetMemoryStats (real host mem; CPU best-effort)
//	events  : DomainGetState polling         (real state transitions -> vm.state)
package kvm

import (
	"context"
	"encoding/xml"
	"fmt"
	"time"

	libvirt "github.com/digitalocean/go-libvirt"
	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// cpuSampleInterval is the short wall-clock gap between the two cpuTime reads used
// to derive an instantaneous CPU% for a running domain. Long enough to be
// meaningful, short enough not to stall the metrics request.
const cpuSampleInterval = 300 * time.Millisecond

// metrics serves REAL metrics for a domain UUID or the host/node id. It satisfies
// the metricsBackend seam consumed by Provider.GetMetrics. A NOT-running domain
// returns a series with ZERO samples (never fabricated load).
func (b *liveBackend) metrics(entityID string, window vp.MetricWindow) (*vp.MetricSeries, bool) {
	b.mu.RLock()
	l := b.l
	nodeID := b.nodeID
	b.mu.RUnlock()
	if l == nil {
		return nil, false
	}

	// Host/node metrics path.
	if entityID == nodeID {
		return b.hostMetrics(l, entityID), true
	}

	// Domain metrics path: resolve the native handle.
	_, dom, ok := b.domainHandle(entityID)
	if !ok {
		return nil, false
	}

	series := &vp.MetricSeries{EntityID: entityID}

	// 1) Power state gate. DomainGetState is authoritative. If NOT running we return
	//    an EMPTY series — a powered-off VM shows 0% CPU and no usage, period.
	state, _, err := l.DomainGetState(dom, 0)
	if err != nil {
		b.fail(err)
		// Could not read state -> behave as "no data" rather than fabricate.
		return series, true
	}
	if libvirtState(state) != domRunning {
		return series, true // ZERO samples: the bug-fix guarantee.
	}

	// 2) RUNNING domain: collect REAL counters.
	sample := vp.MetricSample{Timestamp: time.Now().UTC()}

	// CPU%: two cpuTime reads ~Δt apart. %CPU = ΔcpuTime / (Δwall * nrVcpu) * 100,
	// then clamped to [0,100]. cpuTime is cumulative ns of vCPU execution.
	if _, maxMem, _, nrVCPU, cpu0, err := l.DomainGetInfo(dom); err == nil {
		t0 := time.Now()
		// MemLimitBytes = the domain's REAL configured maximum memory. maxMem is in
		// KiB (libvirt) -> bytes. This MUST reflect the VM's true RAM (8GB shows 8GB).
		sample.MemLimitBytes = uint64(maxMem) * 1024

		time.Sleep(cpuSampleInterval)

		if _, _, _, _, cpu1, err2 := l.DomainGetInfo(dom); err2 == nil {
			dtWall := time.Since(t0).Seconds()
			vcpus := float64(nrVCPU)
			if vcpus < 1 {
				vcpus = 1
			}
			if dtWall > 0 && cpu1 >= cpu0 {
				dCPUns := float64(cpu1 - cpu0)
				pct := (dCPUns / (dtWall * 1e9 * vcpus)) * 100.0
				sample.CPUPercent = clampPct(pct)
			}
		} else {
			b.fail(err2)
		}
	} else {
		b.fail(err)
	}

	// Memory used: DomainMemoryStats (KiB tags). Prefer RSS (host-visible resident
	// set), else actual-balloon minus unused (guest-used), else actual-balloon.
	sample.MemUsageBytes = b.domainMemUsedBytes(l, dom)
	if sample.MemLimitBytes == 0 {
		// Fallback to balloon-current if maxMem was unreadable.
		sample.MemLimitBytes = sample.MemUsageBytes
	}

	// Net + disk: sum REAL per-device counters. Resolve the live device targets
	// (host tap dev for NICs, disk target dev) from the current domain XML.
	rx, tx := b.domainNetBytes(l, dom)
	sample.NetRxBytes, sample.NetTxBytes = rx, tx
	rd, wr := b.domainDiskBytes(l, dom)
	sample.DiskReadBytes, sample.DiskWriteBytes = rd, wr

	series.Samples = append(series.Samples, sample)
	return series, true
}

// clampPct clamps a CPU percentage into [0,100].
func clampPct(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// domainMemUsedBytes returns the domain's real used memory in BYTES from
// DomainMemoryStats. Order of preference: RSS (host resident set) -> actual-balloon
// minus unused (guest-used) -> actual-balloon current. Returns 0 when stats are
// unavailable (no balloon driver / agent) rather than a fabricated value.
func (b *liveBackend) domainMemUsedBytes(l *libvirt.Libvirt, dom libvirt.Domain) uint64 {
	stats, err := l.DomainMemoryStats(dom, uint32(libvirt.DomainMemoryStatNr), 0)
	if err != nil {
		b.fail(err)
		return 0
	}
	var rss, actual, unused, available uint64
	for _, s := range stats {
		switch libvirt.DomainMemoryStatTags(s.Tag) {
		case libvirt.DomainMemoryStatRss:
			rss = s.Val
		case libvirt.DomainMemoryStatActualBalloon:
			actual = s.Val
		case libvirt.DomainMemoryStatUnused:
			unused = s.Val
		case libvirt.DomainMemoryStatAvailable:
			available = s.Val
		}
	}
	switch {
	case rss > 0:
		return rss * 1024 // KiB -> bytes
	case available > 0 && unused > 0 && available >= unused:
		return (available - unused) * 1024 // guest-used
	case actual > 0 && unused > 0 && actual >= unused:
		return (actual - unused) * 1024
	case actual > 0:
		return actual * 1024
	default:
		return 0
	}
}

// domainNetBytes sums rx/tx bytes across all of the domain's interfaces via
// DomainInterfaceStats, keyed by the host-side tap device (<target dev=...>).
// Returns (0,0) when no interface has a queryable host device.
func (b *liveBackend) domainNetBytes(l *libvirt.Libvirt, dom libvirt.Domain) (rx, tx uint64) {
	for _, dev := range b.domainIfaceDevs(l, dom) {
		rxB, _, _, _, txB, _, _, _, err := l.DomainInterfaceStats(dom, dev)
		if err != nil {
			continue // device not queryable -> skip, never fabricate
		}
		if rxB > 0 {
			rx += uint64(rxB)
		}
		if txB > 0 {
			tx += uint64(txB)
		}
	}
	return rx, tx
}

// domainDiskBytes sums read/write bytes across all of the domain's disks via
// DomainBlockStats, keyed by the disk target dev (e.g. "vda"). Returns (0,0) when
// no disk is queryable.
func (b *liveBackend) domainDiskBytes(l *libvirt.Libvirt, dom libvirt.Domain) (rd, wr uint64) {
	for _, dev := range b.domainDiskDevs(l, dom) {
		_, rdB, _, wrB, _, err := l.DomainBlockStats(dom, dev)
		if err != nil {
			continue
		}
		if rdB > 0 {
			rd += uint64(rdB)
		}
		if wrB > 0 {
			wr += uint64(wrB)
		}
	}
	return rd, wr
}

// metricsDevXML is the tiny slice of domain XML the metrics path needs: the
// host-side interface target devs and disk target devs to key the per-device
// libvirt stat RPCs.
type metricsDevXML struct {
	Devices struct {
		Disks []struct {
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"disk"`
		Interfaces []struct {
			Target struct {
				Dev string `xml:"dev,attr"`
			} `xml:"target"`
		} `xml:"interface"`
	} `xml:"devices"`
}

func (b *liveBackend) domainDevXML(l *libvirt.Libvirt, dom libvirt.Domain) *metricsDevXML {
	raw, err := l.DomainGetXMLDesc(dom, 0)
	if err != nil {
		b.fail(err)
		return nil
	}
	var dx metricsDevXML
	if err := xml.Unmarshal([]byte(raw), &dx); err != nil {
		return nil
	}
	return &dx
}

// domainIfaceDevs returns the host-side interface device names (e.g. "vnet0").
func (b *liveBackend) domainIfaceDevs(l *libvirt.Libvirt, dom libvirt.Domain) []string {
	dx := b.domainDevXML(l, dom)
	if dx == nil {
		return nil
	}
	var out []string
	for _, iface := range dx.Devices.Interfaces {
		if d := iface.Target.Dev; d != "" {
			out = append(out, d)
		}
	}
	return out
}

// domainDiskDevs returns the disk target device names (e.g. "vda", "vdb").
func (b *liveBackend) domainDiskDevs(l *libvirt.Libvirt, dom libvirt.Domain) []string {
	dx := b.domainDevXML(l, dom)
	if dx == nil {
		return nil
	}
	var out []string
	for _, dk := range dx.Devices.Disks {
		if d := dk.Target.Dev; d != "" {
			out = append(out, d)
		}
	}
	return out
}

// hostMetrics returns a real single-sample host series: real total memory from
// NodeGetInfo and real used memory from NodeGetMemoryStats. Host CPU% is reported
// as 0 unless a real instantaneous figure is derivable (we do not fabricate it).
func (b *liveBackend) hostMetrics(l *libvirt.Libvirt, entityID string) *vp.MetricSeries {
	series := &vp.MetricSeries{EntityID: entityID}
	sample := vp.MetricSample{Timestamp: time.Now().UTC()}

	// Total memory (KiB) from NodeGetInfo.
	if _, memKiB, _, _, _, _, _, _, err := l.NodeGetInfo(); err == nil {
		sample.MemLimitBytes = uint64(memKiB) * 1024
	} else {
		b.fail(err)
	}

	// Used memory from NodeGetMemoryStats (fields: total/free/buffers/cached, KiB).
	if params, _, err := l.NodeGetMemoryStats(0, int32(libvirt.NodeMemoryStatsAllCells), 0); err == nil {
		var total, free, buffers, cached uint64
		for _, p := range params {
			switch p.Field {
			case "total":
				total = p.Value
			case "free":
				free = p.Value
			case "buffers":
				buffers = p.Value
			case "cached":
				cached = p.Value
			}
		}
		if total > 0 {
			if sample.MemLimitBytes == 0 {
				sample.MemLimitBytes = total * 1024
			}
			used := int64(total) - int64(free) - int64(buffers) - int64(cached)
			if used > 0 {
				sample.MemUsageBytes = uint64(used) * 1024
			}
		}
	} else {
		b.fail(err)
	}

	series.Samples = append(series.Samples, sample)
	return series
}

// streamEvents emits REAL libvirt lifecycle events. It polls DomainGetState across
// all domains and emits a vm.state event whenever a domain's real state changes (a
// genuine lifecycle transition), plus an initial truthful snapshot of current
// states so a fresh subscriber immediately learns reality. NO fabricated heartbeat
// content is ever produced. The goroutine owns `out` and closes it on ctx.Done.
func (b *liveBackend) streamEvents(ctx context.Context, providerID string, out chan<- vp.Event) {
	defer close(out)

	last := map[string]libvirtState{}

	emit := func(uuid, name string, st libvirtState) bool {
		ev := vp.Event{
			Kind:       vp.EventVMStateChanged,
			ProviderID: providerID,
			EntityID:   uuid,
			Message:    fmt.Sprintf("%s is %s", name, normalizeState(st)),
			Timestamp:  time.Now().UTC(),
		}
		select {
		case out <- ev:
			return true
		case <-ctx.Done():
			return false
		}
	}

	// Initial snapshot: emit each domain's CURRENT real state once.
	for _, d := range b.listDomains() {
		last[d.UUID] = d.State
		if !emit(d.UUID, d.Name, d.State) {
			return
		}
	}

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, d := range b.listDomains() {
				prev, seen := last[d.UUID]
				if !seen || prev != d.State {
					last[d.UUID] = d.State
					if seen && !emit(d.UUID, d.Name, d.State) {
						return
					}
					// (Newly-appeared domains seed `last` silently to avoid a burst
					// of duplicate "initial" events on every poll.)
					last[d.UUID] = d.State
				}
			}
		}
	}
}

// compile-time assertions: the live backend serves real metrics + real events.
var (
	_ metricsBackend = (*liveBackend)(nil)
	_ eventBackend   = (*liveBackend)(nil)
)
