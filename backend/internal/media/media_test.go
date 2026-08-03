package media

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// --- image pipeline ---------------------------------------------------------

func pngBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The stored object is named after the bytes we produced, so the same picture is one
// object however many times it is uploaded, and a key can never come to mean something
// else. That is what makes the serve path safe to cache forever.
func TestKeyIsTheContentHashAndIsStable(t *testing.T) {
	src := pngBytes(t, 300, 300, color.RGBA{10, 200, 90, 255})

	a, err := PrepareAvatar(src)
	if err != nil {
		t.Fatal(err)
	}
	b, err := PrepareAvatar(src)
	if err != nil {
		t.Fatal(err)
	}
	if a.Key != b.Key {
		t.Fatalf("same image produced two keys: %q vs %q", a.Key, b.Key)
	}
	if !strings.HasPrefix(a.Key, "avatars/") || !strings.HasSuffix(a.Key, ".jpg") {
		t.Fatalf("unexpected key shape: %q", a.Key)
	}
	if !validAvatarName(strings.TrimPrefix(a.Key, "avatars/")) {
		t.Fatalf("PrepareAvatar produced a key the serve path would reject: %q", a.Key)
	}
	// Different content ⇒ different key, or one image could overwrite another.
	other, err := PrepareAvatar(pngBytes(t, 300, 300, color.RGBA{200, 10, 10, 255}))
	if err != nil {
		t.Fatal(err)
	}
	if other.Key == a.Key {
		t.Fatal("two different images hashed to the same key")
	}
}

// Nothing the uploader sent is stored: the output is always a JPEG we encoded from
// pixels. This is what strips EXIF (a phone photo carries GPS), embedded thumbnails,
// and anything appended to the file.
func TestOutputIsAlwaysAReEncodedJpeg(t *testing.T) {
	// A PNG with a bogus text chunk standing in for metadata that must not survive.
	src := pngBytes(t, 200, 200, color.RGBA{0, 0, 255, 255})
	marker := []byte("SECRET-EXIF-PAYLOAD")
	src = append(src, marker...) // trailing junk after IEND — decoders ignore it

	got, err := PrepareAvatar(src)
	if err != nil {
		t.Fatal(err)
	}
	if got.ContentType != "image/jpeg" {
		t.Fatalf("content type = %q, want image/jpeg", got.ContentType)
	}
	if bytes.Contains(got.Bytes, marker) {
		t.Fatal("uploaded bytes survived into the stored object — re-encoding did not happen")
	}
	if _, err := jpeg.Decode(bytes.NewReader(got.Bytes)); err != nil {
		t.Fatalf("stored object is not a decodable JPEG: %v", err)
	}
}

