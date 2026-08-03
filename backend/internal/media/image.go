package media

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"net/http"

	// Registered for their decoders only. A format we cannot decode is a format we
	// cannot re-encode, and we never store a file we did not produce — so this list
	// IS the accepted-format list.
	_ "image/gif"
	_ "image/png"

	"github.com/agent-arena/arena/internal/httpx"
)

// Limits on what an avatar may be. These are deliberately tight: an avatar is a small
// square rendered at 96px, so anything larger is either a phone photo straight off the
// camera (fine, we downscale it) or an attempt to make the server do work.
const (
	// MaxUploadBytes caps the request body. 8 MiB comfortably fits a modern phone
	// photo; the re-encoded result is typically 30–80 KiB.
	MaxUploadBytes = 8 << 20
	// maxPixels bounds the DECODED image, checked from the header before any pixels
	// are allocated. A 200×200 PNG can declare 50000×50000 and cost gigabytes to
	// decode — the classic decompression bomb — and a byte-length limit does not catch
	// it, because the file itself is tiny.
	maxPixels = 40_000_000 // 40 MP: beyond any real camera, far below a bomb
	// avatarEdge is the stored square edge. Serving a 512px source for a 96px avatar
	// wastes bandwidth on every profile view; 512 keeps it crisp on a 2x display at
	// the largest size the UI uses (104px) with room for future layouts.
	avatarEdge = 512
	// jpegQuality — 82 is the knee of the quality/size curve for photographs.
	jpegQuality = 82
)

// ErrNotFound is returned by Store.Get for a key that does not exist.
var ErrNotFound = errors.New("media: object not found")

func errUnavailable(msg string) error {
	return httpx.NewError(http.StatusServiceUnavailable, "media_unavailable", msg)
}

func errBadImage(msg string) error {
	return httpx.NewError(http.StatusBadRequest, "invalid_image", msg)
}

// Prepared is a re-encoded image ready to store, named by its own content.
type Prepared struct {
	Key         string // e.g. "avatars/ab12….jpg"
	ContentType string
	Bytes       []byte
}

// PrepareAvatar turns arbitrary uploaded bytes into a stored-shape avatar.
//
// The sequence matters, and each step is a specific defence:
//
//  1. Sniff the type from the BYTES, never from the client's Content-Type or the
//     filename. Both are attacker-controlled and neither tells us what will actually
//     be decoded by a browser.
//  2. Read the header only (DecodeConfig) and reject absurd dimensions BEFORE
//     allocating pixels — see maxPixels.
//  3. Decode, centre-crop to a square, downscale to avatarEdge.
//  4. Re-encode to JPEG. This is the step that makes the output safe to serve: the
//     result contains only pixels we wrote, so EXIF (including GPS from a phone
//     photo), embedded thumbnails, colour profiles and any appended payload are all
//     gone. It also means an "image" that is really an HTML or SVG document cannot
//     reach a browser — those do not decode here at all.
//  5. Name the object after the SHA-256 of the FINAL bytes, so the key describes
//     exactly what is stored.
//
// SVG is deliberately unsupported. It is a document format with script and external
// references, and there is no way to re-encode it into something inert.
func PrepareAvatar(raw []byte) (Prepared, error) {
	if len(raw) == 0 {
		return Prepared{}, errBadImage("the uploaded file was empty")
	}
	if len(raw) > MaxUploadBytes {
		return Prepared{}, httpx.NewError(http.StatusRequestEntityTooLarge, "image_too_large",
			fmt.Sprintf("images must be %d MB or smaller", MaxUploadBytes>>20))
	}

	// 1. What is it really?
	switch ct := http.DetectContentType(raw); ct {
	case "image/jpeg", "image/png", "image/gif":
	default:
		return Prepared{}, errBadImage("unsupported image type " + ct + " — use JPEG, PNG or GIF")
	}

	// 2. Bound it before decoding.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		return Prepared{}, errBadImage("that file could not be read as an image")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		return Prepared{}, errBadImage("that image has no dimensions")
	}
	if cfg.Width*cfg.Height > maxPixels {
		return Prepared{}, errBadImage("that image is too large to process")
	}

	// 3. Decode and normalise geometry.
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return Prepared{}, errBadImage("that image could not be decoded")
	}
	square := cropSquare(src)
	out := downscale(square, avatarEdge)

	// 4. Re-encode. Flattened onto opaque white first: JPEG has no alpha, and encoding
	// a transparent PNG straight to JPEG turns every transparent pixel BLACK, which is
	// why a logo with a clear background comes out on a black card.
	flat := image.NewRGBA(out.Bounds())
	draw.Draw(flat, flat.Bounds(), image.NewUniform(image.White), image.Point{}, draw.Src)
	draw.Draw(flat, flat.Bounds(), out, out.Bounds().Min, draw.Over)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, flat, &jpeg.Options{Quality: jpegQuality}); err != nil {
		return Prepared{}, fmt.Errorf("media: encode avatar: %w", err)
	}
	encoded := buf.Bytes()

	// 5. The key is the content.
	return Prepared{
		Key:         "avatars/" + sha256Hex(encoded) + ".jpg",
		ContentType: "image/jpeg",
		Bytes:       encoded,
	}, nil
}

