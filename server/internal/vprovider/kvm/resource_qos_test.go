package kvm

import (
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

func TestRenderCputuneEl(t *testing.T) {
	// shares + MHz limit -> <shares> + <period>/<quota> (quota scaled by host MHz).
	got := renderCputuneEl(&vp.ResourceSpec{CPUShares: 2048, CPULimitMHz: 1000}, 2000)
	for _, want := range []string{"<cputune>", "<shares>2048</shares>", "<period>100000</period>", "<quota>50000</quota>", "</cputune>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cputune missing %q in:\n%s", want, got)
		}
	}
	// reservation-only -> derives a shares weight (no explicit shares given).
	got = renderCputuneEl(&vp.ResourceSpec{CPUReservationMHz: 1000}, 2000)
	if !strings.Contains(got, "<shares>512</shares>") { // 1000*1024/2000
		t.Fatalf("cputune reservation->shares wrong:\n%s", got)
	}
	// all-zero -> empty.
	if s := renderCputuneEl(&vp.ResourceSpec{}, 2000); s != "" {
		t.Fatalf("empty cputune expected, got %q", s)
	}
}

func TestRenderMemtuneEl(t *testing.T) {
	got := renderMemtuneEl(&vp.ResourceSpec{MemoryLimitMB: 2048, MemoryReservationMB: 1024})
	for _, want := range []string{
		"<memtune>",
		"<hard_limit unit='KiB'>2097152</hard_limit>",   // 2048*1024
		"<min_guarantee unit='KiB'>1048576</min_guarantee>", // 1024*1024
		"</memtune>",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("memtune missing %q in:\n%s", want, got)
		}
	}
	// shares without a hard limit -> soft_limit anchor at reservation.
	got = renderMemtuneEl(&vp.ResourceSpec{MemoryShares: 100, MemoryReservationMB: 512})
	if !strings.Contains(got, "<soft_limit unit='KiB'>524288</soft_limit>") {
		t.Fatalf("memtune soft_limit wrong:\n%s", got)
	}
	if s := renderMemtuneEl(&vp.ResourceSpec{}); s != "" {
		t.Fatalf("empty memtune expected, got %q", s)
	}
}

func TestRenderIotuneEl(t *testing.T) {
	// total iops/bytes win over per-direction.
	got := renderIotuneEl(&vp.DiskQoS{TotalIOPS: 500, TotalBytesSec: 1048576})
	for _, want := range []string{"<iotune>", "<total_bytes_sec>1048576</total_bytes_sec>", "<total_iops_sec>500</total_iops_sec>", "</iotune>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("iotune missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "read_iops_sec") {
		t.Fatalf("iotune should not emit read_iops when total set:\n%s", got)
	}
	// per-direction.
	got = renderIotuneEl(&vp.DiskQoS{ReadIOPS: 100, WriteIOPS: 50, ReadBytesSec: 4096, WriteBytesSec: 2048})
	for _, want := range []string{"<read_iops_sec>100</read_iops_sec>", "<write_iops_sec>50</write_iops_sec>", "<read_bytes_sec>4096</read_bytes_sec>", "<write_bytes_sec>2048</write_bytes_sec>"} {
		if !strings.Contains(got, want) {
			t.Fatalf("iotune per-direction missing %q in:\n%s", want, got)
		}
	}
	// all-zero -> empty.
	if s := renderIotuneEl(&vp.DiskQoS{}); s != "" {
		t.Fatalf("empty iotune expected, got %q", s)
	}
	if s := renderIotuneEl(nil); s != "" {
		t.Fatalf("nil iotune expected empty, got %q", s)
	}
}

func TestRenderDiskDriverEl_Discard(t *testing.T) {
	if got := renderDiskDriverEl("qcow2", true); !strings.Contains(got, "discard='unmap'") {
		t.Fatalf("expected discard='unmap', got %q", got)
	}
	if got := renderDiskDriverEl("qcow2", false); strings.Contains(got, "discard") {
		t.Fatalf("did not expect discard attr, got %q", got)
	}
}

func TestRenderDomainXML_ResourceAndDiskQoS(t *testing.T) {
	d := &libvirtDomain{
		Name: "tunevm", VCPUs: 2, MemoryKB: 1024 * 1024,
		Resources: &vp.ResourceSpec{CPUShares: 1024, CPULimitMHz: 2000, MemoryLimitMB: 1024},
		Disks: []libvirtDisk{{
			Target: "vda", Driver: "qcow2", Source: "/var/lib/libvirt/images/d.qcow2",
			Discard: true,
			QoS:     &vp.DiskQoS{TotalIOPS: 300},
		}},
	}
	xmlOut := renderDomainXML(d)
	for _, want := range []string{
		"<cputune>", "<shares>1024</shares>",
		"<memtune>", "<hard_limit unit='KiB'>1048576</hard_limit>",
		"discard='unmap'",
		"<iotune>", "<total_iops_sec>300</total_iops_sec>",
	} {
		if !strings.Contains(xmlOut, want) {
			t.Fatalf("domain XML missing %q in:\n%s", want, xmlOut)
		}
	}
}

func TestSpliceResourceTune(t *testing.T) {
	base := "<domain type='kvm'>\n  <name>x</name>\n  <cputune>\n    <shares>500</shares>\n  </cputune>\n  <vcpu>2</vcpu>\n</domain>\n"
	out := spliceResourceTune(base, "  <cputune>\n    <shares>999</shares>\n  </cputune>\n", "")
	if strings.Contains(out, "<shares>500</shares>") {
		t.Fatalf("old cputune not stripped:\n%s", out)
	}
	if !strings.Contains(out, "<shares>999</shares>") {
		t.Fatalf("new cputune not inserted:\n%s", out)
	}
	if !strings.HasSuffix(strings.TrimSpace(out), "</domain>") {
		t.Fatalf("cputune must be before </domain>:\n%s", out)
	}
}

func TestRenderVolumeXMLProv_ThickThin(t *testing.T) {
	thin := renderVolumeXMLProv("v", "qcow2", 1<<30, false)
	if !strings.Contains(thin, "<allocation unit='bytes'>0</allocation>") || strings.Contains(thin, "<allocation>full</allocation>") {
		t.Fatalf("thin volume wrong:\n%s", thin)
	}
	thick := renderVolumeXMLProv("v", "qcow2", 1<<30, true)
	if !strings.Contains(thick, "<allocation unit='bytes'>1073741824</allocation>") || !strings.Contains(thick, "<allocation>full</allocation>") {
		t.Fatalf("thick volume wrong:\n%s", thick)
	}
}
