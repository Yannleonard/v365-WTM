package kvm

import (
	"encoding/xml"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

func TestRenderAutounattendXML_FullSpec(t *testing.T) {
	s := &vp.SysprepSpec{
		ComputerName:     "WIN-APP01",
		AdminPassword:    "P@ssw0rd!",
		ProductKey:       "AAAAA-BBBBB-CCCCC-DDDDD-EEEEE",
		OrgName:          "GTek IT",
		TimeZone:         "Romance Standard Time",
		Locale:           "fr-FR",
		UnattendXMLExtra: "  <!-- extra -->\n",
	}
	out := renderAutounattendXML(s)

	// Must be well-formed XML.
	if err := xml.Unmarshal([]byte(out), new(struct{})); err != nil {
		t.Fatalf("autounattend.xml is not well-formed XML: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, `<?xml version="1.0"`) {
		t.Fatalf("must start with the XML declaration, got:\n%s", out)
	}
	mustContain(t, out, `<unattend xmlns="urn:schemas-microsoft-com:unattend">`)
	mustContain(t, out, `pass="specialize"`)
	mustContain(t, out, `pass="oobeSystem"`)
	mustContain(t, out, "<ComputerName>WIN-APP01</ComputerName>")
	mustContain(t, out, "<RegisteredOrganization>GTek IT</RegisteredOrganization>")
	mustContain(t, out, "<ProductKey>AAAAA-BBBBB-CCCCC-DDDDD-EEEEE</ProductKey>")
	mustContain(t, out, "<TimeZone>Romance Standard Time</TimeZone>")
	mustContain(t, out, "<InputLocale>fr-FR</InputLocale>")
	mustContain(t, out, "<SystemLocale>fr-FR</SystemLocale>")
	mustContain(t, out, "<UILanguage>fr-FR</UILanguage>")
	mustContain(t, out, "<AdministratorPassword>")
	mustContain(t, out, "<Value>P@ssw0rd!</Value>")
	mustContain(t, out, "<PlainText>true</PlainText>")
	mustContain(t, out, "<HideEULAPage>true</HideEULAPage>")
	mustContain(t, out, "<!-- extra -->")
}

func TestRenderAutounattendXML_Minimal(t *testing.T) {
	s := &vp.SysprepSpec{ComputerName: "HOST1"}
	out := renderAutounattendXML(s)
	if err := xml.Unmarshal([]byte(out), new(struct{})); err != nil {
		t.Fatalf("minimal autounattend.xml is not well-formed: %v\n%s", err, out)
	}
	mustContain(t, out, "<ComputerName>HOST1</ComputerName>")
	// No product key / org / password blocks when unset.
	if strings.Contains(out, "<ProductKey>") {
		t.Fatalf("unexpected <ProductKey> for a spec without one:\n%s", out)
	}
	if strings.Contains(out, "<AdministratorPassword>") {
		t.Fatalf("unexpected <AdministratorPassword> for a spec without one:\n%s", out)
	}
}

func TestRenderDomainXML_CPUTopology(t *testing.T) {
	d := &libvirtDomain{
		Name:     "topo-vm",
		VCPUs:    1, // overridden by topology
		MemoryKB: 1024 * 1024,
		CPU:      &vp.CPUSpec{Sockets: 2, CoresPerSocket: 2, ThreadsPerCore: 1},
	}
	out := renderDomainXML(d)
	// <vcpu> must equal sockets*cores*threads = 4.
	mustContain(t, out, "<vcpu placement='static'>4</vcpu>")
	mustContain(t, out, "<cpu mode='host-passthrough'>")
	mustContain(t, out, "<topology sockets='2' cores='2' threads='1'/>")
}

func TestRenderDomainXML_CPUCustomModel(t *testing.T) {
	d := &libvirtDomain{
		Name:     "model-vm",
		VCPUs:    2,
		MemoryKB: 1024 * 1024,
		CPU:      &vp.CPUSpec{Sockets: 1, CoresPerSocket: 2, ThreadsPerCore: 1, Model: "Skylake-Server"},
	}
	out := renderDomainXML(d)
	mustContain(t, out, "<cpu mode='custom'")
	mustContain(t, out, "<model fallback='allow'>Skylake-Server</model>")
	mustContain(t, out, "<topology sockets='1' cores='2' threads='1'/>")
	mustContain(t, out, "<vcpu placement='static'>2</vcpu>")
}

func TestRenderDomainXML_NoCPU(t *testing.T) {
	d := &libvirtDomain{Name: "flat-vm", VCPUs: 3, MemoryKB: 1024 * 1024}
	out := renderDomainXML(d)
	if strings.Contains(out, "<cpu") {
		t.Fatalf("flat vCPU VM must not emit a <cpu> element:\n%s", out)
	}
	mustContain(t, out, "<vcpu placement='static'>3</vcpu>")
}

func TestRenderDomainXML_TemplateMetadata(t *testing.T) {
	d := &libvirtDomain{Name: "golden", VCPUs: 1, MemoryKB: 1024 * 1024, IsTemplate: true}
	out := renderDomainXML(d)
	mustContain(t, out, "<metadata>")
	mustContain(t, out, "<unihv:template")
	mustContain(t, out, ">true</unihv:template>")

	d2 := &libvirtDomain{Name: "plain", VCPUs: 1, MemoryKB: 1024 * 1024}
	out2 := renderDomainXML(d2)
	if strings.Contains(out2, "unihv:template") {
		t.Fatalf("non-template VM must not emit template metadata:\n%s", out2)
	}
}

func TestRenderDomainXML_SysprepSeedCdrom(t *testing.T) {
	d := &libvirtDomain{Name: "win", VCPUs: 1, MemoryKB: 1024 * 1024, SysprepISO: "/pool/win-sysprep.iso"}
	out := renderDomainXML(d)
	mustContain(t, out, "<source file='/pool/win-sysprep.iso'/>")
	mustContain(t, out, "<target dev='sdc' bus='sata'/>")
}