// A tall or wide image becomes a square, because every avatar slot is a circle and a
// squashed face is worse than a cropped one. Small images are NOT enlarged.
func TestGeometryIsSquaredAndNeverUpscaled(t *testing.T) {
	// A panorama crops to its SHORT edge, and 400 is already under the target — so the
	// result is a 400px square, not an upscaled 512. Both properties matter: square,
	// and never larger than it arrived.
	wide, err := PrepareAvatar(pngBytes(t, 1200, 400, color.RGBA{9, 9, 9, 255}))
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(wide.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	if b.Dx() != b.Dy() {
		t.Fatalf("stored %dx%d — not square", b.Dx(), b.Dy())
	}
	if b.Dx() != 400 {
		t.Fatalf("stored edge %d, want the 400px short edge (crop, no upscale)", b.Dx())
	}

	// A phone-sized photo is genuinely downscaled to the stored edge.
	big, err := PrepareAvatar(pngBytes(t, 1600, 1200, color.RGBA{9, 9, 9, 255}))
	if err != nil {
		t.Fatal(err)
	}
	bimg, err := jpeg.Decode(bytes.NewReader(big.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if got := bimg.Bounds(); got.Dx() != avatarEdge || got.Dy() != avatarEdge {
		t.Fatalf("stored %dx%d, want %d square", got.Dx(), got.Dy(), avatarEdge)
	}

	small, err := PrepareAvatar(pngBytes(t, 64, 64, color.RGBA{9, 9, 9, 255}))
	if err != nil {
		t.Fatal(err)
	}
	simg, err := jpeg.Decode(bytes.NewReader(small.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	if got := simg.Bounds().Dx(); got != 64 {
		t.Fatalf("a 64px avatar was resized to %d — upscaling only makes a blurry, bigger file", got)
	}
}

// A transparent PNG must not come out on a black card. JPEG has no alpha, so the
// image is flattened onto white first; without that every transparent pixel encodes
// as black, which is what happens to a logo with a clear background.
func TestTransparencyFlattensToWhiteNotBlack(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 100, 100))
	// Fully transparent everywhere.
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	got, err := PrepareAvatar(buf.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	out, err := jpeg.Decode(bytes.NewReader(got.Bytes))
	if err != nil {
		t.Fatal(err)
	}
	r, g, b, _ := out.At(50, 50).RGBA()
	if r < 0xF000 || g < 0xF000 || b < 0xF000 {
		t.Fatalf("transparent pixel encoded as rgb(%d,%d,%d) — want near-white", r>>8, g>>8, b>>8)
	}
}

// Things that are not images, or are hostile, are refused before anything is stored.
func TestRejectsNonImagesAndBombs(t *testing.T) {
	cases := map[string][]byte{
		"empty":      {},
		"plain text": []byte("this is not an image"),
		// An SVG is a document with script and remote references, and cannot be
		// re-encoded into something inert — so it is not an accepted format.
		"svg":                           []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`),
		"html masquerading as an image": []byte("<!doctype html><html><body>hi</body></html>"),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := PrepareAvatar(raw); err == nil {
				t.Fatalf("%s was accepted as an avatar", name)
			}
		})
	}

	t.Run("oversized body", func(t *testing.T) {
		if _, err := PrepareAvatar(make([]byte, MaxUploadBytes+1)); err == nil {
			t.Fatal("a body over the cap was accepted")
		}
	})

	// A decompression bomb: a tiny file that declares enormous dimensions. A byte-length
	// limit does not catch this, which is why the header is checked before decoding.
	t.Run("declared-dimension bomb", func(t *testing.T) {
		big := image.NewRGBA(image.Rect(0, 0, 1, 1))
		var buf bytes.Buffer
		_ = png.Encode(&buf, big)
		b := buf.Bytes()
		// Rewrite the IHDR width/height to 30000x30000 (0x7530). IHDR data starts at
		// offset 16 in a PNG: 8-byte signature + 4-byte length + 4-byte "IHDR".
		for i, v := range []byte{0x00, 0x00, 0x75, 0x30, 0x00, 0x00, 0x75, 0x30} {
			b[16+i] = v
		}
		if _, err := PrepareAvatar(b); err == nil {
			t.Fatal("an image declaring 900 megapixels was accepted")
		}
	})
}

// --- serve-path key validation ---------------------------------------------

// The serve path validates the SHAPE of the name rather than sanitising it, which is
// what makes traversal impossible: nothing that is not 64 hex chars + .jpg gets through.
func TestServeRejectsAnythingButAContentHash(t *testing.T) {
	good := strings.Repeat("a", 64) + ".jpg"
	if !validAvatarName(good) {
		t.Fatal("a legitimate content-hash name was rejected")
	}
	for _, bad := range []string{
		"../../../etc/passwd",
		"../" + strings.Repeat("a", 61) + ".jpg",
		strings.Repeat("a", 64) + ".png",
		strings.Repeat("a", 63) + ".jpg",
		strings.Repeat("A", 64) + ".jpg", // uppercase: not what we emit
		strings.Repeat("z", 64) + ".jpg", // 'z' is not hex
		"avatars/" + strings.Repeat("a", 64) + ".jpg",
		"",
	} {
		if validAvatarName(bad) {
			t.Fatalf("accepted a name it should refuse: %q", bad)
		}
	}
}

// --- object store: SigV4 wire format ---------------------------------------

// The signature covers the method, path, payload hash and the exact set of headers
// named in SignedHeaders. A mismatch between the two is the classic SigV4 bug and
// shows up only as a 403 from the store, so it is pinned here.
func TestPutSignsTheRequestItSends(t *testing.T) {
	var gotAuth, gotSHA, gotDate, gotCT, gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotSHA = r.Header.Get("X-Amz-Content-Sha256")
		gotDate = r.Header.Get("X-Amz-Date")
		gotCT = r.Header.Get("Content-Type")
		gotPath, gotMethod = r.URL.Path, r.Method
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := New(Config{Endpoint: srv.URL, Bucket: "pyyol-media", AccessKey: "AK", SecretKey: "SK"})
	body := []byte("jpeg-bytes")
	if err := s.Put(context.Background(), "avatars/abc.jpg", "image/jpeg", body); err != nil {
		t.Fatal(err)
	}

	if gotMethod != http.MethodPut {
		t.Fatalf("method = %s", gotMethod)
	}
	if gotPath != "/pyyol-media/avatars/abc.jpg" {
		t.Fatalf("path = %q — path-style bucket addressing expected", gotPath)
	}
	if gotSHA != sha256Hex(body) {
		t.Fatal("payload hash header does not match the body actually sent")
	}
	if gotCT != "image/jpeg" || gotDate == "" {
		t.Fatalf("content-type=%q date=%q", gotCT, gotDate)
	}
	if !strings.HasPrefix(gotAuth, "AWS4-HMAC-SHA256 Credential=AK/") {
		t.Fatalf("authorization header malformed: %q", gotAuth)
	}
	// Every signed header must be present on the request, or the store recomputes a
	// different signature and answers 403.
	for _, h := range []string{"host", "x-amz-content-sha256", "x-amz-date", "content-type"} {
		if !strings.Contains(gotAuth, h) {
			t.Fatalf("SignedHeaders is missing %q: %q", h, gotAuth)
		}
	}
}

// A missing object is a distinct, non-error condition — the serve path turns it into a
// 404 rather than a 502.
func TestGetMapsMissingObjectToErrNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	s := New(Config{Endpoint: srv.URL, Bucket: "b", AccessKey: "AK", SecretKey: "SK"})
	if _, err := s.Get(context.Background(), "avatars/none.jpg"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// An unconfigured store is a valid object that fails cleanly. A deployment with no
// bucket must serve matches, not panic on a nil client.
func TestUnconfiguredStoreIsSafe(t *testing.T) {
	s := New(Config{})
	if s.Enabled() {
		t.Fatal("an empty config reported enabled")
	}
	if err := s.Put(context.Background(), "k", "image/jpeg", []byte("x")); err == nil {
		t.Fatal("put on an unconfigured store did not fail")
	}
	if _, err := s.Get(context.Background(), "k"); err == nil {
		t.Fatal("get on an unconfigured store did not fail")
	}
}

// --- live MinIO round trip -------------------------------------------------

// The test that actually matters for "does it persist": a real S3 implementation.
//
// Every other test here proves the shape of what we send. Only this one proves MinIO
// ACCEPTS it — SigV4 is exactly the kind of code that passes a self-consistent unit
// test and gets a 403 from a real server. Skipped unless MEDIA_S3_ENDPOINT is set.
func TestLiveMinIORoundTrip(t *testing.T) {
	endpoint := os.Getenv("MEDIA_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("MEDIA_S3_ENDPOINT unset — needs a running MinIO")
	}
	s := New(Config{
		Endpoint:  endpoint,
		Bucket:    envOr("MEDIA_S3_BUCKET", "pyyol-media"),
		AccessKey: envOr("MEDIA_S3_ACCESS_KEY", "minioadmin"),
		SecretKey: envOr("MEDIA_S3_SECRET_KEY", "minioadmin"),
	})
	ctx := context.Background()
	if err := s.EnsureBucket(ctx); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}

	prepared, err := PrepareAvatar(pngBytes(t, 900, 600, color.RGBA{200, 40, 120, 255}))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, prepared.Key, prepared.ContentType, prepared.Bytes); err != nil {
		t.Fatalf("Put: %v", err)
	}

	obj, err := s.Get(ctx, prepared.Key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer obj.Body.Close()
	var back bytes.Buffer
	if _, err := back.ReadFrom(obj.Body); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(back.Bytes(), prepared.Bytes) {
		t.Fatalf("read back %d bytes, stored %d — the object is not what we wrote",
			back.Len(), len(prepared.Bytes))
	}
	if obj.ContentType != "image/jpeg" {
		t.Fatalf("content type came back as %q", obj.ContentType)
	}
	// Re-Put the same key: content addressing makes a retry idempotent, and this is
	// the path a client retry after a timeout actually takes.
	if err := s.Put(ctx, prepared.Key, prepared.ContentType, prepared.Bytes); err != nil {
		t.Fatalf("re-Put of an identical object failed: %v", err)
	}
	t.Logf("live: stored + read back %d bytes at %s", len(prepared.Bytes), prepared.Key)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
