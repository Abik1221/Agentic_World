// Package media stores and serves user-uploaded images.
//
// It exists because the platform had object storage and no way to put anything in
// it: MinIO shipped with the prod stack, the deploy passed S3_* credentials into the
// arena's environment, and no Go code read them. So an avatar had nowhere durable to
// go, the client kept it as a data: URL in localStorage, and a developer who set a
// photo on their phone saw nothing at all on their laptop.
//
// Two rules shape everything here:
//
//  1. NEVER STORE WHAT WAS UPLOADED. Every image is decoded and re-encoded from
//     pixels (see image.go). That drops EXIF, colour profiles, trailing archives and
//     anything else riding along in the container — a file that renders in other
//     developers' browsers must contain only what we produced.
//
//  2. THE KEY IS THE CONTENT. Objects are named by the SHA-256 of the bytes we
//     encoded, so the same image uploaded twice is one object, a key can never point
//     at different bytes later, and the serve path is safe to cache immutably and
//     forever. It also means an upload is idempotent: a retry after a timeout
//     overwrites itself with identical content.
//
// The S3 API is spoken directly with net/http + SigV4 rather than through an SDK.
// Two PUT/GET calls against a known endpoint do not justify pulling a cloud SDK (and
// its transitive tree) into a service whose dependency list is otherwise this short;
// the signing is ~80 lines below and is covered by tests against a real MinIO.
package media

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Config is the object-store connection. Empty Endpoint or Bucket disables uploads
// (the handler then answers 503 rather than the arena refusing to boot: an avatar is
// not worth taking the platform down for).
type Config struct {
	Endpoint  string // e.g. http://minio:9000 — scheme required
	Bucket    string
	AccessKey string
	SecretKey string
	Region    string // SigV4 region; MinIO accepts anything, default us-east-1
	// PublicBase optionally overrides where clients are told to fetch objects from
	// (a CDN in front of the bucket). Empty ⇒ objects are served by the arena itself
	// via the media handler, which needs no bucket policy and no second hostname.
	PublicBase string
}

// Store is an S3-compatible object store client. Safe for concurrent use.
type Store struct {
	cfg    Config
	client *http.Client
}

// New builds a Store. A Store with an incomplete config is valid and reports
// Enabled() == false; every method on it fails cleanly rather than panicking, so a
// deployment without object storage behaves like one with uploads switched off.
func New(cfg Config) *Store {
	if cfg.Region == "" {
		cfg.Region = "us-east-1"
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	cfg.PublicBase = strings.TrimRight(cfg.PublicBase, "/")
	return &Store{
		cfg: cfg,
		// Generous next to a request timeout but finite: an upload is a few hundred
		// KB to a host on the same network, and a hung store must not pin a handler.
		client: &http.Client{Timeout: 30 * time.Second},
	}
}

// Enabled reports whether the store is configured well enough to use.
func (s *Store) Enabled() bool {
	return s != nil && s.cfg.Endpoint != "" && s.cfg.Bucket != "" &&
		s.cfg.AccessKey != "" && s.cfg.SecretKey != ""
}

// PublicBase returns the configured CDN prefix, or "" when the arena serves objects.
func (s *Store) PublicBase() string { return s.cfg.PublicBase }

// Put stores body at key with the given content type.
//
// Overwriting is intentional and safe here BECAUSE keys are content-addressed: the
// only thing that can be written to an existing key is the identical image. That is
// what makes a retried upload harmless.
func (s *Store) Put(ctx context.Context, key, contentType string, body []byte) error {
	if !s.Enabled() {
		return errUnavailable("object storage is not configured")
	}
	req, err := s.newRequest(ctx, http.MethodPut, key, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	// Immutable by construction, so tell any cache in the path the truth about it.
	req.Header.Set("Cache-Control", "public, max-age=31536000, immutable")
	if err := s.sign(req, body); err != nil {
		return err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("media: put %s: %w", key, err)
	}
	defer drain(resp)
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("media: put %s: store answered %s: %s", key, resp.Status, peek(resp))
	}
	return nil
}

// Object is a stored object being read back.
type Object struct {
	Body        io.ReadCloser
	ContentType string
	Size        int64
}

// Get streams an object back. The caller must Close Body.
func (s *Store) Get(ctx context.Context, key string) (*Object, error) {
	if !s.Enabled() {
		return nil, errUnavailable("object storage is not configured")
	}
	req, err := s.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	if err := s.sign(req, nil); err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media: get %s: %w", key, err)
	}
	if resp.StatusCode == http.StatusNotFound {
		drain(resp)
		return nil, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		defer drain(resp)
		return nil, fmt.Errorf("media: get %s: store answered %s", key, resp.Status)
	}
	return &Object{
		Body:        resp.Body,
		ContentType: resp.Header.Get("Content-Type"),
		Size:        resp.ContentLength,
	}, nil
}

