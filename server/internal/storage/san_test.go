package storage

import (
	"encoding/xml"
	"strings"
	"testing"
)

func TestRenderPoolXMLNFS(t *testing.T) {
	cfg := Config{Type: TypeNFS, Endpoint: "192.168.1.10", Target: "/export/images"}
	out := renderPoolXML(cfg, "unihv-nfs-test")
	mustWellFormed(t, out)
	for _, sub := range []string{
		"<pool type='netfs'>",
		"<host name='192.168.1.10'/>",
		"<dir path='/export/images'/>",
		"<format type='nfs'/>",
		"<name>unihv-nfs-test</name>",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("nfs pool XML missing %q:\n%s", sub, out)
		}
	}
}

func TestRenderPoolXMLISCSI(t *testing.T) {
	cfg := Config{Type: TypeISCSI, Endpoint: "10.0.0.5", Target: "iqn.2004-04.com.example:target0"}
	out := renderPoolXML(cfg, "unihv-iscsi")
	mustWellFormed(t, out)
	for _, sub := range []string{
		"<pool type='iscsi'>",
		"<host name='10.0.0.5' port='3260'/>",
		"<device path='iqn.2004-04.com.example:target0'/>",
		"<path>/dev/disk/by-path</path>",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("iscsi pool XML missing %q:\n%s", sub, out)
		}
	}
}

func TestRenderPoolXMLISCSICustomPort(t *testing.T) {
	cfg := Config{Type: TypeISCSI, Endpoint: "10.0.0.5:3261", Target: "iqn.x:y"}
	out := renderPoolXML(cfg, "p")
	if !strings.Contains(out, "<host name='10.0.0.5' port='3261'/>") {
		t.Errorf("custom port not honored:\n%s", out)
	}
}

func TestRenderPoolXMLSMB(t *testing.T) {
	cfg := Config{Type: TypeSMB, Endpoint: "fileserver", Target: `\\fileserver\isos`}
	out := renderPoolXML(cfg, "unihv-smb")
	mustWellFormed(t, out)
	for _, sub := range []string{
		"<pool type='netfs'>",
		"<host name='fileserver'/>",
		"<format type='cifs'/>",
		"<dir path='/isos'/>",
	} {
		if !strings.Contains(out, sub) {
			t.Errorf("smb pool XML missing %q:\n%s", sub, out)
		}
	}
}

func TestSMBHostFromUNC(t *testing.T) {
	if h := smbHost("", `\\server01\share`); h != "server01" {
		t.Errorf("smbHost from UNC: %q", h)
	}
	if s := smbShare(`\\server01\share`); s != "/share" {
		t.Errorf("smbShare from UNC: %q", s)
	}
	if s := smbShare("bareshare"); s != "/bareshare" {
		t.Errorf("smbShare from bare name: %q", s)
	}
}

func TestSanitizeName(t *testing.T) {
	if got := sanitizeName("unihv/nfs:1 2"); strings.ContainsAny(got, "/: ") {
		t.Errorf("sanitizeName left invalid chars: %q", got)
	}
}

func TestParseLibvirtEndpoint(t *testing.T) {
	cases := []struct{ in, net, addr string }{
		{"", "unix", "/var/run/libvirt/libvirt-sock"},
		{"tcp://127.0.0.1:16509", "tcp", "127.0.0.1:16509"},
		{"/var/run/x.sock", "unix", "/var/run/x.sock"},
		{"host:16509", "tcp", "host:16509"},
	}
	for _, c := range cases {
		n, a := parseLibvirtEndpoint(c.in)
		if n != c.net || a != c.addr {
			t.Errorf("parseLibvirtEndpoint(%q)=(%q,%q) want (%q,%q)", c.in, n, a, c.net, c.addr)
		}
	}
}

func TestConfigValidate(t *testing.T) {
	bad := []Config{
		{Type: TypeNFS, Endpoint: "h"},                                       // missing target
		{Type: TypeISCSI, Target: "iqn"},                                     // missing portal
		{Type: TypeSMB, Endpoint: "s"},                                       // missing share
		{Type: TypeAzureBlob, Username: "acct", Target: "c"},                 // missing key
		{Type: TypeS3, Username: "ak", Secret: "sk", Target: "b"},            // missing region
		{Type: "bogus"},                                                      // unknown type
	}
	for i, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("case %d: expected validation error for %+v", i, c)
		}
	}
	good := []Config{
		{Type: TypeNFS, Endpoint: "h", Target: "/e"},
		{Type: TypeAzureBlob, Username: "acct", Target: "c", Secret: "k"},
		{Type: TypeS3, Username: "ak", Secret: "sk", Target: "b", Region: "us-east-1"},
	}
	for i, c := range good {
		if err := c.Validate(); err != nil {
			t.Errorf("good case %d: unexpected error: %v", i, err)
		}
	}
}

func mustWellFormed(t *testing.T, doc string) {
	t.Helper()
	dec := xml.NewDecoder(strings.NewReader(doc))
	for {
		_, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				return
			}
			t.Fatalf("not well-formed XML: %v\n%s", err, doc)
		}
	}
}
