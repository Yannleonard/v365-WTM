// modeled on server/internal/vprovider/sim (see CASTOR-REUSE.md)
//
// cloudinit.go implements cloud-init guest customization for KVM domains using the
// NoCloud datasource. On VM create, when a CloudInitSpec is present, the LIVE
// backend renders the standard NoCloud files (user-data + meta-data, plus optional
// network-config) and packs them into a small ISO9660 image whose volume id is
// "cidata". A cloud-init-enabled guest image reads this CD on first boot and
// self-configures (hostname, user, password, SSH keys, runcmd, network).
//
// The pure functions renderUserData / renderMetaData are CGO-free and unit-tested
// in isolation. buildSeedISO is a method on the LIVE backend (it shells to xorriso
// and writes files into the storage pool), so the in-memory sim never runs it — the
// sim simply creates the VM WITHOUT a seed (cloud-init is a live-only feature).
package kvm

import (
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

// renderUserData builds the NoCloud #cloud-config user-data document from the spec.
// This is the standard cloud-config a cloud-init guest consumes on first boot.
//
// It emits, in order:
//   - hostname / fqdn
//   - users: a single sudo user (when Username set) with plain_text_passwd / passwd,
//     ssh_authorized_keys, sudo ALL=(ALL) NOPASSWD:ALL and lock_passwd:false so the
//     password actually works.
//   - ssh_pwauth: true when a password is set (so console/SSH password login works).
//   - runcmd: the requested first-boot commands.
//   - any raw UserDataExtra appended verbatim (advanced override).
func renderUserData(ci *vp.CloudInitSpec) string {
	var sb strings.Builder
	sb.WriteString("#cloud-config\n")

	if h := strings.TrimSpace(ci.Hostname); h != "" {
		fmt.Fprintf(&sb, "hostname: %s\n", yamlScalar(h))
		fmt.Fprintf(&sb, "fqdn: %s\n", yamlScalar(h))
		sb.WriteString("manage_etc_hosts: true\n")
	}

	if u := strings.TrimSpace(ci.Username); u != "" {
		sb.WriteString("users:\n")
		fmt.Fprintf(&sb, "  - name: %s\n", yamlScalar(u))
		sb.WriteString("    sudo: ALL=(ALL) NOPASSWD:ALL\n")
		sb.WriteString("    shell: /bin/bash\n")
		sb.WriteString("    lock_passwd: false\n")
		if pw := ci.Password; pw != "" {
			// plain_text_passwd is set verbatim; cloud-init hashes it. (A pre-hashed
			// value can be supplied via UserDataExtra's `passwd:` if preferred.)
			fmt.Fprintf(&sb, "    plain_text_passwd: %s\n", yamlScalar(pw))
		}
		keys := nonEmpty(ci.SSHAuthorizedKeys)
		if len(keys) > 0 {
			sb.WriteString("    ssh_authorized_keys:\n")
			for _, k := range keys {
				fmt.Fprintf(&sb, "      - %s\n", yamlScalar(k))
			}
		}
	}

	if ci.Password != "" {
		// Enable SSH/console password auth so the seeded password is usable.
		sb.WriteString("ssh_pwauth: true\n")
		sb.WriteString("chpasswd:\n")
		sb.WriteString("  expire: false\n")
	}

	if cmds := nonEmpty(ci.RunCmd); len(cmds) > 0 {
		sb.WriteString("runcmd:\n")
		for _, c := range cmds {
			// Each command runs via the shell; wrap as a single-element exec list
			// using the YAML flow form to preserve the command verbatim.
			fmt.Fprintf(&sb, "  - [ sh, -c, %s ]\n", yamlScalar(c))
		}
	}

	if extra := strings.TrimRight(ci.UserDataExtra, "\n"); strings.TrimSpace(extra) != "" {
		sb.WriteString("# --- user-supplied extra cloud-config ---\n")
		sb.WriteString(extra)
		sb.WriteString("\n")
	}

	return sb.String()
}

// renderMetaData builds the NoCloud meta-data document (instance-id + local-hostname).
// instance-id MUST be stable per VM; cloud-init re-runs per-instance modules when it
// changes, so we derive it from the domain name.
func renderMetaData(instanceID, hostname string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "instance-id: %s\n", yamlScalar(instanceID))
	if h := strings.TrimSpace(hostname); h != "" {
		fmt.Fprintf(&sb, "local-hostname: %s\n", yamlScalar(h))
	}
	return sb.String()
}

