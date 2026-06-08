// modeled on cloudinit.go (see CASTOR-REUSE.md)
//
// sysprep.go implements WINDOWS guest customization for KVM domains — the Windows
// analogue of cloud-init (Lot 4A). On VM create, when a SysprepSpec is present, the
// LIVE backend renders a standard autounattend.xml answer file and packs it into a
// small ISO9660 image whose volume id is "sysprep". Windows Setup auto-discovers an
// autounattend.xml on any attached removable medium and runs an UNATTENDED install /
// specialize / OOBE pass (computer name, admin password, locale, time zone, product
// key, organization) without interactive prompts.
//
// The pure function renderAutounattendXML is CGO-free and unit-tested in isolation.
// buildSysprepISO is a method on the LIVE backend (it shells to xorriso and writes
// files into the storage pool), so the in-memory sim never runs it — the sim simply
// creates the VM WITHOUT a sysprep seed (sysprep is a live-only feature).
package kvm

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// renderAutounattendXML builds a Windows Setup answer file (autounattend.xml) from
// the spec. It emits a minimal-but-valid unattend with the two passes that matter
// for an unattended deploy:
//
//   - specialize: ComputerName + RegisteredOrganization + ProductKey (Shell-Setup),
//     plus the international settings (locale/UI language/time zone) when given.
//   - oobeSystem: the local Administrator password + an OOBE block that skips the
//     EULA/network/privacy prompts so first boot lands at the desktop unattended.
//
// Any caller-supplied UnattendXMLExtra is appended verbatim inside <unattend> as an
// advanced override (e.g. extra <settings> passes or RunSynchronousCommands).
func renderAutounattendXML(s *vp.SysprepSpec) string {
	var sb strings.Builder
	sb.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	sb.WriteString(`<unattend xmlns="urn:schemas-microsoft-com:unattend">` + "\n")

	// --- specialize pass: computer name, org, product key, intl settings ---
	sb.WriteString(`  <settings pass="specialize">` + "\n")
	sb.WriteString(`    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" language="neutral" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">` + "\n")
	if cn := strings.TrimSpace(s.ComputerName); cn != "" {
		fmt.Fprintf(&sb, "      <ComputerName>%s</ComputerName>\n", xmlEscape(cn))
	}
	if org := strings.TrimSpace(s.OrgName); org != "" {
		fmt.Fprintf(&sb, "      <RegisteredOrganization>%s</RegisteredOrganization>\n", xmlEscape(org))
	}
	if pk := strings.TrimSpace(s.ProductKey); pk != "" {
		fmt.Fprintf(&sb, "      <ProductKey>%s</ProductKey>\n", xmlEscape(pk))
	}
	if tz := strings.TrimSpace(s.TimeZone); tz != "" {
		fmt.Fprintf(&sb, "      <TimeZone>%s</TimeZone>\n", xmlEscape(tz))
	}
	sb.WriteString(`    </component>` + "\n")
	if loc := strings.TrimSpace(s.Locale); loc != "" {
		sb.WriteString(`    <component name="Microsoft-Windows-International-Core" processorArchitecture="amd64" language="neutral" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">` + "\n")
		fmt.Fprintf(&sb, "      <InputLocale>%s</InputLocale>\n", xmlEscape(loc))
		fmt.Fprintf(&sb, "      <SystemLocale>%s</SystemLocale>\n", xmlEscape(loc))
		fmt.Fprintf(&sb, "      <UILanguage>%s</UILanguage>\n", xmlEscape(loc))
		fmt.Fprintf(&sb, "      <UserLocale>%s</UserLocale>\n", xmlEscape(loc))
		sb.WriteString(`    </component>` + "\n")
	}
	sb.WriteString(`  </settings>` + "\n")

	// --- oobeSystem pass: Administrator password + skip OOBE prompts ---
	sb.WriteString(`  <settings pass="oobeSystem">` + "\n")
	sb.WriteString(`    <component name="Microsoft-Windows-Shell-Setup" processorArchitecture="amd64" language="neutral" xmlns:wcm="http://schemas.microsoft.com/WMIConfig/2002/State">` + "\n")
	if pw := s.AdminPassword; pw != "" {
		sb.WriteString(`      <UserAccounts>` + "\n")
		sb.WriteString(`        <AdministratorPassword>` + "\n")
		fmt.Fprintf(&sb, "          <Value>%s</Value>\n", xmlEscape(pw))
		sb.WriteString(`          <PlainText>true</PlainText>` + "\n")
		sb.WriteString(`        </AdministratorPassword>` + "\n")
		sb.WriteString(`      </UserAccounts>` + "\n")
	}
	sb.WriteString(`      <OOBE>` + "\n")
	sb.WriteString(`        <HideEULAPage>true</HideEULAPage>` + "\n")
	sb.WriteString(`        <HideOnlineAccountScreens>true</HideOnlineAccountScreens>` + "\n")
	sb.WriteString(`        <HideWirelessSetupInOOBE>true</HideWirelessSetupInOOBE>` + "\n")
	sb.WriteString(`        <ProtectYourPC>3</ProtectYourPC>` + "\n")
	sb.WriteString(`      </OOBE>` + "\n")
	sb.WriteString(`    </component>` + "\n")
	sb.WriteString(`  </settings>` + "\n")

	if extra := strings.TrimRight(s.UnattendXMLExtra, "\n"); strings.TrimSpace(extra) != "" {
		sb.WriteString("  <!-- user-supplied extra unattend settings -->\n")
		sb.WriteString(extra)
		sb.WriteString("\n")
	}

	sb.WriteString(`</unattend>` + "\n")
	return sb.String()
}

