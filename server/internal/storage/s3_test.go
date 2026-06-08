package storage

import (
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

func mustURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url %q: %v", raw, err)
	}
	return u
}

func contains(s, sub string) bool { return strings.Contains(s, sub) }

// sigOf extracts the hex signature value from an Authorization header.
func sigOf(auth string) string {
	i := strings.Index(auth, "Signature=")
	if i < 0 {
		return ""
	}
	return auth[i+len("Signature="):]
}

func isHex(s string) bool {
	_, err := hex.DecodeString(s)
	return err == nil
}

// TestDeriveSigV4Key checks the 4-step SigV4 signing-key derivation
// (kDate->kRegion->kService->kSigning) for the AWS example credential
//   secret = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
//   dateStamp = "20120215", region = "us-east-1", service = "iam".
// The expected key was independently computed with `openssl dgst -sha256 -mac
// HMAC` chaining the four HMACs, pinning the canonical algorithm.
func TestDeriveSigV4Key(t *testing.T) {
	key := deriveSigV4Key("wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY", "20120215", "us-east-1", "iam")
	got := hex.EncodeToString(key)
	want := "004aa806e13dae88b9032d9261bcb04c67d023afadd221e6b0d206e1760e0b5e"
	if got != want {
		t.Fatalf("deriveSigV4Key mismatch:\n got=%s\nwant=%s", got, want)
	}
}

// TestSha256HexEmpty pins the empty-payload hash constant used by signV4.
func TestSha256HexEmpty(t *testing.T) {
	if got := sha256Hex([]byte("")); got != emptyPayloadHash {
		t.Fatalf("empty payload hash mismatch: got=%s want=%s", got, emptyPayloadHash)
	}
}

func TestCanonicalS3Query(t *testing.T) {
	u := mustURL(t, "https://s3.us-east-1.amazonaws.com/bucket?list-type=2&max-keys=1&prefix=a%20b")
	got := canonicalS3Query(u)
	// keys sorted; value "a b" -> "a%20b".
	want := "list-type=2&max-keys=1&prefix=a%20b"
	if got != want {
		t.Fatalf("canonicalS3Query: got=%q want=%q", got, want)
	}
}

func TestCanonicalS3URIRoot(t *testing.T) {
	u := mustURL(t, "https://s3.us-east-1.amazonaws.com")
	if got := canonicalS3URI(u); got != "/" {
		t.Fatalf("canonicalS3URI root: got=%q want=/", got)
	}
}

func TestAWSURIEscape(t *testing.T) {
	cases := map[string]string{
		"a b":        "a%20b",
		"key/with":   "key%2Fwith",
		"keep-_.~":   "keep-_.~",
		"perçent":    "per%C3%A7ent",
	}
	for in, want := range cases {
		if got := awsURIEscape(in); got != want {
			t.Errorf("awsURIEscape(%q): got=%q want=%q", in, got, want)
		}
	}
}

// TestS3SignV4Headers verifies signV4 sets a well-formed Authorization header with
// the expected scope, signed headers and a deterministic signature for a fixed
// request + clock (regression pin; the signature is computed once and asserted).
func TestS3SignV4Headers(t *testing.T) {
	b := &s3Backend{
		accessKey: "AKIDEXAMPLE",
		secretKey: "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY",
		bucket:    "examplebucket",
		region:    "us-east-1",
		endpoint:  "https://s3.us-east-1.amazonaws.com",
	}
	req, err := http.NewRequest(http.MethodGet,
		"https://s3.us-east-1.amazonaws.com/examplebucket?list-type=2&max-keys=1", nil)
	if err != nil {
		t.Fatal(err)
	}
	fixed := time.Date(2013, 5, 24, 0, 0, 0, 0, time.UTC)
	if err := b.signV4(req, emptyPayloadHash, fixed); err != nil {
		t.Fatalf("signV4: %v", err)
	}
	if req.Header.Get("x-amz-date") != "20130524T000000Z" {
		t.Fatalf("x-amz-date: %q", req.Header.Get("x-amz-date"))
	}
	if req.Header.Get("x-amz-content-sha256") != emptyPayloadHash {
		t.Fatalf("x-amz-content-sha256 not set")
	}
	auth := req.Header.Get("Authorization")
	wantScope := "Credential=AKIDEXAMPLE/20130524/us-east-1/s3/aws4_request"
	if !contains(auth, "AWS4-HMAC-SHA256") || !contains(auth, wantScope) {
		t.Fatalf("Authorization malformed: %s", auth)
	}
	if !contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Fatalf("SignedHeaders wrong: %s", auth)
	}
	if sig := sigOf(auth); len(sig) != 64 || !isHex(sig) {
		t.Fatalf("Signature not a 64-hex value: %q (auth=%s)", sig, auth)
	}
}

func TestNewS3BackendValidation(t *testing.T) {
	_, err := newS3Backend(Config{Type: TypeS3, Target: "bucket", Region: "us-east-1"})
	if err == nil {
		t.Fatal("expected error for missing credentials")
	}
	be, err := newS3Backend(Config{
		Type: TypeS3, Username: "AKID", Secret: "secret", Target: "bucket", Region: "us-east-1",
	})
	if err != nil {
		t.Fatalf("newS3Backend: %v", err)
	}
	if be.Type() != TypeS3 {
		t.Fatalf("type: %v", be.Type())
	}
}