// yamlScalar quotes a YAML scalar value so special characters (':', '#', leading
// spaces, '@', etc.) and passwords are preserved exactly. Always double-quoted with
// internal backslashes/quotes escaped — valid YAML for any string.
func yamlScalar(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// nonEmpty returns the input with blank/whitespace-only entries dropped.
func nonEmpty(in []string) []string {
	out := in[:0:0]
	for _, s := range in {
		if strings.TrimSpace(s) != "" {
			out = append(out, strings.TrimSpace(s))
		}
	}
	return out
}

// buildSeedISO renders the NoCloud files for ci and packs them into a 'cidata'
// ISO9660 image via `xorriso -as mkisofs`, written into the storage pool's target
// directory so libvirt can use it as a domain cdrom <source>. Returns the absolute
// path of the seed ISO. LIVE backend only (writes files + runs xorriso).
//
// poolName selects where the ISO lands (defaults to "default"); the file is named
// after the domain so it is easy to correlate and clean up.
func (b *liveBackend) buildSeedISO(domName, poolName string, ci *vp.CloudInitSpec) (string, error) {
	if ci == nil {
		return "", nil
	}
	xorriso, err := exec.LookPath("xorriso")
	if err != nil {
		return "", fmt.Errorf("kvm: cloud-init needs xorriso to build the NoCloud seed ISO: %w", err)
	}
	if poolName == "" {
		poolName = "default"
	}
	dir, err := b.poolTargetDir(poolName)
	if err != nil {
		return "", err
	}

	base := sanitizeFileToken(domName)
	instanceID := "iid-" + base + "-cidata"
	userData := renderUserData(ci)
	metaData := renderMetaData(instanceID, ci.Hostname)

	// Stage the NoCloud files in a temp dir, then have xorriso pack them.
	stage, err := os.MkdirTemp("", "unihv-cidata-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(stage)

	files := []string{"user-data", "meta-data"}
	if err := os.WriteFile(filepath.Join(stage, "user-data"), []byte(userData), 0o600); err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stage, "meta-data"), []byte(metaData), 0o600); err != nil {
		return "", err
	}
	if nc := strings.TrimSpace(ci.NetworkConfig); nc != "" {
		if err := os.WriteFile(filepath.Join(stage, "network-config"), []byte(ci.NetworkConfig), 0o600); err != nil {
			return "", err
		}
		files = append(files, "network-config")
	}

	seedPath := filepath.Join(dir, base+"-seed.iso")
	// xorriso -as mkisofs -output <seed.iso> -volid cidata -joliet -rock <files...>
	// The volume id MUST be "cidata" for the NoCloud datasource to find it.
	args := []string{"-as", "mkisofs", "-output", seedPath, "-volid", "cidata", "-joliet", "-rock"}
	args = append(args, files...)
	cmd := exec.Command(xorriso, args...)
	cmd.Dir = stage // resolve the file args relative to the staging dir
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("kvm: xorriso seed build failed: %v: %s", err, strings.TrimSpace(string(out)))
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

// poolTargetDir resolves a storage pool's on-disk target directory from its XML
// (<pool><target><path>). Used to place the cloud-init seed ISO inside the pool so
// libvirt (and the qemu process) can read it.
func (b *liveBackend) poolTargetDir(poolName string) (string, error) {
	pool, err := b.poolHandle(poolName)
	if err != nil {
		return "", err
	}
	b.mu.RLock()
	l := b.l
	b.mu.RUnlock()
	if l == nil {
		return "", errNoConn
	}
	raw, err := l.StoragePoolGetXMLDesc(pool, 0)
	if err != nil {
		b.fail(err)
		return "", mapLibvirtErr(err)
	}
	dir := poolTargetPathFromXML(raw)
	if dir == "" {
		return "", fmt.Errorf("kvm: storage pool %q has no target path (not a dir-backed pool?)", poolName)
	}
	return dir, nil
}

// sanitizeFileToken derives a safe, reasonably-unique filename fragment from a VM
// name (keeps alnum, '-', '_'; caps length). Unlike sanitizeBridge it preserves the
// full name so per-VM seed ISOs don't collide on a shared truncated prefix.
func sanitizeFileToken(name string) string {
	var sb strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
			sb.WriteRune(r)
		}
		if sb.Len() >= 64 {
			break
		}
	}
	if sb.Len() == 0 {
		return "vm"
	}
	return sb.String()
}

// poolTargetPathFromXML reads <pool><target><path> from a storage pool XML dump.
func poolTargetPathFromXML(raw string) string {
	var px struct {
		Target struct {
			Path string `xml:"path"`
		} `xml:"target"`
	}
	if err := xml.Unmarshal([]byte(raw), &px); err != nil {
		return ""
	}
	return strings.TrimSpace(px.Target.Path)
}
