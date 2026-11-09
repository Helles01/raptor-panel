// Package s3 is the small part of the S3 API that the backups of the panel need: a multipart upload that a node does through addresses the
// panel has signed, a signed address to download from, and the removal of an object. Requests are signed with AWS Signature Version 4, written
// here with the standard library only; nothing is fetched or installed for it. It speaks to Amazon S3 and to the servers that copy its API
// (MinIO and the like), by address style and endpoint.
//
// The secret key is never part of a result, an error or a log line. A signed address does carry a signature and the key id, and is as good as
// the access it grants until it expires, so it is not logged either.
package s3

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// ContentType is what the archives of backups are stored as.
const ContentType = "application/x-gzip"

// The limits of the protocol that the panel keeps to.
const (
	// MaxParts is the most parts a multipart upload may have.
	MaxParts = 10000
	// MinPartSize is the smallest part S3 accepts, except for the last one.
	MinPartSize = 5 << 20
	// MaxPartSize is the largest part S3 accepts.
	MaxPartSize = 5 << 30
	// MaxValidity is the longest a signed address can be good for (seven days).
	MaxValidity = 7 * 24 * time.Hour

	maxResponse = 1 << 20
	shortWait   = 30 * time.Second
	// completeWait is how long finishing an upload may take: S3 puts the parts together before it answers, which takes minutes for a big archive.
	completeWait = 20 * time.Minute
)

// Config says where the bucket is and how to sign in to it.
type Config struct {
	// Endpoint is the address of an S3-compatible server, such as https://minio.example.com:9000. Empty is Amazon S3 in Region.
	Endpoint string
	Region   string
	Bucket   string
	// AccessKey and SecretKey sign the requests.
	AccessKey string
	SecretKey string
	// PathStyle puts the bucket in the path (https://host/bucket/key) instead of in the host name (https://bucket.host/key).
	PathStyle bool
	// StorageClass is the class new archives are stored in; empty is the bucket's default.
	StorageClass string
}