// cropSquare takes the largest centred square from an image. Cropping rather than
// squashing: a stretched face is worse than a cropped one, and every avatar slot in
// the UI is a circle, which a non-square image cannot fill without distortion.
func cropSquare(src image.Image) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w == h {
		return src
	}
	edge := w
	if h < w {
		edge = h
	}
	x0 := b.Min.X + (w-edge)/2
	y0 := b.Min.Y + (h-edge)/2
	rect := image.Rect(x0, y0, x0+edge, y0+edge)
	// SubImage keeps the original pixels; the crop costs nothing.
	if si, ok := src.(interface {
		SubImage(image.Rectangle) image.Image
	}); ok {
		return si.SubImage(rect)
	}
	dst := image.NewRGBA(image.Rect(0, 0, edge, edge))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

// downscale resizes a square image to edge×edge by AREA AVERAGING, and returns the
// source untouched when it is already no larger (never upscale — enlarging a 64px
// avatar to 512 only makes a blurry file four times the size).
//
// Area averaging rather than nearest-neighbour: dropping pixels on a 4000px phone
// photo aliases badly, and hair and text turn to noise. Averaging every source pixel
// that falls inside a destination pixel is the cheapest filter that looks correct, and
// it is a few lines — which is why there is no image-resizing dependency here.
func downscale(src image.Image, edge int) image.Image {
	b := src.Bounds()
	if b.Dx() <= edge {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, edge, edge))
	sw, sh := b.Dx(), b.Dy()

	for dy := 0; dy < edge; dy++ {
		// Source band for this destination row, computed in integer arithmetic so the
		// bands tile the source exactly with no gap and no overlap.
		sy0 := b.Min.Y + dy*sh/edge
		sy1 := b.Min.Y + (dy+1)*sh/edge
		if sy1 <= sy0 {
			sy1 = sy0 + 1
		}
		for dx := 0; dx < edge; dx++ {
			sx0 := b.Min.X + dx*sw/edge
			sx1 := b.Min.X + (dx+1)*sw/edge
			if sx1 <= sx0 {
				sx1 = sx0 + 1
			}
			var rs, gs, bs, as uint64
			var n uint64
			for sy := sy0; sy < sy1; sy++ {
				for sx := sx0; sx < sx1; sx++ {
					r, g, bl, a := src.At(sx, sy).RGBA() // 16-bit, alpha-premultiplied
					rs += uint64(r)
					gs += uint64(g)
					bs += uint64(bl)
					as += uint64(a)
					n++
				}
			}
			if n == 0 {
				continue
			}
			// >>8 converts the 16-bit channel average back to 8-bit. RGBA() returns
			// alpha-premultiplied values and image.RGBA stores premultiplied, so the
			// averages carry across directly with no conversion.
			dst.SetRGBA(dx, dy, color.RGBA{
				R: uint8(rs / n >> 8),
				G: uint8(gs / n >> 8),
				B: uint8(bs / n >> 8),
				A: uint8(as / n >> 8),
			})
		}
	}
	return dst
}
