package backup

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/jothost/panel/shared/validate"
)

// The S3 destination.
//
// # Why this is written out rather than imported
//
// The Agent has no third-party dependencies, deliberately: it runs as root on
// somebody's server and every module in it is one more thing that can be
// compromised upstream. The AWS SDK is a large amount of code to gain four
// requests — PUT, GET, HEAD, DELETE — and the only hard part of those is the
// signature, which is a page of HMAC over a canonical string.
//
// # What is deliberately not implemented
//
// Multipart upload. A single PUT is capped by S3 at 5 GiB, so an archive larger
// than that is refused with a message saying so rather than half-uploaded.
// Multipart is not hard, but it introduces an upload that can be abandoned
// half-done and needs a lifecycle rule to clean up after itself, and a backup
// system whose failure mode is "silently accruing charges" is worse than one
// that says "this is too big for this destination". It is named in
// docs/PHASE14.md as a limitation rather than left to be discovered.

// MaxSinglePutBytes is S3's limit for one PUT.
const MaxSinglePutBytes int64 = 5 << 30

// s3Store talks to an S3-compatible service.
type s3Store struct {
	client    *http.Client
	endpoint  *url.URL
	region    string
	bucket    string
	prefix    string
	accessKey string
	secretKey string
	pathStyle bool
	now       func() time.Time
}

// s3Store builds and checks an S3 destination.
func (p *Provider) s3Store(dest Destination) (Store, error) {
	if err := validate.S3Endpoint(dest.Endpoint, dest.AllowInsecure); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := validate.S3Bucket(dest.Bucket); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := validate.S3Region(dest.Region); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if dest.AccessKey == "" || dest.SecretKey == "" {
		return nil, fmt.Errorf("%w: an S3 destination needs an access key and a secret key",
			ErrDestinationFailed)
	}
	if dest.Prefix != "" {
		if err := validate.BackupKey(strings.Trim(dest.Prefix, "/")); err != nil {
			return nil, fmt.Errorf("%w: the prefix is not usable: %s", ErrDestinationFailed, err)
		}
	}

	endpoint, err := url.Parse(dest.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	return &s3Store{
		client:    p.http,
		endpoint:  endpoint,
		region:    dest.Region,
		bucket:    dest.Bucket,
		prefix:    strings.Trim(dest.Prefix, "/"),
		accessKey: dest.AccessKey,
		secretKey: dest.SecretKey,
		// A bucket name with a dot in it breaks TLS certificate matching under
		// virtual-host addressing, and every self-hosted implementation wants
		// path style anyway. Honouring the setting but forcing it on for a
		// dotted name is what keeps a working configuration working.
		pathStyle: dest.PathStyle || strings.Contains(dest.Bucket, "."),
		now:       p.now,
	}, nil
}

func (s *s3Store) Kind() string { return validate.DestinationS3 }

// objectPath returns the URL path for a key.
func (s *s3Store) objectPath(key string) (string, error) {
	if err := validate.BackupKey(key); err != nil {
		return "", fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	full := key
	if s.prefix != "" {
		full = path.Join(s.prefix, key)
	}
	if s.pathStyle {
		return "/" + s.bucket + "/" + full, nil
	}
	return "/" + full, nil
}

// requestURL builds the absolute URL for a key.
func (s *s3Store) requestURL(key string) (*url.URL, string, error) {
	objectPath, err := s.objectPath(key)
	if err != nil {
		return nil, "", err
	}

	target := *s.endpoint
	target.Path = objectPath
	if !s.pathStyle {
		target.Host = s.bucket + "." + s.endpoint.Host
	}
	return &target, objectPath, nil
}

func (s *s3Store) Put(ctx context.Context, key, source string) error {
	info, err := os.Stat(source)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if info.Size() > MaxSinglePutBytes {
		return fmt.Errorf(
			"%w: this archive is %d bytes and an S3 destination accepts at most %d in one upload",
			ErrDestinationFailed, info.Size(), MaxSinglePutBytes)
	}

	// The payload digest is required by the signature, and it is the archive's
	// own digest — already computed while the archive was written, but read
	// again here so this function is correct on its own.
	digest, size, err := digestFile(source)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}

	handle, err := os.Open(source) //nolint:gosec // agent-owned staging path
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	defer func() { _ = handle.Close() }()

	response, err := s.do(ctx, http.MethodPut, key, handle, size, digest)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return s.statusError("upload", key, response)
	}
	return nil
}

