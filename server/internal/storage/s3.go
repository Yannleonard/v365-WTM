package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// s3Backend is a MINIMAL AWS S3 REST client using stdlib net/http and AWS
// Signature Version 4 — no AWS SDK. It is CGO-free and adds zero dependencies.
// It backs storing images / ISOs / backups. SigV4 follows the official spec:
// https://docs.aws.amazon.com/general/latest/gr/sigv4-create-canonical-request.html
type s3Backend struct {
	accessKey string
	secretKey string
	bucket    string
	region    string
	// endpoint is the host base, e.g. https://s3.us-east-1.amazonaws.com or a
	// custom S3-compatible endpoint (MinIO/Ceph). Path-style addressing is used so
	// any S3-compatible endpoint works without DNS bucket vhosts.
	endpoint string
	http     *http.Client
}

const s3Service = "s3"
const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" // sha256("")
const unsignedPayload = "UNSIGNED-PAYLOAD"                                                  // SigV4 streaming sentinel

func newS3Backend(cfg Config) (Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(strings.TrimSpace(cfg.ServiceURL), "/")
	if endpoint == "" {
		endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", cfg.Region)
	}
	return &s3Backend{
		accessKey: firstNonEmpty(cfg.Account, cfg.Username),
		secretKey: cfg.Secret,
		bucket:    firstNonEmpty(cfg.Container, cfg.Target),
		region:    cfg.Region,
		endpoint:  endpoint,
		http:      &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (b *s3Backend) Type() Type { return TypeS3 }

// Test lists objects in the bucket (ListObjectsV2). A 2xx confirms the
// credentials, region and bucket are valid and reachable.
func (b *s3Backend) Test(ctx context.Context) error {
	q := url.Values{}
	q.Set("list-type", "2")
	q.Set("max-keys", "1")
	target := fmt.Sprintf("%s/%s?%s", b.endpoint, url.PathEscape(b.bucket), q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if err := b.signV4(req, emptyPayloadHash, time.Now().UTC()); err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("s3: list objects: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3: list objects failed: %s: %s", resp.Status, s3ErrCode(string(body)))
	}
	return nil
}

// objURL builds the path-style object URL (endpoint/bucket/key), escaping each
// key segment so "/" separators are preserved in the path.
func (b *s3Backend) objURL(key string) string {
	parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return fmt.Sprintf("%s/%s/%s", b.endpoint, url.PathEscape(b.bucket), strings.Join(parts, "/"))
}

// PutObject uploads r under key using a single PutObject with an UNSIGNED-PAYLOAD
// body so multi-GB images stream without buffering. size sets Content-Length when
// known (>=0); otherwise the http client uses chunked transfer.
func (b *s3Backend) PutObject(ctx context.Context, key string, r io.Reader, size int64) (int64, error) {
	cr := &countingReader{r: r}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, b.objURL(key), cr)
	if err != nil {
		return 0, err
	}
	if size >= 0 {
		req.ContentLength = size
	}
	if err := b.signV4(req, unsignedPayload, time.Now().UTC()); err != nil {
		return 0, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return cr.n, fmt.Errorf("s3: put object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return cr.n, fmt.Errorf("s3: put object failed: %s: %s", resp.Status, s3ErrCode(string(body)))
	}
	return cr.n, nil
}

// GetObject opens key for reading (the caller closes the returned reader).
func (b *s3Backend) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.objURL(key), nil)
	if err != nil {
		return nil, err
	}
	if err := b.signV4(req, emptyPayloadHash, time.Now().UTC()); err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("s3: get object: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("s3: get object failed: %s: %s", resp.Status, s3ErrCode(string(body)))
	}
	return resp.Body, nil
}

// ListObjects returns every object under prefix (ListObjectsV2, paginated).
func (b *s3Backend) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	token := ""
	for {
		q := url.Values{}
		q.Set("list-type", "2")
		q.Set("prefix", prefix)
		if token != "" {
			q.Set("continuation-token", token)
		}
		target := fmt.Sprintf("%s/%s?%s", b.endpoint, url.PathEscape(b.bucket), q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := b.signV4(req, emptyPayloadHash, time.Now().UTC()); err != nil {
			return nil, err
		}
		resp, err := b.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("s3: list objects: %w", err)
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("s3: list objects failed: %s: %s", resp.Status, s3ErrCode(string(body)))
		}
		var lr s3ListResult
		if err := xml.Unmarshal(body, &lr); err != nil {
			return nil, fmt.Errorf("s3: parse list result: %w", err)
		}
		for _, c := range lr.Contents {
			out = append(out, ObjectInfo{Key: c.Key, SizeBytes: c.Size})
		}
		if !lr.IsTruncated || lr.NextContinuationToken == "" {
			break
		}
		token = lr.NextContinuationToken
	}
	return out, nil
}

