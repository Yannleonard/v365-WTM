package storage

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// azureBackend is a MINIMAL Azure Blob Storage REST client using stdlib net/http
// and SharedKey (account key) authentication — no Azure SDK. It is CGO-free and
// adds zero dependencies. It backs storing images / ISOs / backups.
//
// SharedKey signing follows the official Azure Storage spec:
// https://learn.microsoft.com/rest/api/storageservices/authorize-with-shared-key
type azureBackend struct {
	account   string
	key       []byte // decoded base64 account key
	container string
	// serviceURL is the blob service base, e.g. https://acct.blob.core.windows.net.
	// Overridable for Azurite / sovereign clouds.
	serviceURL string
	http       *http.Client
}

const azureAPIVersion = "2021-12-02"

func newAzureBackend(cfg Config) (Backend, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	account := firstNonEmpty(cfg.Account, cfg.Username)
	container := firstNonEmpty(cfg.Container, cfg.Target)
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(cfg.Secret))
	if err != nil {
		return nil, fmt.Errorf("azureblob: account key is not valid base64: %w", err)
	}
	svc := strings.TrimRight(strings.TrimSpace(cfg.ServiceURL), "/")
	if svc == "" {
		svc = fmt.Sprintf("https://%s.blob.core.windows.net", account)
	}
	return &azureBackend{
		account:    account,
		key:        key,
		container:  container,
		serviceURL: svc,
		http:       &http.Client{Timeout: 20 * time.Second},
	}, nil
}

func (b *azureBackend) Type() Type { return TypeAzureBlob }

// Test lists blobs in the container (List Blobs, comp=list, restype=container).
// A 2xx confirms the account key + container are valid and reachable.
func (b *azureBackend) Test(ctx context.Context) error {
	q := url.Values{}
	q.Set("restype", "container")
	q.Set("comp", "list")
	q.Set("maxresults", "1")
	target := fmt.Sprintf("%s/%s?%s", b.serviceURL, url.PathEscape(b.container), q.Encode())

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	if err := b.sign(req); err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("azureblob: list blobs: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("azureblob: list blobs failed: %s: %s", resp.Status, azureErrCode(string(body)))
	}
	return nil
}

// blobURL builds the blob URL (serviceURL/container/key), escaping key segments
// while preserving "/" separators.
func (b *azureBackend) blobURL(key string) string {
	parts := strings.Split(strings.TrimPrefix(key, "/"), "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return fmt.Sprintf("%s/%s/%s", b.serviceURL, url.PathEscape(b.container), strings.Join(parts, "/"))
}

// PutObject uploads r as a single block blob. Azure SharedKey signs Content-Length,
// so the body length must be known: when size<0 the stream is buffered to measure
// it (backup callers always pass a known size from a staged temp file). The single
// Put Blob path supports up to ~5000 MiB, sufficient for typical exported images.
func (b *azureBackend) PutObject(ctx context.Context, key string, r io.Reader, size int64) (int64, error) {
	var body io.Reader = r
	if size < 0 {
		buf, err := io.ReadAll(r)
		if err != nil {
			return 0, fmt.Errorf("azureblob: buffer body: %w", err)
		}
		body = strings.NewReader(string(buf))
		size = int64(len(buf))
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, b.blobURL(key), body)
	if err != nil {
		return 0, err
	}
	req.ContentLength = size
	req.Header.Set("x-ms-blob-type", "BlockBlob")
	req.Header.Set("Content-Type", "application/octet-stream")
	if err := b.sign(req); err != nil {
		return 0, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return 0, fmt.Errorf("azureblob: put blob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return 0, fmt.Errorf("azureblob: put blob failed: %s: %s", resp.Status, azureErrCode(string(rb)))
	}
	return size, nil
}

// GetObject opens a blob for reading (the caller closes the reader).
func (b *azureBackend) GetObject(ctx context.Context, key string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.blobURL(key), nil)
	if err != nil {
		return nil, err
	}
	if err := b.sign(req); err != nil {
		return nil, err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("azureblob: get blob: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("azureblob: get blob failed: %s: %s", resp.Status, azureErrCode(string(rb)))
	}
	return resp.Body, nil
}

// ListObjects lists blobs under prefix (List Blobs, paginated via NextMarker).
func (b *azureBackend) ListObjects(ctx context.Context, prefix string) ([]ObjectInfo, error) {
	var out []ObjectInfo
	marker := ""
	for {
		q := url.Values{}
		q.Set("restype", "container")
		q.Set("comp", "list")
		q.Set("prefix", prefix)
		if marker != "" {
			q.Set("marker", marker)
		}
		target := fmt.Sprintf("%s/%s?%s", b.serviceURL, url.PathEscape(b.container), q.Encode())
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return nil, err
		}
		if err := b.sign(req); err != nil {
			return nil, err
		}
		resp, err := b.http.Do(req)
		if err != nil {
			return nil, fmt.Errorf("azureblob: list blobs: %w", err)
		}
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("azureblob: list blobs failed: %s: %s", resp.Status, azureErrCode(string(rb)))
		}
		var lr azureListResult
		if err := xml.Unmarshal(rb, &lr); err != nil {
			return nil, fmt.Errorf("azureblob: parse list result: %w", err)
		}
		for _, bl := range lr.Blobs.Blob {
			out = append(out, ObjectInfo{Key: bl.Name, SizeBytes: bl.Properties.ContentLength})
		}
		if lr.NextMarker == "" {
			break
		}
		marker = lr.NextMarker
	}
	return out, nil
}