// EnsureBucket creates the bucket when it does not exist. Called once at boot so a
// fresh volume does not make every upload fail on a missing bucket — the compose
// stack's minio-init does this too, and doing it here as well means the arena works
// against a plain S3 endpoint with no init container.
//
// Reports, never fails boot: an unreachable store at startup must not stop the
// platform serving matches.
func (s *Store) EnsureBucket(ctx context.Context) error {
	if !s.Enabled() {
		return errUnavailable("object storage is not configured")
	}
	// HEAD the bucket first — creating an existing bucket is an error on some
	// implementations, and this keeps the common path a single cheap call.
	head, err := s.newRequest(ctx, http.MethodHead, "", nil)
	if err != nil {
		return err
	}
	if err := s.sign(head, nil); err != nil {
		return err
	}
	resp, err := s.client.Do(head)
	if err != nil {
		return fmt.Errorf("media: head bucket: %w", err)
	}
	drain(resp)
	if resp.StatusCode/100 == 2 {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("media: head bucket: store answered %s", resp.Status)
	}

	put, err := s.newRequest(ctx, http.MethodPut, "", nil)
	if err != nil {
		return err
	}
	if err := s.sign(put, nil); err != nil {
		return err
	}
	cresp, err := s.client.Do(put)
	if err != nil {
		return fmt.Errorf("media: create bucket: %w", err)
	}
	defer drain(cresp)
	if cresp.StatusCode/100 != 2 {
		return fmt.Errorf("media: create bucket: store answered %s: %s", cresp.Status, peek(cresp))
	}
	return nil
}

// newRequest builds a path-style bucket request. Path style (host/bucket/key) rather
// than virtual-host style (bucket.host/key) because MinIO on a container hostname has
// no wildcard DNS, and it works against real S3 too.
func (s *Store) newRequest(ctx context.Context, method, key string, body []byte) (*http.Request, error) {
	u := s.cfg.Endpoint + "/" + s.cfg.Bucket
	if key != "" {
		u += "/" + escapePath(key)
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, u, rdr)
	if err != nil {
		return nil, fmt.Errorf("media: build request: %w", err)
	}
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	return req, nil
}

// escapePath percent-encodes each path segment, leaving the separators. url.PathEscape
// on the whole key would escape the slashes and flatten the prefix into one name.
func escapePath(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

// --- SigV4 ------------------------------------------------------------------
//
// AWS Signature Version 4, the subset these calls need: a single-chunk payload whose
// SHA-256 we already have in memory, no session token, no query signing.

const isoLayout = "20060102T150405Z"

func (s *Store) sign(req *http.Request, body []byte) error {
	now := time.Now().UTC()
	amzDate := now.Format(isoLayout)
	dateStamp := now.Format("20060102")

	payloadHash := sha256Hex(body)
	req.Header.Set("Host", req.URL.Host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)

	// Canonical headers: lowercase names, sorted, values trimmed. Only the headers we
	// sign appear here, and the signed set must match SignedHeaders exactly.
	signed := []string{"host", "x-amz-content-sha256", "x-amz-date"}
	if ct := req.Header.Get("Content-Type"); ct != "" {
		signed = append(signed, "content-type")
	}
	sortStrings(signed)

	var canonHeaders strings.Builder
	for _, h := range signed {
		v := req.Header.Get(h)
		if h == "host" {
			v = req.URL.Host
		}
		canonHeaders.WriteString(h)
		canonHeaders.WriteString(":")
		canonHeaders.WriteString(strings.TrimSpace(v))
		canonHeaders.WriteString("\n")
	}
	signedHeaders := strings.Join(signed, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		req.URL.RawQuery,
		canonHeaders.String(),
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, s.cfg.Region, "s3", "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	kDate := hmacSHA256([]byte("AWS4"+s.cfg.SecretKey), dateStamp)
	kRegion := hmacSHA256(kDate, s.cfg.Region)
	kService := hmacSHA256(kRegion, "s3")
	kSigning := hmacSHA256(kService, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(kSigning, stringToSign))

	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, signature))
	return nil
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

// sortStrings is an insertion sort over the handful of signed header names — small
// enough that pulling in sort for it would be the heavier choice.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- helpers ----------------------------------------------------------------

func drain(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

// peek reads a bounded slice of an error body for the log. S3 errors are XML and the
// useful part (the Code element) is near the front.
func peek(resp *http.Response) string {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	return strings.TrimSpace(string(b))
}