// buildSysprepISO renders autounattend.xml for s and packs it into a 'sysprep'
// ISO9660 image via `xorriso -as mkisofs`, written into the storage pool's target
// directory so libvirt can use it as a domain cdrom <source>. Returns the absolute
// path of the seed ISO. LIVE backend only (writes a file + runs xorriso). Mirrors
// buildSeedISO (cloudinit.go).
//
// poolName selects where the ISO lands (defaults to "default"); the file is named
// after the domain so it is easy to correlate and clean up.
func (b *liveBackend) buildSysprepISO(domName, poolName string, s *vp.SysprepSpec) (string, error) {
	if s == nil {
		return "", nil
	}
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		return "", fmt.Errorf("kvm: sysprep needs xorriso to build the autounattend seed ISO: %w", err)
	}
	if poolName == "" {
		poolName = "default"
	}
	dir, err := b.poolTargetDir(poolName)
	if err != nil {
		return "", err
	}

	base := sanitizeFileToken(domName)
	answer := renderAutounattendXML(s)

	stage, err := os.MkdirTemp("", "unihv-sysprep-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	// Windows Setup looks for autounattend.xml in the ROOT of removable media.
	if err := os.WriteFile(filepath.Join(stage, "autounattend.xml"), []byte(answer), 0o600); err != nil {
		return "", err
	}

	seedPath := filepath.Join(dir, base+"-sysprep.iso")
	// The volume id 'sysprep' is a convention so the ISO is easy to identify; Windows
	// finds autounattend.xml by filename regardless of the label.
	args := []string{"-as", "mkisofs", "-output", seedPath, "-volid", "sysprep", "-joliet", "-rock", "autounattend.xml"}
	cmd := exec.Command(xorriso, args...)
	cmd.Dir = stage
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("kvm: xorriso sysprep build failed: %v: %s", err, strings.TrimSpace(string(out)))
	}

	// Refresh the pool so libvirt sees the new file as a volume (best-effort).
	if pool, perr := b.poolHandle(poolName); perr == nil {
		b.mu.RLock()
		l := b.l
		b.mu.RUnlock()
		if l != nil {
			_ = l.StoragePoolRefresh(pool, 0)
		}
	}
	return seedPath, nil
}