var (
	bucketPattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	regionPattern       = regexp.MustCompile(`^[a-z0-9-]{2,32}$`)
	accessKeyPattern    = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,128}$`)
	secretKeyPattern    = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
	storageClassPattern = regexp.MustCompile(`^[A-Z_]{1,32}$`)
	uploadIDPattern     = regexp.MustCompile(`^[A-Za-z0-9._~+=/-]+$`)
)

// Validate checks the settings against allow-lists. Nothing is looked up on the network.
func (c Config) Validate() error {
	switch {
	case !bucketPattern.MatchString(c.Bucket):
		return errors.New("the bucket must be 3 to 63 characters: lower-case letters, digits, dots and hyphens")
	case !regionPattern.MatchString(c.Region):
		return errors.New("the region must be 2 to 32 characters: lower-case letters, digits and hyphens")
	case !accessKeyPattern.MatchString(c.AccessKey):
		return errors.New("the access key is not set, or has characters it should not have")
	case !secretKeyPattern.MatchString(c.SecretKey):
		return errors.New("the secret key is not set, or is not printable characters without spaces")
	case c.StorageClass != "" && !storageClassPattern.MatchString(c.StorageClass):
		return errors.New("the storage class must be capital letters and underscores, such as STANDARD_IA")
	}
	if c.Endpoint != "" {
		u, err := url.Parse(c.Endpoint)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return errors.New("the endpoint must be an http(s) address such as https://minio.example.com:9000")
		}
		if u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("the endpoint must not have credentials, a path, a query or a fragment")
		}
	}
	return nil
}

func redacted(set bool) string {
	if set {
		return "[redacted]"
	}
	return "[empty]"
}

// String never includes the keys.
func (c Config) String() string {
	return fmt.Sprintf("s3{endpoint=%q region=%s bucket=%s path_style=%t storage_class=%s access_key=%s secret_key=%s}",
		c.Endpoint, c.Region, c.Bucket, c.PathStyle, c.StorageClass, redacted(c.AccessKey != ""), redacted(c.SecretKey != ""))
}

// GoString keeps %#v from printing the keys.
func (c Config) GoString() string { return c.String() }

// LogValue keeps slog from printing the keys.
func (c Config) LogValue() slog.Value { return slog.StringValue(c.String()) }

// Options are what a Client can be given besides its settings.
type Options struct {
	// HTTP defaults to a client that does not follow redirects: a signed request cannot be sent on to another host.
	HTTP *http.Client
	// Now defaults to the clock.
	Now func() time.Time
}

// Client talks to one bucket.
type Client struct {
	cfg    Config
	scheme string
	host   string
	// prefix is "/bucket" with path-style addresses and empty otherwise.
	prefix string
	http   *http.Client
	now    func() time.Time
}

// New checks the settings and returns a Client.
func New(cfg Config, o Options) (*Client, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("s3: %w", err)
	}
	c := &Client{cfg: cfg, http: o.HTTP, now: o.Now, scheme: "https"}
	if c.http == nil {
		c.http = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	if c.now == nil {
		c.now = time.Now
	}

	host := "s3." + cfg.Region + ".amazonaws.com"
	if cfg.Endpoint != "" {
		u, _ := url.Parse(cfg.Endpoint) // checked by Validate
		c.scheme, host = u.Scheme, u.Host
	}
	if cfg.PathStyle {
		c.host, c.prefix = host, "/"+cfg.Bucket
	} else {
		c.host = cfg.Bucket + "." + host
	}
	return c, nil
}

// Part is one piece of a multipart upload as the server knows it.
type Part struct {
	Number int
	// ETag is as the server answered the upload of the part, quotes included.
	ETag string
}

// Error is what the server answered when it refused a request.
type Error struct {
	Status    int
	Code      string
	Message   string
	RequestID string
}

func (e *Error) Error() string {
	if e.Code == "" {
		return fmt.Sprintf("s3: the server answered %d", e.Status)
	}
	return fmt.Sprintf("s3: %s (%d): %s", e.Code, e.Status, e.Message)
}

// IsCode says whether err is the server's refusal with that code, such as NoSuchUpload.
func IsCode(err error, code string) bool {
	var e *Error
	return errors.As(err, &e) && e.Code == code
}

// --- the signature

const (
	algorithm       = "AWS4-HMAC-SHA256"
	unsignedPayload = "UNSIGNED-PAYLOAD"
	emptyHash       = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	dateFormat      = "20060102T150405Z"
)

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func hexSHA256(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// signingKey is the key for one day, region and service.
func (c *Client) signingKey(day string) []byte {
	k := hmacSHA256([]byte("AWS4"+c.cfg.SecretKey), day)
	k = hmacSHA256(k, c.cfg.Region)
	k = hmacSHA256(k, "s3")
	return hmacSHA256(k, "aws4_request")
}

func (c *Client) scope(day string) string { return day + "/" + c.cfg.Region + "/s3/aws4_request" }

func unreserved(b byte) bool {
	return b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || b == '-' || b == '_' || b == '.' || b == '~'
}

// escape percent-encodes everything but the unreserved characters, as the signature wants; a slash is kept when keepSlash is set (in a path).
func escape(s string, keepSlash bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch ch := s[i]; {
		case unreserved(ch), keepSlash && ch == '/':
			b.WriteByte(ch)
		default:
			fmt.Fprintf(&b, "%%%02X", ch)
		}
	}
	return b.String()
}

// canonicalQuery sorts the parameters by name and then by value, each side encoded.
func canonicalQuery(q url.Values) string {
	type pair struct{ k, v string }
	var pairs []pair
	for k, vs := range q {
		for _, v := range vs {
			pairs = append(pairs, pair{escape(k, false), escape(v, false)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	parts := make([]string, len(pairs))
	for i, p := range pairs {
		parts[i] = p.k + "=" + p.v
	}
	return strings.Join(parts, "&")
}

func collapse(s string) string { return strings.Join(strings.Fields(s), " ") }

// signature is the signature of a request in the canonical form: method, escaped path, canonical query, the headers that are signed (name to value),
// and the hash of the payload.
func (c *Client) signature(method, path, query string, headers map[string]string, payloadHash, amzDate string) (signed, sig string) {
	names := make([]string, 0, len(headers))
	for n := range headers {
		names = append(names, n)
	}
	sort.Strings(names)
	var canonical strings.Builder
	for _, n := range names {
		canonical.WriteString(n + ":" + collapse(headers[n]) + "\n")
	}
	signed = strings.Join(names, ";")
	request := strings.Join([]string{method, path, query, canonical.String(), signed, payloadHash}, "\n")
	day := amzDate[:8]
	toSign := strings.Join([]string{algorithm, amzDate, c.scope(day), hexSHA256([]byte(request))}, "\n")
	return signed, hex.EncodeToString(hmacSHA256(c.signingKey(day), toSign))
}

// objectPath is the escaped path of a key, with the bucket in front of it for path-style addresses.
func (c *Client) objectPath(key string) string {
	return escape(c.prefix, true) + "/" + escape(key, true)
}

func checkKey(key string) error {
	if key == "" || len(key) > 1024 || !utf8.ValidString(key) || strings.HasPrefix(key, "/") || strings.ContainsFunc(key, func(r rune) bool { return r < ' ' || r == 0x7f }) {
		return errors.New("s3: not a usable key")
	}
	return nil
}

func checkUploadID(id string) error {
	if len(id) > 1024 || !uploadIDPattern.MatchString(id) {
		return errors.New("s3: not a usable upload id")
	}
	return nil
}

// presign makes an address that can be used by whoever holds it, for method, until valid has passed. No other header is signed than the host, so the
// one who uses it may send what else they like.
func (c *Client) presign(method, key string, extra url.Values, valid time.Duration) (string, error) {
	if err := checkKey(key); err != nil {
		return "", err
	}
	if valid < time.Second || valid > MaxValidity {
		return "", errors.New("s3: a signed address is good for 1 second to 7 days")
	}
	now := c.now().UTC()
	amzDate := now.Format(dateFormat)
	q := url.Values{}
	for k, vs := range extra {
		q[k] = vs
	}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", c.cfg.AccessKey+"/"+c.scope(amzDate[:8]))
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", strconv.FormatInt(int64(valid/time.Second), 10))
	q.Set("X-Amz-SignedHeaders", "host")

	path := c.objectPath(key)
	query := canonicalQuery(q)
	_, sig := c.signature(method, path, query, map[string]string{"host": c.host}, unsignedPayload, amzDate)
	return c.scheme + "://" + c.host + path + "?" + query + "&X-Amz-Signature=" + sig, nil
}

// PresignGet is an address the archive can be downloaded from for the time given.
func (c *Client) PresignGet(key string, valid time.Duration) (string, error) {
	return c.presign(http.MethodGet, key, nil, valid)
}

// PresignUploadPart is an address one part of a multipart upload can be sent to with PUT.
func (c *Client) PresignUploadPart(key, uploadID string, part int, valid time.Duration) (string, error) {
	if err := checkUploadID(uploadID); err != nil {
		return "", err
	}
	if part < 1 || part > MaxParts {
		return "", errors.New("s3: a part number is 1 to 10000")
	}
	return c.presign(http.MethodPut, key, url.Values{"partNumber": {strconv.Itoa(part)}, "uploadId": {uploadID}}, valid)
}

// --- the requests

type reply struct {
	status int
	header http.Header
	body   []byte
}

// do sends one signed request. A refusal is an *Error. With a body the caller has hashed it, so that the hash is made once.
func (c *Client) do(ctx context.Context, method, key string, query url.Values, header map[string]string, body []byte, wait time.Duration) (reply, error) {
	if err := checkKey(key); err != nil {
		return reply{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()

	path := c.objectPath(key)
	canon := canonicalQuery(query)
	target := c.scheme + "://" + c.host + path
	if canon != "" {
		target += "?" + canon
	}
	var reader io.Reader
	payloadHash := emptyHash
	if len(body) > 0 {
		reader, payloadHash = bytes.NewReader(body), hexSHA256(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return reply{}, fmt.Errorf("s3: make the request: %w", err)
	}
	amzDate := c.now().UTC().Format(dateFormat)
	signedValues := map[string]string{"host": c.host, "x-amz-content-sha256": payloadHash, "x-amz-date": amzDate}
	req.Header.Set("x-amz-content-sha256", payloadHash)
	req.Header.Set("x-amz-date", amzDate)
	for k, v := range header {
		req.Header.Set(k, v)
		signedValues[strings.ToLower(k)] = v
	}
	signed, sig := c.signature(method, path, canon, signedValues, payloadHash, amzDate)
	req.Header.Set("Authorization", fmt.Sprintf("%s Credential=%s/%s, SignedHeaders=%s, Signature=%s", algorithm, c.cfg.AccessKey, c.scope(amzDate[:8]), signed, sig))

	resp, err := c.http.Do(req)
	if err != nil {
		// The address of the request carries no secret, but the error of the transport is cut to its cause all the same.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return reply{}, fmt.Errorf("s3: %s: %w", method, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if err != nil {
		return reply{}, fmt.Errorf("s3: read the answer: %w", err)
	}
	r := reply{status: resp.StatusCode, header: resp.Header, body: data}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return r, errorOf(r)
	}
	return r, nil
}

type xmlError struct {
	XMLName   xml.Name `xml:"Error"`
	Code      string   `xml:"Code"`
	Message   string   `xml:"Message"`
	RequestID string   `xml:"RequestId"`
}

// clean makes text that came from the server fit for a log line.
func clean(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < ' ' || r == 0x7f {
			return ' '
		}
		return r
	}, strings.ToValidUTF8(s, ""))
	if len(s) > 300 {
		s = s[:300]
	}
	return strings.TrimSpace(s)
}

func errorOf(r reply) error {
	var x xmlError
	if len(r.body) > 0 && xml.Unmarshal(r.body, &x) == nil && x.Code != "" {
		return &Error{Status: r.status, Code: clean(x.Code), Message: clean(x.Message), RequestID: clean(x.RequestID)}
	}
	return &Error{Status: r.status}
}

// failedWith200 is the error in an answer that says 200: finishing an upload may answer so and put the refusal in the body.
func failedWith200(r reply) error {
	var x xmlError
	if xml.Unmarshal(r.body, &x) == nil && x.XMLName.Local == "Error" {
		return &Error{Status: r.status, Code: clean(x.Code), Message: clean(x.Message), RequestID: clean(x.RequestID)}
	}
	return nil
}

// CreateMultipartUpload starts an upload of the object and returns its id.
func (c *Client) CreateMultipartUpload(ctx context.Context, key string) (string, error) {
	header := map[string]string{"Content-Type": ContentType}
	if c.cfg.StorageClass != "" {
		header["x-amz-storage-class"] = c.cfg.StorageClass
	}
	r, err := c.do(ctx, http.MethodPost, key, url.Values{"uploads": {""}}, header, nil, shortWait)
	if err != nil {
		return "", err
	}
	var out struct {
		UploadID string `xml:"UploadId"`
	}
	if err := xml.Unmarshal(r.body, &out); err != nil || checkUploadID(out.UploadID) != nil {
		return "", errors.New("s3: the server did not answer with a usable upload id")
	}
	return out.UploadID, nil
}

type completeXML struct {
	XMLName xml.Name  `xml:"CompleteMultipartUpload"`
	Parts   []partXML `xml:"Part"`
}

type partXML struct {
	PartNumber int    `xml:"PartNumber"`
	ETag       string `xml:"ETag"`
}

// CompleteMultipartUpload puts the parts together into the object. The parts are given in any order.
func (c *Client) CompleteMultipartUpload(ctx context.Context, key, uploadID string, parts []Part) error {
	if err := checkUploadID(uploadID); err != nil {
		return err
	}
	if len(parts) == 0 || len(parts) > MaxParts {
		return errors.New("s3: an upload has 1 to 10000 parts")
	}
	sorted := append([]Part(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Number < sorted[j].Number })
	body := completeXML{}
	for i, p := range sorted {
		if p.Number < 1 || p.Number > MaxParts || p.ETag == "" || len(p.ETag) > 256 || strings.ContainsFunc(p.ETag, func(r rune) bool { return r < ' ' || r == 0x7f }) {
			return errors.New("s3: a part has no usable number or entity tag")
		}
		if i > 0 && sorted[i-1].Number == p.Number {
			return errors.New("s3: two parts have the same number")
		}
		body.Parts = append(body.Parts, partXML{PartNumber: p.Number, ETag: p.ETag})
	}
	data, err := xml.Marshal(body)
	if err != nil {
		return fmt.Errorf("s3: make the list of parts: %w", err)
	}
	r, err := c.do(ctx, http.MethodPost, key, url.Values{"uploadId": {uploadID}}, map[string]string{"Content-Type": "application/xml"}, data, completeWait)
	if err != nil {
		return err
	}
	return failedWith200(r)
}

// ListParts lists the parts the server has for an upload, all pages of them.
func (c *Client) ListParts(ctx context.Context, key, uploadID string) ([]Part, error) {
	if err := checkUploadID(uploadID); err != nil {
		return nil, err
	}
	var all []Part
	marker := 0
	for page := 0; page < MaxParts; page++ {
		q := url.Values{"uploadId": {uploadID}, "max-parts": {"1000"}}
		if marker > 0 {
			q.Set("part-number-marker", strconv.Itoa(marker))
		}
		r, err := c.do(ctx, http.MethodGet, key, q, nil, nil, shortWait)
		if err != nil {
			return nil, err
		}
		var out struct {
			Truncated bool `xml:"IsTruncated"`
			Next      int  `xml:"NextPartNumberMarker"`
			Parts     []struct {
				PartNumber int    `xml:"PartNumber"`
				ETag       string `xml:"ETag"`
			} `xml:"Part"`
		}
		if err := xml.Unmarshal(r.body, &out); err != nil {
			return nil, errors.New("s3: the list of parts is not understood")
		}
		for _, p := range out.Parts {
			all = append(all, Part{Number: p.PartNumber, ETag: p.ETag})
		}
		if len(all) > MaxParts {
			return nil, errors.New("s3: the upload has more parts than the protocol allows")
		}
		if !out.Truncated || out.Next <= marker {
			return all, nil
		}
		marker = out.Next
	}
	return nil, errors.New("s3: the list of parts does not end")
}

// AbortMultipartUpload throws away an upload and the parts of it that were sent.
func (c *Client) AbortMultipartUpload(ctx context.Context, key, uploadID string) error {
	if err := checkUploadID(uploadID); err != nil {
		return err
	}
	_, err := c.do(ctx, http.MethodDelete, key, url.Values{"uploadId": {uploadID}}, nil, nil, shortWait)
	return err
}

// DeleteObject removes an object. S3 answers the same whether it was there or not.
func (c *Client) DeleteObject(ctx context.Context, key string) error {
	_, err := c.do(ctx, http.MethodDelete, key, nil, nil, nil, shortWait)
	return err
}

// Head says whether the object is there, and how large.
func (c *Client) Head(ctx context.Context, key string) (size int64, found bool, err error) {
	r, err := c.do(ctx, http.MethodHead, key, nil, nil, nil, shortWait)
	var e *Error
	if errors.As(err, &e) && e.Status == http.StatusNotFound {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, err
	}
	n, perr := strconv.ParseInt(r.header.Get("Content-Length"), 10, 64)
	if perr != nil || n < 0 {
		return 0, true, errors.New("s3: the server did not say how large the object is")
	}
	return n, true, nil
}