// DeleteObject removes key (DELETE; a missing key returns 204 so it is idempotent).
func (b *s3Backend) DeleteObject(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.objURL(key), nil)
	if err != nil {
		return err
	}
	if err := b.signV4(req, emptyPayloadHash, time.Now().UTC()); err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("s3: delete object: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("s3: delete object failed: %s: %s", resp.Status, s3ErrCode(string(body)))
	}
	return nil
}

// s3ListResult / s3Object model the ListObjectsV2 XML response.
type s3ListResult struct {
	XMLName               xml.Name   `xml:"ListBucketResult"`
	IsTruncated           bool       `xml:"IsTruncated"`
	NextContinuationToken string     `xml:"NextContinuationToken"`
	Contents              []s3Object `xml:"Contents"`
}
type s3Object struct {
	Key  string `xml:"Key"`
	Size int64  `xml:"Size"`
}

// countingReader counts the bytes read through it (for byte accounting on upload).
type countingReader struct {
	r io.Reader
	n int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

// signV4 signs req with AWS SigV4 for the s3 service, setting x-amz-date,
// x-amz-content-sha256 and Authorization headers. payloadHash is the hex sha256 of
// the (empty for GET) body.
func (b *s3Backend) signV4(req *http.Request, payloadHash string, now time.Time) error {
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	if req.Host == "" {
		req.Host = req.URL.Host
	}
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("x-amz-content-sha256", payloadHash)

	canonHeaders, signedHeaders := canonicalS3Headers(req)
	canonRequest := strings.Join([]string{
		req.Method,
		canonicalS3URI(req.URL),
		canonicalS3Query(req.URL),
		canonHeaders,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, b.region, s3Service, "aws4_request"}, "/")
	hashedCanon := sha256Hex([]byte(canonRequest))
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hashedCanon,
	}, "\n")

	signingKey := deriveSigV4Key(b.secretKey, dateStamp, b.region, s3Service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	auth := fmt.Sprintf("AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		b.accessKey, scope, signedHeaders, signature)
	req.Header.Set("Authorization", auth)
	return nil
}

// canonicalS3Headers builds the canonical headers + signed-header list. Always
// includes host, x-amz-content-sha256 and x-amz-date.
func canonicalS3Headers(req *http.Request) (canon, signed string) {
	type hv struct{ k, v string }
	var items []hv
	add := func(k, v string) {
		items = append(items, hv{strings.ToLower(k), strings.TrimSpace(v)})
	}
	add("host", req.URL.Host)
	for k := range req.Header {
		lk := strings.ToLower(k)
		if lk == "x-amz-date" || lk == "x-amz-content-sha256" || strings.HasPrefix(lk, "x-amz-") {
			add(lk, req.Header.Get(k))
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].k < items[j].k })
	// Dedup (host could collide if set in header too).
	var sb strings.Builder
	var names []string
	seen := map[string]bool{}
	for _, it := range items {
		if seen[it.k] {
			continue
		}
		seen[it.k] = true
		sb.WriteString(it.k)
		sb.WriteString(":")
		sb.WriteString(it.v)
		sb.WriteString("\n")
		names = append(names, it.k)
	}
	return sb.String(), strings.Join(names, ";")
}

// canonicalS3URI returns the URI-encoded path (S3 does NOT double-encode the path).
func canonicalS3URI(u *url.URL) string {
	p := u.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

// canonicalS3Query returns the canonical, sorted, URI-encoded query string.
func canonicalS3Query(u *url.URL) string {
	q := u.Query()
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, awsURIEscape(k)+"="+awsURIEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// awsURIEscape encodes per RFC 3986 (AWS-style: spaces -> %20, keep -_.~).
func awsURIEscape(s string) string {
	const upperhex = "0123456789ABCDEF"
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			sb.WriteByte(c)
		} else {
			sb.WriteByte('%')
			sb.WriteByte(upperhex[c>>4])
			sb.WriteByte(upperhex[c&0x0f])
		}
	}
	return sb.String()
}

// deriveSigV4Key derives the SigV4 signing key.
func deriveSigV4Key(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// s3ErrCode extracts the <Code> from an S3 error XML body.
func s3ErrCode(body string) string {
	const open, close = "<Code>", "</Code>"
	i := strings.Index(body, open)
	if i < 0 {
		return strings.TrimSpace(body)
	}
	j := strings.Index(body[i:], close)
	if j < 0 {
		return strings.TrimSpace(body)
	}
	return body[i+len(open) : i+j]
}
