package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"
)

func TestCanonicalizedAzureResource(t *testing.T) {
	u := mustURL(t, "https://myaccount.blob.core.windows.net/mycontainer?restype=container&comp=list&maxresults=1")
	got := canonicalizedAzureResource("myaccount", u)
	// "/account/path" then sorted lowercase "param:value" lines.
	want := "/myaccount/mycontainer\ncomp:list\nmaxresults:1\nrestype:container"
	if got != want {
		t.Fatalf("canonicalizedAzureResource:\n got=%q\nwant=%q", got, want)
	}
}

func TestCanonicalizedAzureHeaders(t *testing.T) {
	h := http.Header{}
	h.Set("x-ms-date", "Fri, 26 Jun 2015 23:39:12 GMT")
	h.Set("x-ms-version", "2015-02-21")
	h.Set("Content-Type", "text/plain") // not x-ms-*, must be excluded
	got := canonicalizedAzureHeaders(h)
	want := "x-ms-date:Fri, 26 Jun 2015 23:39:12 GMT\nx-ms-version:2015-02-21\n"
	if got != want {
		t.Fatalf("canonicalizedAzureHeaders:\n got=%q\nwant=%q", got, want)
	}
}

// TestBuildAzureStringToSign pins the SharedKey string-to-sign layout for a GET
// List Blobs request (the connectivity probe). The 12 blank-able verb lines, the
// canonical x-ms headers and the canonical resource must be in the exact order.
func TestBuildAzureStringToSign(t *testing.T) {
	req, err := http.NewRequest(http.MethodGet,
		"https://myaccount.blob.core.windows.net/mycontainer?restype=container&comp=list", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("x-ms-date", "Fri, 26 Jun 2015 23:39:12 GMT")
	req.Header.Set("x-ms-version", "2021-12-02")

	got := buildAzureStringToSign("myaccount", req)
	want := strings.Join([]string{
		"GET", // method
		"",     // content-encoding
		"",     // content-language
		"",     // content-length
		"",     // content-md5
		"",     // content-type
		"",     // date
		"",     // if-modified-since
		"",     // if-match
		"",     // if-none-match
		"",     // if-unmodified-since
		"",     // range
	}, "\n") + "\n" +
		"x-ms-date:Fri, 26 Jun 2015 23:39:12 GMT\nx-ms-version:2021-12-02\n" +
		"/myaccount/mycontainer\ncomp:list\nrestype:container"
	if got != want {
		t.Fatalf("buildAzureStringToSign:\n got=%q\nwant=%q", got, want)
	}
}

// TestAzureSignProducesValidHMAC verifies sign() sets a SharedKey Authorization
// header whose signature is the HMAC-SHA256 of the string-to-sign under the
// (base64-decoded) account key — i.e. it round-trips correctly.
func TestAzureSignProducesValidHMAC(t *testing.T) {
	// A dummy 32-byte base64 key.
	rawKey := make([]byte, 32)
	for i := range rawKey {
		rawKey[i] = byte(i)
	}
	keyB64 := base64.StdEncoding.EncodeToString(rawKey)

	be, err := newAzureBackend(Config{
		Type: TypeAzureBlob, Username: "myaccount", Target: "mycontainer", Secret: keyB64,
	})
	if err != nil {
		t.Fatalf("newAzureBackend: %v", err)
	}
	ab := be.(*azureBackend)

	req, _ := http.NewRequest(http.MethodGet,
		"https://myaccount.blob.core.windows.net/mycontainer?restype=container&comp=list", nil)
	req.Header.Set("x-ms-date", "Fri, 26 Jun 2015 23:39:12 GMT")
	if err := ab.sign(req); err != nil {
		t.Fatalf("sign: %v", err)
	}
	auth := req.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "SharedKey myaccount:") {
		t.Fatalf("Authorization prefix wrong: %s", auth)
	}
	// Recompute the expected signature from the string-to-sign and compare.
	sts := buildAzureStringToSign("myaccount", req)
	mac := hmac.New(sha256.New, rawKey)
	mac.Write([]byte(sts))
	wantSig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	gotSig := strings.TrimPrefix(auth, "SharedKey myaccount:")
	if gotSig != wantSig {
		t.Fatalf("signature mismatch:\n got=%s\nwant=%s", gotSig, wantSig)
	}
}

func TestAzureErrCode(t *testing.T) {
	body := `<?xml version="1.0"?><Error><Code>ContainerNotFound</Code><Message>nope</Message></Error>`
	if got := azureErrCode(body); got != "ContainerNotFound" {
		t.Fatalf("azureErrCode: %q", got)
	}
}