func (s *s3Store) Get(ctx context.Context, key, destination string) error {
	response, err := s.do(ctx, http.MethodGet, key, nil, 0, emptyPayloadDigest)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotFound {
		return fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if response.StatusCode != http.StatusOK {
		return s.statusError("download", key, response)
	}

	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	// Bounded: the body is data from somewhere else, and an unbounded copy is
	// how a destination that has been taken over fills this host's disk.
	if _, err := io.Copy(out, io.LimitReader(response.Body, MaxArchiveBytes)); err != nil {
		_ = out.Close()
		_ = os.Remove(destination)
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	return out.Close()
}

func (s *s3Store) Stat(ctx context.Context, key string) (int64, error) {
	response, err := s.do(ctx, http.MethodHead, key, nil, 0, emptyPayloadDigest)
	if err != nil {
		return 0, err
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode == http.StatusNotFound {
		return 0, fmt.Errorf("%w: %s", ErrNotFound, key)
	}
	if response.StatusCode != http.StatusOK {
		return 0, s.statusError("check", key, response)
	}
	return response.ContentLength, nil
}

func (s *s3Store) Delete(ctx context.Context, key string) error {
	response, err := s.do(ctx, http.MethodDelete, key, nil, 0, emptyPayloadDigest)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()

	// S3 answers 204 for a delete whether or not the object was there, which
	// is exactly the behaviour retention wants.
	if response.StatusCode != http.StatusNoContent && response.StatusCode != http.StatusOK &&
		response.StatusCode != http.StatusNotFound {
		return s.statusError("delete", key, response)
	}
	return nil
}

// statusError turns a non-success response into a message worth showing.
//
// The service's own XML error is included, truncated: it names the bucket, the
// key and the reason far better than any message written here could, and an
// operator debugging a destination needs it. It is truncated because it is
// unbounded input that ends up in a job's failure text.
func (s *s3Store) statusError(action, key string, response *http.Response) error {
	const maxDetail = 512
	body, _ := io.ReadAll(io.LimitReader(response.Body, maxDetail))
	detail := strings.TrimSpace(string(body))
	if detail != "" {
		detail = ": " + collapseWhitespace(detail)
	}
	return fmt.Errorf("%w: could not %s %s (%s)%s",
		ErrDestinationFailed, action, key, response.Status, detail)
}

// collapseWhitespace makes an XML error one readable line.
func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

// emptyPayloadDigest is the SHA-256 of nothing, which SigV4 requires for a
// request with no body.
const emptyPayloadDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// do signs and sends one request.
func (s *s3Store) do(ctx context.Context, method, key string, body io.Reader,
	length int64, payloadDigest string,
) (*http.Response, error) {
	target, objectPath, err := s.requestURL(key)
	if err != nil {
		return nil, err
	}

	request, err := http.NewRequestWithContext(ctx, method, target.String(), body)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, err)
	}
	if body != nil {
		request.ContentLength = length
	}
	request.Header.Set("Content-Type", "application/gzip")

	canonicalPath := objectPath
	if !s.pathStyle {
		// Virtual-host addressing puts the bucket in the host, so the
		// canonical path is the object alone — the same string that is in the
		// URL. Getting this wrong produces a signature mismatch and nothing
		// else, which is why it is written down rather than inferred.
		canonicalPath = objectPath
	}

	if err := s.sign(request, canonicalPath, payloadDigest); err != nil {
		return nil, err
	}

	response, err := s.client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %s", ErrDestinationFailed, redactURL(err))
	}
	return response, nil
}

// redactURL removes any query string from an error before it is shown.
//
// A signed URL error would otherwise carry the signature into a job's failure
// message, and from there into the panel's audit trail.
func redactURL(err error) string {
	text := err.Error()
	if index := strings.Index(text, "?"); index >= 0 {
		if end := strings.IndexAny(text[index:], " \""); end > 0 {
			return text[:index] + "?…" + text[index+end:]
		}
		return text[:index] + "?…"
	}
	return text
}

// sign applies AWS Signature Version 4 to a request.
//
// The algorithm, in the order it happens: build a canonical request from the
// method, path, query, the headers being signed and the payload digest; hash
// it; build a string to sign from the timestamp, the scope and that hash;
// derive a key by HMAC-chaining the date, region, service and terminator onto
// the secret; and sign.
//
// Only three headers are signed — host, x-amz-content-sha256 and x-amz-date —
// because they are the three every implementation agrees on. Signing more,
// Content-Type in particular, is legal and is a common source of mismatches
// against non-AWS services that rewrite it.
func (s *s3Store) sign(request *http.Request, canonicalPath, payloadDigest string) error {
	const (
		algorithm = "AWS4-HMAC-SHA256"
		service   = "s3"
	)

	now := s.now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	host := request.URL.Host
	request.Header.Set("Host", host)
	request.Header.Set("X-Amz-Date", amzDate)
	request.Header.Set("X-Amz-Content-Sha256", payloadDigest)

	signedHeaders := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	sort.Strings(signedHeaders)

	var canonicalHeaders strings.Builder
	for _, name := range signedHeaders {
		value := ""
		switch name {
		case "host":
			value = host
		default:
			value = request.Header.Get(name)
		}
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteString(":")
		canonicalHeaders.WriteString(strings.TrimSpace(value))
		canonicalHeaders.WriteString("\n")
	}

	canonicalRequest := strings.Join([]string{
		request.Method,
		uriEncodePath(canonicalPath),
		request.URL.RawQuery,
		canonicalHeaders.String(),
		strings.Join(signedHeaders, ";"),
		payloadDigest,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		algorithm,
		amzDate,
		scope,
		hex.EncodeToString(sha256Sum([]byte(canonicalRequest))),
	}, "\n")

	signingKey := hmacSHA256([]byte("AWS4"+s.secretKey), dateStamp)
	signingKey = hmacSHA256(signingKey, s.region)
	signingKey = hmacSHA256(signingKey, service)
	signingKey = hmacSHA256(signingKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))

	request.Header.Set("Authorization", fmt.Sprintf(
		"%s Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		algorithm, s.accessKey, scope, strings.Join(signedHeaders, ";"), signature))
	return nil
}

// uriEncodePath encodes a path the way SigV4 requires.
//
// Not url.PathEscape: that leaves some characters unescaped that the signature
// expects escaped, and escapes the slashes that must stay. The rule is
// unreserved characters and the slash pass through; everything else becomes
// uppercase percent-encoding.
func uriEncodePath(value string) string {
	var out strings.Builder
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~', c == '/':
			out.WriteByte(c)
		default:
			// Two hex digits, always. A byte below 0x10 written as one digit
			// produces a signature that is wrong only for those bytes, which
			// is the kind of bug that works for a year and then does not.
			fmt.Fprintf(&out, "%%%02X", c)
		}
	}
	return out.String()
}

func sha256Sum(value []byte) []byte {
	sum := sha256.Sum256(value)
	return sum[:]
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}
