package kvm

import (
	"context"
	"strings"
	"testing"

	vp "github.com/gtek-it/castor/server/internal/vprovider"
)

func TestRenderUserData_FullSpec(t *testing.T) {
	ci := &vp.CloudInitSpec{
		Hostname:          "web01",
		Username:          "ops",
		Password:          "s3cr3t:pw",
		SSHAuthorizedKeys: []string{"ssh-ed25519 AAAA key1", "  ", "ssh-rsa BBBB key2"},
		RunCmd:            []string{"apt-get update", "touch /tmp/done"},
		UserDataExtra:     "package_update: true\n",
	}
	out := renderUserData(ci)

	if !strings.HasPrefix(out, "#cloud-config\n") {
		t.Fatalf("user-data must start with #cloud-config header, got:\n%s", out)
	}
	mustContain(t, out, "hostname: \"web01\"")
	mustContain(t, out, "fqdn: \"web01\"")
	mustContain(t, out, "users:")
	mustContain(t, out, "- name: \"ops\"")
	mustContain(t, out, "sudo: ALL=(ALL) NOPASSWD:ALL")
	mustContain(t, out, "lock_passwd: false")
	mustContain(t, out, "plain_text_passwd: \"s3cr3t:pw\"")
	mustContain(t, out, "ssh_authorized_keys:")
	mustContain(t, out, "- \"ssh-ed25519 AAAA key1\"")
	mustContain(t, out, "- \"ssh-rsa BBBB key2\"")
	mustContain(t, out, "ssh_pwauth: true")
	mustContain(t, out, "runcmd:")
	mustContain(t, out, "[ sh, -c, \"apt-get update\" ]")
	mustContain(t, out, "[ sh, -c, \"touch /tmp/done\" ]")
	mustContain(t, out, "package_update: true")

	// the blank ssh key entry must have been dropped
	if strings.Count(out, "ssh-") != 2 {
		t.Fatalf("expected exactly 2 ssh keys, blank entry should be dropped:\n%s", out)
	}
}

func TestRenderUserData_Minimal(t *testing.T) {
	ci := &vp.CloudInitSpec{Hostname: "only-host"}
	out := renderUserData(ci)
	mustContain(t, out, "#cloud-config")
	mustContain(t, out, "hostname: \"only-host\"")
	if strings.Contains(out, "users:") {
		t.Fatalf("no username -> no users block expected:\n%s", out)
	}
	if strings.Contains(out, "ssh_pwauth") {
		t.Fatalf("no password -> no ssh_pwauth expected:\n%s", out)
	}
	if strings.Contains(out, "runcmd") {
		t.Fatalf("no runcmd expected:\n%s", out)
	}
}

func TestRenderMetaData(t *testing.T) {
	out := renderMetaData("iid-vm1-cidata", "vm1")
	mustContain(t, out, "instance-id: \"iid-vm1-cidata\"")
	mustContain(t, out, "local-hostname: \"vm1\"")

	// no hostname -> no local-hostname line
	out2 := renderMetaData("iid-x", "")
	mustContain(t, out2, "instance-id: \"iid-x\"")
	if strings.Contains(out2, "local-hostname") {
		t.Fatalf("empty hostname must omit local-hostname:\n%s", out2)
	}
}

func TestYamlScalar_Escaping(t *testing.T) {
	if got := yamlScalar(`a"b\c`); got != `"a\"b\\c"` {
		t.Fatalf("yamlScalar escaping wrong: %s", got)
	}
}

// TestCreateVM_SimNoSeed proves the sim path (no extBackend / no live) still
// succeeds when a CloudInit spec is present — it simply produces no seed ISO.
func TestCreateVM_SimNoSeed(t *testing.T) {
	p := New("kvm-test")
	_, err := p.CreateVM(context.Background(), vp.VMSpec{
		Name: "ci-sim", VCPUs: 1, MemoryMB: 256,
		CloudInit: &vp.CloudInitSpec{Hostname: "h", Username: "u"},
	})
	if err != nil {
		t.Fatalf("CreateVM with CloudInit on sim backend should succeed (no-op seed), got: %v", err)
	}
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected output to contain %q, full output:\n%s", needle, haystack)
	}
}