// DeleteObject removes a blob (a 404 is treated as success for idempotency).
func (b *azureBackend) DeleteObject(ctx context.Context, key string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, b.blobURL(key), nil)
	if err != nil {
		return err
	}
	if err := b.sign(req); err != nil {
		return err
	}
	resp, err := b.http.Do(req)
	if err != nil {
		return fmt.Errorf("azureblob: delete blob: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		rb, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("azureblob: delete blob failed: %s: %s", resp.Status, azureErrCode(string(rb)))
	}
	return nil
}

// azureListResult models the List Blobs XML response.
type azureListResult struct {
	XMLName    xml.Name `xml:"EnumerationResults"`
	NextMarker string   `xml:"NextMarker"`
	Blobs      struct {
		Blob []struct {
			Name       string `xml:"Name"`
			Properties struct {
				ContentLength int64 `xml:"Content-Length"`
			} `xml:"Properties"`
		} `xml:"Blob"`
	} `xml:"Blobs"`
}

// sign adds the x-ms-date / x-ms-version headers and the SharedKey Authorization
// header to req.
func (b *azureBackend) sign(req *http.Request) error {
	if req.Header.Get("x-ms-date") == "" {
		req.Header.Set("x-ms-date", time.Now().UTC().Format(http.TimeFormat))
	}
	req.Header.Set("x-ms-version", azureAPIVersion)

	strToSign := buildAzureStringToSign(b.account, req)
	mac := hmac.New(sha256.New, b.key)
	mac.Write([]byte(strToSign))
	sig := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	req.Header.Set("Authorization", fmt.Sprintf("SharedKey %s:%s", b.account, sig))
	return nil
}

// buildAzureStringToSign builds the SharedKey string-to-sign for a blob request.
// Exported via package-internal helper so it can be unit-tested against the
// documented Microsoft example vector.
func buildAzureStringToSign(account string, req *http.Request) string {
	h := req.Header
	get := func(k string) string { return h.Get(k) }

	contentLength := get("Content-Length")
	// Per spec, an empty or zero content length is signed as an empty string
	// (the 2015-02-21+ behavior).
	if contentLength == "0" {
		contentLength = ""
	}

	parts := []string{
		req.Method,
		get("Content-Encoding"),
		get("Content-Language"),
		contentLength,
		get("Content-MD5"),
		get("Content-Type"),
		get("Date"),
		get("If-Modified-Since"),
		get("If-Match"),
		get("If-None-Match"),
		get("If-Unmodified-Since"),
		get("Range"),
	}
	canonHeaders := canonicalizedAzureHeaders(h)
	canonResource := canonicalizedAzureResource(account, req.URL)
	return strings.Join(parts, "\n") + "\n" + canonHeaders + canonResource
}

// canonicalizedAzureHeaders builds the CanonicalizedHeaders block: all x-ms-*
// headers, lowercased, sorted, "name:value\n".
func canonicalizedAzureHeaders(h http.Header) string {
	var keys []string
	for k := range h {
		lk := strings.ToLower(k)
		if strings.HasPrefix(lk, "x-ms-") {
			keys = append(keys, lk)
		}
	}
	sort.Strings(keys)
	var sb strings.Builder
	for _, k := range keys {
		// Canonical form expects the original-cased value; values joined by comma if
		// multiple. Header.Get returns the first; join all for correctness.
		vals := h.Values(canonicalHeaderKey(h, k))
		val := strings.Join(vals, ",")
		val = strings.ReplaceAll(val, "\n", " ")
		sb.WriteString(k)
		sb.WriteString(":")
		sb.WriteString(strings.TrimSpace(val))
		sb.WriteString("\n")
	}
	return sb.String()
}

// canonicalHeaderKey finds the actual stored (textproto-canonical) key for a
// lowercased x-ms-* name so Header.Values returns its values.
func canonicalHeaderKey(h http.Header, lower string) string {
	for k := range h {
		if strings.ToLower(k) == lower {
			return k
		}
	}
	return lower
}

// canonicalizedAzureResource builds the CanonicalizedResource block:
// "/account/path" plus sorted query parameters "param:value\n".
func canonicalizedAzureResource(account string, u *url.URL) string {
	var sb strings.Builder
	sb.WriteString("/")
	sb.WriteString(account)
	sb.WriteString(u.EscapedPath())
	q := u.Query()
	if len(q) == 0 {
		return sb.String()
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, strings.ToLower(k))
	}
	sort.Strings(keys)
	for _, k := range keys {
		vals := q[k]
		// Query keys are case-insensitive; collect by lowercase match.
		if len(vals) == 0 {
			for origK, v := range q {
				if strings.ToLower(origK) == k {
					vals = v
					break
				}
			}
		}
		sort.Strings(vals)
		sb.WriteString("\n")
		sb.WriteString(k)
		sb.WriteString(":")
		sb.WriteString(strings.Join(vals, ","))
	}
	return sb.String()
}

// azureErrCode extracts the <Code> from an Azure error XML body for a concise msg.
func azureErrCode(body string) string {
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
